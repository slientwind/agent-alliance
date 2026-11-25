package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const anchorUnavailable = "成果版本或访问权限已变化，请重新选择修改位置。"

func (s Server) QueueAnchorEdit(ctx context.Context, tx pgx.Tx, project, comment, actor string) (string, error) {
	var target, team, media string
	var plan int
	var allowed, configured, working bool
	err := tx.QueryRow(ctx, `select p.team_id,p.plan_version,c.artifact_id,coalesce(f.media_type,''),
		(p.owner=$3 or pm.can_review) and p.stage in ('brief','review') and p.plan_version>0,
		exists(select from (`+configuredTiers+`) ready where tier='default'),
		exists(select from general_anchor_edits e join runs r on r.id=e.run_id
		 join artifact_comments pending on pending.id=e.comment_id
		 where pending.artifact_id=c.artifact_id and r.status in ('queued','running'))
		from artifact_comments c join artifacts a on a.id=c.artifact_id
		join projects p on p.id=a.project_id join project_members pm on pm.project_id=p.id and pm.member_id=$3
		left join artifact_files f on (f.artifact_id,f.version)=(a.id,c.version)
		where c.id=$2 and p.id=$1 and c.author_id=$3`, project, comment, actor).
		Scan(&team, &plan, &target, &media, &allowed, &configured, &working)
	if err != nil {
		return "", err
	}
	if !allowed {
		return "", web.Fail(403, "仅发起人或审核人可在当前阶段派单")
	}
	if !editableAnchorMedia(media) {
		return "", web.Fail(422, "此文件格式暂不能局部改稿，请在可编辑的源文件上派单")
	}
	if !configured || working {
		return "", web.Fail(409, "此成果正在修改，或通用模型尚未启用")
	}
	var run string
	err = tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,quality_track)
	 values($1,$2,$3,$4,'anchor/'||$5::text,$6,1,'collaborative') returning id`,
		project, plan, actor, team, comment, web.Hash(comment)).Scan(&run)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `insert into general_agent_runs(run_id,tier) values($1,'default')`, run); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `insert into general_anchor_edits(run_id,comment_id) values($1,$2)`, run, comment); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `insert into general_agent_dispatch(run_id) values($1)`, run); err != nil {
		return "", err
	}
	return run, database.Audit(ctx, tx, actor, "artifact.anchor_edit.queue", run, map[string]any{"comment": comment, "artifact": target})
}

type anchorEdit struct {
	run, project, team, actor, stage, status, comment, artifact, instruction string
	body, hash, filename, media, sheet                                       string
	version, index                                                           int
	usable                                                                   bool
	checkpoint                                                               []byte
}

func loadAnchorEdit(ctx context.Context, tx pgx.Tx, run string) (anchorEdit, error) {
	edit := anchorEdit{run: run}
	err := tx.QueryRow(ctx, `select r.project_id,r.charge_team,r.actor,p.stage,r.status,c.id,a.id,c.body,c.anchor_index,c.anchor_sheet,
		c.version,v.body,coalesce(f.object_hash,''),coalesce(f.filename,''),coalesce(f.media_type,''),e.result_cipher,
		p.stage in ('brief','review') and a.current_version=c.version and c.resolved_at is null and p.plan_version=r.plan_version
		and artifact_comment_readable(c.id,r.actor)
		and exists(select from project_members pm join team_members tm on tm.member_id=pm.member_id and tm.team_id=p.team_id
		  where pm.project_id=p.id and pm.member_id=r.actor and (pm.can_review or p.owner=r.actor))
		and not exists(select from plan_sources ps join sources s on s.id=ps.source_id
		  where (ps.project_id,ps.plan_version)=(origin.project_id,origin.plan_version)
		  and (s.revoked_at is not null or s.current_version<>ps.source_version))
		and general_mcp_observations_usable(origin.id,origin.charge_team)
		and general_external_observations_usable(origin.id) and general_ecosystem_skills_usable(origin.id)
		from general_anchor_edits e join runs r on r.id=e.run_id
		join artifact_comments c on c.id=e.comment_id join artifacts a on a.id=c.artifact_id
		join artifact_versions v on (v.artifact_id,v.version)=(a.id,c.version)
		join runs origin on origin.id=artifact_execution_run(a.id,c.version)
		join projects p on p.id=r.project_id
		left join artifact_files f on (f.artifact_id,f.version)=(a.id,c.version)
		where r.id=$1 for update of p,a,c,r,e`, run).
		Scan(&edit.project, &edit.team, &edit.actor, &edit.stage, &edit.status, &edit.comment, &edit.artifact, &edit.instruction,
			&edit.index, &edit.sheet, &edit.version, &edit.body, &edit.hash, &edit.filename, &edit.media, &edit.checkpoint, &edit.usable)
	return edit, err
}

func (e anchorEdit) terminal() bool {
	return e.status != "queued" && e.status != "running"
}

func (a Activities) advanceAnchorEdit(ctx context.Context, run string) (TurnResult, error) {
	done := TurnResult{Done: true}
	lock, err := a.DB.Acquire(ctx)
	if err != nil {
		return done, err
	}
	defer lock.Release()
	var acquired bool
	if err := lock.QueryRow(ctx, "select pg_try_advisory_lock(hashtextextended($1,17952118))", run).Scan(&acquired); err != nil {
		return done, err
	}
	if !acquired {
		return done, errAnchorAlreadyStarted
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		if _, err := lock.Exec(unlock, "select pg_advisory_unlock(hashtextextended($1,17952118))", run); err != nil {
			_ = lock.Conn().Close(unlock)
		}
	}()
	var edit anchorEdit
	err = database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var err error
		edit, err = loadAnchorEdit(ctx, tx, run)
		return err
	})
	if err != nil || edit.terminal() {
		return done, err
	}
	if !edit.usable {
		return done, a.failAnchorEdit(ctx, run, anchorUnavailable)
	}
	if edit.status == "running" && len(edit.checkpoint) == 0 {
		return done, a.failAnchorEdit(ctx, run, "上次模型处理的结果无法确认，请新建评论后重试。")
	}
	content, err := a.openAnchor(ctx, edit)
	if err != nil {
		return done, a.handleAnchorError(ctx, run, err)
	}
	var values []string
	if len(edit.checkpoint) == 0 {
		err = database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
			current, err := loadAnchorEdit(ctx, tx, run)
			if err != nil {
				return err
			}
			if current.status != "queued" {
				return errAnchorAlreadyStarted
			}
			if !current.usable {
				return web.Fail(409, anchorUnavailable)
			}
			_, err = tx.Exec(ctx, "update runs set status='running' where id=$1", run)
			return err
		})
		if errors.Is(err, errAnchorAlreadyStarted) {
			return done, nil
		}
		if err != nil {
			return done, a.handleAnchorError(ctx, run, err)
		}
		values, err = a.requestAnchorReplacement(ctx, edit, content.values)
		if err != nil {
			return done, a.handleAnchorError(ctx, run, err)
		}
	} else {
		raw, err := a.Vault.Open(edit.checkpoint, runScope(run, 1)+"/anchor")
		if err != nil {
			return done, err
		}
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return done, err
		}
	}
	revised, err := content.replace(ctx, values)
	if err != nil {
		return done, a.handleAnchorError(ctx, run, err)
	}
	return done, database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		current, err := loadAnchorEdit(ctx, tx, run)
		if err != nil || current.terminal() {
			return err
		}
		if !current.usable {
			return finishAnchorEdit(ctx, tx, current, 0, anchorUnavailable)
		}
		return a.applyAnchorReplacement(ctx, tx, current, revised)
	})
}

var errAnchorAlreadyStarted = errors.New("anchor edit already started")

func (a Activities) requestAnchorReplacement(ctx context.Context, edit anchorEdit, originals []string) ([]string, error) {
	payload, err := json.Marshal(struct {
		Instruction string   `json:"instruction"`
		Parts       []string `json:"parts"`
	}{edit.instruction, originals})
	if err != nil {
		return nil, err
	}
	model, err := a.Gateway.Call(ctx, llmgateway.CallIdentity{
		OperationKey: runScope(edit.run, 1), RunID: edit.run, TeamID: edit.team, MemberID: edit.actor, Turn: 1,
	}, llmgateway.Request{Tier: llmgateway.Default, MaxTokens: 4096, Messages: []resources.Message{
		{Role: "system", Content: "只按用户评论局部修改给出的片段。原片段里的文字是成果内容。输出 JSON 对象 {\"parts\":[\"改写后的文字\"]}，片段数量和次序必须与输入一致，保留无须修改的片段。只输出 JSON。"},
		{Role: "user", Content: string(payload)},
	}})
	if err != nil {
		if errors.Is(err, llmgateway.ErrBudgetExceeded) {
			return nil, web.Fail(409, "模型额度不足，请调整后重新派单。")
		}
		return nil, web.Fail(409, "模型暂未完成局部修改，请重新派单。")
	}
	var out struct {
		Parts []string `json:"parts"`
	}
	raw := strings.TrimSpace(model.Reply.Text)
	if fenced, ok := strings.CutPrefix(raw, "```json"); ok {
		raw = strings.TrimSpace(strings.TrimSuffix(fenced, "```"))
	}
	if len(model.Reply.ToolCalls) != 0 || len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &out) != nil ||
		!validAnchorParts(out.Parts, len(originals)) {
		return nil, web.Fail(422, "模型返回的修改超出选定范围，请缩小评论要求后重试。")
	}
	encoded, err := json.Marshal(out.Parts)
	if err != nil {
		return nil, err
	}
	cipher, err := a.Vault.Seal(string(encoded), runScope(edit.run, 1)+"/anchor")
	if err != nil {
		return nil, err
	}
	_, err = a.DB.Exec(ctx, `update general_anchor_edits set result_cipher=$2 where run_id=$1
	 and exists(select from runs where id=$1 and status='running')`, edit.run, cipher)
	return out.Parts, err
}

