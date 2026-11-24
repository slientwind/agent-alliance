package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"context"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type slideEdit struct {
	Page        int
	Title, Body string
}

// Use the same conservative full-width units as the slide renderer.
func slideTextWidth(s string) int {
	units := 0
	for _, c := range s {
		switch {
		case unicode.IsMark(c):
		case c < 127 && !(c >= 'A' && c <= 'Z') && !strings.ContainsRune("wm@%", c):
			units++
		default:
			units += 2
		}
	}
	return units
}

func slideControl(c rune) bool {
	return unicode.IsControl(c) || c == '\u2028' || c == '\u2029'
}

func (edit slideEdit) replace(base string) (string, error) {
	if edit.Page < 1 || edit.Page > 40 || strings.IndexFunc(edit.Title, slideControl) >= 0 {
		return "", web.Fail(422, "请核对页序和标题")
	}
	title := strings.TrimSpace(edit.Title)
	if title == "" || slideTextWidth(title) > 50 {
		return "", web.Fail(422, "请填写短标题，最多 25 个全角字符")
	}
	var paragraphs []string
	for _, line := range strings.Split(strings.ReplaceAll(edit.Body, "\r\n", "\n"), "\n") {
		if strings.IndexFunc(line, slideControl) >= 0 {
			return "", web.Fail(422, "正文含有无法显示的控制字符")
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if slideTextWidth(line) > 72 {
			return "", web.Fail(422, "单页正文行过长，请分行")
		}
		if strings.HasPrefix(line, "##") {
			return "", web.Fail(422, "单页修改不能增加页标题")
		}
		paragraphs = append(paragraphs, line)
	}
	if len(paragraphs) < 1 || len(paragraphs) > 7 {
		return "", web.Fail(422, "单页需要 1 至 7 行正文")
	}
	pages := strings.Split(base, "\n\n")
	if edit.Page > len(pages) {
		return "", web.Fail(422, "当前课件没有此页")
	}
	replacement := "## " + title + "\n" + strings.Join(paragraphs, "\n")
	if pages[edit.Page-1] == replacement {
		return "", web.Fail(422, "此页内容未修改")
	}
	pages[edit.Page-1] = replacement
	return strings.Join(pages, "\n\n"), nil
}

func (s Server) startChange(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Version, Expected    int
		AuthorizeOneCall     bool
		Replacement, Excerpt string
		Slide                *slideEdit
		ImageRegion          *knowledge.ImageRegion
		SourceAssessment     string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 180 || !in.AuthorizeOneCall {
		return web.Fail(422, "请明确授权本次单步调用并提供幂等键")
	}
	if len([]rune(in.Replacement)) > 64000 || strings.Count(in.Replacement, "\n") >= 400 {
		return web.Fail(422, "替换稿最多 64000 字、400 行")
	}
	artifact, feedback := r.PathValue("artifact"), r.PathValue("feedback")
	signature := fmt.Sprintf("change/%s/%s/%d/%d/%q/%q", artifact, feedback, in.Version, in.Expected, in.Replacement, in.Excerpt)
	if in.SourceAssessment != "" {
		signature += "/source-assessment/" + in.SourceAssessment
	}
	if in.Slide != nil {
		if in.Replacement != "" || in.Excerpt != "" || len([]rune(in.Slide.Title))+len([]rune(in.Slide.Body)) > 64000 {
			return web.Fail(422, "单页修改仅接受该页的标题和正文")
		}
		signature += fmt.Sprintf("/slide/%d/%q/%q", in.Slide.Page, in.Slide.Title, in.Slide.Body)
	}
	if in.ImageRegion != nil {
		r := in.ImageRegion
		signature += fmt.Sprintf("/image-region/%d/%d/%d/%d", r.X, r.Y, r.Width, r.Height)
	}
	sig := web.Hash(signature)
	ctx := r.Context()
	var run string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		var match bool
		err = tx.QueryRow(ctx, "select id,request_hash=$1 and project_id=$4 from runs where actor=$2 and request_key=$3", sig, u.ID, key, p.ID).Scan(&run, &match)
		if err == nil {
			if !match {
				return web.Fail(409, "该幂等键已用于其他请求")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var allowed, admitted bool
		var tool, state, original, lookupPhase string
		var revision, version, ordinal, plan int
		err = tx.QueryRow(ctx, `select pm.can_review,a.current_version,a.ordinal,a.run_id,r.plan_version,f.revision,f.state,n.lookup_phase,
   case when agent_edits_picture(v) then 'image-edit' when agent_output_operation(v)='png' then 'png' else coalesce(v.tool->>'operation','') end,agent_verified(n.agent_id,n.agent_revision) and not agent.disabled and not agent.emergency and coalesce(m.enabled,true) and project_agent_admissible(a.project_id,n.agent_id,n.agent_revision,$3)
   from artifacts a join runs r on r.id=a.run_id join effective_run_nodes n on(n.run_id,n.ordinal)=(r.id,a.ordinal)
   join agents agent on agent.id=n.agent_id join agent_revisions v on(v.agent_id,v.revision)=(n.agent_id,n.agent_revision)
   left join model_resources m on m.id=v.resource_id join feedback f on f.artifact_id=a.id join project_members pm on pm.project_id=a.project_id
   where a.project_id=$1 and a.id=$2 and pm.member_id=$3 and f.id=$4`, p.ID, artifact, u.ID, feedback).Scan(&allowed, &version, &ordinal, &original, &plan, &revision, &state, &lookupPhase, &tool, &admitted)
		if err != nil {
			return err
		}
		if !allowed && p.Owner != u.ID {
			return web.Fail(403, "需要项目制作或审核职责")
		}
		if !admitted {
			return web.Fail(409, "负责此成果的 Agent 或资源不可用")
		}
		if p.Stage != "review" || revision != in.Expected || version != in.Version || state != "required" {
			return web.Fail(409, "请核对当前成果、版本及已采纳修改项")
		}
		if in.ImageRegion != nil && tool != "image-edit" {
			return web.Fail(422, "此成果未绑定参考图编辑 Agent，无法按选区修改")
		}
		if tool == "image-edit" {
			if in.Replacement != "" || in.Excerpt != "" || in.Slide != nil || in.SourceAssessment != "" {
				return web.Fail(422, "图片改稿使用本次固定的成果原件作为基准")
			}
			picture, err := knowledge.EditedArtifactPicture(ctx, tx, artifact, u.ID, version)
			if err != nil {
				return err
			}
			if in.ImageRegion != nil {
				if _, err := in.ImageRegion.ValidateBase(picture); err != nil {
					return err
				}
			}
		}
		var pageNumber *int
		if in.Slide != nil {
			var base string
			if err = tx.QueryRow(ctx, "select coalesce(editable_slide_outline($1,$2),'') where distribution_readable($1,$2,$3)", artifact, version, u.ID).Scan(&base); err != nil {
				return err
			}
			if base == "" {
				return web.Fail(409, "此版本无法按原提纲逐页修改，请核对原件")
			}
			in.Replacement, err = in.Slide.replace(base)
			if err != nil {
				return err
			}
			pageNumber = &in.Slide.Page
		}
		if in.Excerpt != "" {
			if lookupPhase != "" {
				return web.Fail(422, "原文核对任务须连同引用重新生成整稿")
			}
			if tool != "" || strings.TrimSpace(in.Excerpt) == "" {
				return web.Fail(422, "选段修改适用于文本 Agent，需选择原文")
			}
			var baseBody string
			if err = tx.QueryRow(ctx, "select body from artifact_versions where artifact_id=$1 and version=$2", artifact, version).Scan(&baseBody); err != nil {
				return err
			}
			start := strings.Index(baseBody, in.Excerpt)
			if start < 0 || strings.LastIndex(baseBody, in.Excerpt) != start {
				return web.Fail(422, "请提供在当前稿中唯一出现的原文片段")
			}
		}
		switch tool {
		case "", "png", "image-edit":
			if in.Replacement != "" {
				return web.Fail(422, "此能力按反馈生成修订稿")
			}
		case "docx", "pptx", "xlsx":
			if web.Blank(in.Replacement) {
				return web.Fail(422, "此工具需要排版用的完整替换正文")
			}
		default:
			return web.Fail(422, "此工具只能按固定规则处理资料，请使用人工或外部修订")
		}
		err = tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls) select $1,$2,$3,charge_team,$4,$5,1 from runs where id=$6 returning id`, p.ID, plan, u.ID, key, sig, original).Scan(&run)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into change_batches(run_id,feedback_id,feedback_revision,artifact_id,base_version,replacement,excerpt,slide_page,image_region) values($1,$2,$3,$4,$5,$6,$7,$8,$9)", run, feedback, revision, artifact, version, in.Replacement, in.Excerpt, pageNumber, in.ImageRegion); err != nil {
			return err
		}
		if in.SourceAssessment != "" {
			tag, err := tx.Exec(ctx, `update change_batches b set source_assessment=x.assessment from changed_artifact_sources x
             where b.run_id=$1 and x.assessment::text=$2 and (x.feedback_id,x.artifact_id,x.artifact_version)=(b.feedback_id,b.artifact_id,b.base_version)
             and x.decision='change' and not x.revoked and usable_change_source(x.artifact_id,x.source_id,x.pinned,x.observed)`, run, in.SourceAssessment)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "新版资料或修改项已变化，请重新核对后授权")
			}
		}
		if _, err = tx.Exec(ctx, `insert into change_inputs select $1,a.id,a.current_version from node_edges e join artifacts a on a.run_id=$2 and a.ordinal=e.depends_on where e.project_id=$3 and e.plan_version=$4 and e.ordinal=$5`, run, original, p.ID, plan, ordinal); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into outbox(run_id) values($1)", run); err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "change.start", run, map[string]any{"feedback": feedback, "base": version, "step": ordinal, "authorizedCalls": 1, "imageRegion": in.ImageRegion})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]string{"run": run})
}

type changeTarget struct {
	project, artifact, feedback, original, stage, state, disposition string
	ordinal, current, base, revision, feedbackRevision               int
	allowed, staleInputs                                             bool
}

// Lock the same project row used by manual revisions and feedback transitions.
func readTarget(ctx context.Context, tx pgx.Tx, run, actor string) (changeTarget, error) {
	var t changeTarget
	_, err := tx.Exec(ctx, `select p.id from projects p join runs r on r.project_id=p.id where r.id=$1 for update of p`, run)
	if err != nil {
		return t, err
	}
	err = tx.QueryRow(ctx, `select p.id,a.id,f.id,a.run_id,p.stage,f.state,c.disposition,a.ordinal,a.current_version,c.base_version,f.revision,c.feedback_revision,
 exists(select from project_members m join members u on u.id=m.member_id where m.project_id=p.id and m.member_id=$2 and u.enabled and (m.can_review or u.id=p.owner)),
 exists(select from change_inputs i join artifacts dep on dep.id=i.artifact_id where i.run_id=c.run_id and dep.current_version<>i.version)
 from change_batches c join artifacts a on a.id=c.artifact_id join projects p on p.id=a.project_id join feedback f on f.id=c.feedback_id where c.run_id=$1 for update of p`, run, actor).Scan(&t.project, &t.artifact, &t.feedback, &t.original, &t.stage, &t.state, &t.disposition, &t.ordinal, &t.current, &t.base, &t.revision, &t.feedbackRevision, &t.allowed, &t.staleInputs)
	return t, err
}

func applyCandidate(ctx context.Context, tx pgx.Tx, run, actor, body, reason string, t changeTarget) error {
	newVersion := t.current + 1
	if _, err := tx.Exec(ctx, "insert into artifact_versions(artifact_id,version,body,reason) values($1,$2,$3,$4)", t.artifact, newVersion, body, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "insert into artifact_files select $1,$2,object_hash,filename,media_type from invocation_files where run_id=$3", t.artifact, newVersion, run); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update artifacts set current_version=$2 where id=$1", t.artifact, newVersion); err != nil {
		return err
	}
	if err := advanceDescendants(ctx, tx, t.original, t.ordinal); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update change_batches set disposition='applied',applied_version=$2 where run_id=$1", run, newVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update feedback set state='verification',candidate_version=$2,revision=revision+1 where id=$1", t.feedback, newVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "insert into feedback_events(feedback_id,revision,actor,action,reason,artifact_version) values($1,$2,$3,'submit',$4,$5)", t.feedback, t.revision+1, actor, reason, newVersion); err != nil {
		return err
	}
	if err := Touch(ctx, tx, t.project, t.stage); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "change.apply", run, map[string]any{"base": t.base, "previous": t.current, "version": newVersion, "reason": reason})
}

func FinishChange(ctx context.Context, tx pgx.Tx, run, actor, body string) error {
	t, err := readTarget(ctx, tx, run, actor)
	if err != nil {
		return err
	}
	if t.disposition != "pending" {
		return nil
	}
	if !t.allowed {
		return web.Fail(403, "修改职责已变化")
	}
	if _, err = tx.Exec(ctx, "update runs set status='completed' where id=$1", run); err != nil {
		return err
	}
	if t.stage != "review" || t.state != "required" || t.current != t.base || t.revision != t.feedbackRevision || t.staleInputs {
		if _, err = tx.Exec(ctx, "update change_batches set disposition='conflict' where run_id=$1", run); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor, "change.conflict", run, map[string]int{"base": t.base, "current": t.current})
	}
	return applyCandidate(ctx, tx, run, actor, body, "Agent 按反馈提交修订稿", t)
}

func (s Server) decideChange(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Version, Expected          int
		Action, Reason, MergedBody string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) || (in.Action != "apply" && in.Action != "discard" && in.Action != "merge") {
		return web.Fail(422, "请选择处理方式并说明原因")
	}
	ctx := r.Context()
	run := r.PathValue("change")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		t, err := readTarget(ctx, tx, run, u.ID)
		if err != nil {
			return err
		}
		if t.project != p.ID {
			return web.Fail(404, "未找到本项目修改批次")
		}
		if !t.allowed {
			return web.Fail(403, "需要项目制作或审核职责")
		}
		if t.disposition != "conflict" || t.current != in.Version || t.revision != in.Expected {
			return web.Fail(409, "请比较当前成果和最新反馈后再决定")
		}
		if in.Action == "discard" {
			if _, err = tx.Exec(ctx, "update change_batches set disposition='discarded' where run_id=$1", run); err != nil {
				return err
			}
			return database.Audit(ctx, tx, u.ID, "change.discard", run, map[string]string{"reason": in.Reason})
		}
		var invalidInput bool
		if err = tx.QueryRow(ctx, `select exists(select from run_source_inputs where run_id=$1 and extracted is null and picture_hash is null)
            or exists(select from invocations i where i.run_id=$1 and not run_working_set_usable(i.run_id,i.ordinal,i.attempt_no))`, run).Scan(&invalidInput); err != nil {
			return err
		}
		if invalidInput {
			return web.Fail(409, "修订依据已失效，请保留现稿并重新核对资料")
		}
		if t.stage != "review" || t.state != "required" && t.state != "verification" {
			return web.Fail(409, "此反馈当前不能接收修订")
		}
		var body string
		var binary bool
		if err = tx.QueryRow(ctx, "select result,exists(select from invocation_files where run_id=$1) from invocations where run_id=$1 and status='completed'", run).Scan(&body, &binary); err != nil {
			return err
		}
		if in.Action == "merge" {
			if binary || web.Blank(in.MergedBody) {
				return web.Fail(422, "合并正文适用于文本成果，需要完整合并稿")
			}
			body = in.MergedBody
		}
		return applyCandidate(ctx, tx, run, u.ID, body, in.Reason, t)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

// TouchRun keeps modification runs from changing the lifecycle of their parent project.
func TouchRun(ctx context.Context, tx pgx.Tx, run, project, stage string) error {
	_, err := tx.Exec(ctx, `update projects set stage=case when exists(select from change_batches where run_id=$1) then stage else $3 end,revision=revision+1,updated_at=now() where id=$2`, run, project, stage)
	return err
}