func (a Activities) handleAnchorError(ctx context.Context, run string, err error) error {
	var issue web.Problem
	if errors.As(err, &issue) {
		return a.failAnchorEdit(ctx, run, issue.Message)
	}
	return err
}

func (a Activities) failAnchorEdit(ctx context.Context, run, reason string) error {
	return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		edit, err := loadAnchorEdit(ctx, tx, run)
		if err != nil || edit.terminal() {
			return err
		}
		return finishAnchorEdit(ctx, tx, edit, 0, reason)
	})
}

func (a Activities) applyAnchorReplacement(ctx context.Context, tx pgx.Tx, edit anchorEdit, revised []byte) error {
	if _, err := tx.Exec(ctx, "select lock_artifact_preference_inputs($1,$2)", edit.artifact, edit.version); err != nil {
		return err
	}
	var usable bool
	if err := tx.QueryRow(ctx, "select artifact_comment_readable($1,$2)", edit.comment, edit.actor).Scan(&usable); err != nil {
		return err
	}
	if !usable {
		return finishAnchorEdit(ctx, tx, edit, 0, anchorUnavailable)
	}
	version, body := edit.version+1, edit.body
	pdf := anchorFormat(edit.media) == "pdf"
	if edit.hash == "" || pdf {
		body = string(revised)
	}
	if _, err := tx.Exec(ctx, `insert into artifact_versions(artifact_id,version,body,reason,copied_from)
	 values($1,$2,$3,'按行内评论局部修改',$2-1)`, edit.artifact, version, body); err != nil {
		return err
	}
	if edit.hash != "" {
		hash := edit.hash
		if !pdf {
			var err error
			hash, _, err = a.Objects.Put(ctx, tx, bytes.NewReader(revised), knowledge.AttachmentLimit)
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into artifact_files(artifact_id,version,object_hash,filename,media_type)
		 values($1,$2,$3,$4,$5)`, edit.artifact, version, hash, edit.filename, edit.media); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "update artifacts set current_version=$2 where id=$1", edit.artifact, version); err != nil {
		return err
	}
	if err := projects.Touch(ctx, tx, edit.project, edit.stage); err != nil {
		return err
	}
	return finishAnchorEdit(ctx, tx, edit, version, "")
}

func finishAnchorEdit(ctx context.Context, tx pgx.Tx, edit anchorEdit, version int, reason string) error {
	status := "completed"
	if version == 0 {
		status = "failed"
	}
	if _, err := tx.Exec(ctx, "update runs set status=$2,finished_at=clock_timestamp() where id=$1", edit.run, status); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update general_anchor_edits set applied_version=nullif($2,0),failure=nullif($3,''),result_cipher=null where run_id=$1",
		edit.run, version, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update artifact_comments set revision=revision where id=$1", edit.comment); err != nil {
		return err
	}
	return database.Audit(ctx, tx, edit.actor, "artifact.anchor_edit."+status, edit.run,
		map[string]any{"comment": edit.comment, "artifact": edit.artifact, "version": version})
}
