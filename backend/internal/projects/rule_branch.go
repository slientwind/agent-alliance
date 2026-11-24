package projects

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

type RuleBranchStep struct {
	Step      int     `json:"step"`
	Name      string  `json:"name"`
	Mode      string  `json:"mode"`
	Artifact  *string `json:"artifact,omitempty"`
	Version   *int    `json:"version,omitempty"`
	OriginRun *string `json:"originRun,omitempty"`
	Attempt   *int    `json:"attempt,omitempty"`
}

type ruleBranchRule struct {
	Step        int    `json:"step"`
	Rule        string `json:"rule"`
	Previous    int    `json:"previous"`
	Replacement int    `json:"replacement"`
	Body        string `json:"body"`
}

type RuleBranchPreview struct {
	Run     string           `json:"run"`
	Control int              `json:"controlRevision"`
	Plan    int              `json:"planVersion"`
	Project int              `json:"projectRevision"`
	Team    string           `json:"chargeTeam"`
	Calls   int              `json:"calls"`
	Steps   []RuleBranchStep `json:"steps"`
	Rules   []ruleBranchRule `json:"rules"`
	Digest  string           `json:"digest"`
}

type RuleBranchInput struct {
	Digest         string `json:"digest"`
	AuthorizeCalls int    `json:"authorizeCalls"`
}

func (s Server) ruleBranchPreview(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	ctx := r.Context()
	var result RuleBranchPreview
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		result, err = PreviewRuleBranch(ctx, tx, r.PathValue("id"), member.ID)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) rebuildRuleBranch(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	var in RuleBranchInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	var next string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		next, err = RebuildRuleBranch(ctx, tx, r.PathValue("id"), member.ID, r.Header.Get("Idempotency-Key"), in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]string{"run": next})
}

type branchSource struct {
	project, state, team string
	control, plan        int
}

func lockBranchSource(ctx context.Context, tx pgx.Tx, run, actor string) (branchSource, error) {
	var source branchSource
	err := tx.QueryRow(ctx, `select r.project_id,r.status,r.control_revision,r.plan_version,t.name from runs r
        join projects p on p.id=r.project_id join project_members pm on (pm.project_id,pm.member_id)=(p.id,$2)
        join teams t on t.id=r.charge_team
        join members m on m.id=pm.member_id and m.enabled
        where r.id=$1 and r.actor=$2 and p.owner=$2
        and not exists(select from change_batches c where c.run_id=r.id) for update of r`, run, actor).
		Scan(&source.project, &source.state, &source.control, &source.plan, &source.team)
	if err != nil {
		return source, err
	}
	var permitted bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&permitted); err != nil {
		return source, err
	}
	if !permitted {
		return source, web.Fail(403, "原任务的服务授权已失效")
	}
	return source, nil
}

func PreviewRuleBranch(ctx context.Context, tx pgx.Tx, run, actor string) (RuleBranchPreview, error) {
	source, err := lockBranchSource(ctx, tx, run, actor)
	if err != nil {
		return RuleBranchPreview{}, err
	}
	return buildRuleBranchPreview(ctx, tx, run, actor, source)
}

func buildRuleBranchPreview(ctx context.Context, tx pgx.Tx, run, actor string, source branchSource) (RuleBranchPreview, error) {
	var preview RuleBranchPreview
	p, err := lock(ctx, tx, source.project, actor)
	if err != nil {
		return preview, err
	}
	if p.PlanVersion != source.plan || (p.Stage != "running" && p.Stage != "review") ||
		(source.state != "running" && source.state != "completed") {
		return preview, web.Fail(409, "请在原任务处理当前状态后再重做受影响分支")
	}
	var latest, quiescent bool
	err = tx.QueryRow(ctx, `select $1=(select id from runs r where project_id=$2
           and not exists(select from change_batches c where c.run_id=r.id) order by r.created_at desc,r.id desc limit 1),
        not exists(select from invocations i where i.run_id=$1 and i.status<>'completed')
        and not exists(select from change_batches c join artifacts a on a.id=c.artifact_id
          where a.project_id=$2 and c.disposition in ('pending','conflict'))
        and not exists(select from remote_task_inputs q where q.run_id=$1 and q.state in ('queued','submitted','uncertain'))`,
		run, p.ID).Scan(&latest, &quiescent)
	if err != nil {
		return preview, err
	}
	if !latest || !quiescent {
		return preview, web.Fail(409, "请先等待已发调用返回或完成回执核对，再确认本次重做")
	}
	rows, err := tx.Query(ctx, `with recursive roots(ordinal) as (
          select w.ordinal from step_rule_waits w where w.run_id=$1 and w.rebuild_required
          union
          select o.ordinal from run_observations o where o.run_id=$1 and exists(
            select from invalid_working_step_rules(o.origin_run,o.ordinal,o.attempt_no) invalid where invalid.scoped)
        ), affected(ordinal) as (
          select ordinal from roots
          union
          select e.ordinal from affected a join node_edges e on e.depends_on=a.ordinal
          where (e.project_id,e.plan_version)=($2,$3)
        )
        select n.ordinal,v.name,case when a.ordinal is not null then 'rebuild'
               when o.origin_run is not null or committed.id is not null then 'reuse' else 'continue' end,
               slot.artifact_id,artifact.current_version,o.origin_run,o.attempt_no,
               prior.result is not null or committed.id is not null,
               (o.origin_run is null or run_working_set_usable(o.origin_run,o.ordinal,o.attempt_no))
                 and (slot.artifact_id is null or distribution_readable(slot.artifact_id,artifact.current_version,$4))
        from effective_run_nodes n join agent_revisions v on(v.agent_id,v.revision)=(n.agent_id,n.agent_revision)
        left join affected a on a.ordinal=n.ordinal
        left join run_observations o on(o.run_id,o.ordinal)=(n.run_id,n.ordinal)
        left join invocations prior on (prior.run_id,prior.ordinal,prior.attempt_no)=(o.origin_run,o.ordinal,o.attempt_no)
        left join run_candidate_artifacts committed on (committed.run_id,committed.ordinal)=(n.run_id,n.ordinal)
        left join run_artifact_slots slot on (slot.run_id,slot.ordinal)=(n.run_id,n.ordinal)
        left join artifacts artifact on artifact.id=slot.artifact_id
        where n.run_id=$1 order by n.ordinal`, run, p.ID, source.plan, actor)
	if err != nil {
		return preview, err
	}
	defer rows.Close()
	var targets []int
	rebuild := false
	for rows.Next() {
		var step RuleBranchStep
		var finished, usable bool
		if err := rows.Scan(&step.Step, &step.Name, &step.Mode, &step.Artifact, &step.Version, &step.OriginRun, &step.Attempt, &finished, &usable); err != nil {
			return preview, err
		}
		if step.Mode == "reuse" && (!finished || !usable) {
			return preview, web.Fail(409, "请先完成独立分支或核对其执行依据，再确认本次重做")
		}
		if step.Mode == "rebuild" {
			rebuild = true
		}
		if step.Mode != "reuse" {
			targets = append(targets, step.Step)
		}
		preview.Steps = append(preview.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return preview, err
	}
	if !rebuild {
		return preview, web.Fail(409, "此运行没有因已使用的团队规则失效而需要重做的分支")
	}
	preview.Rules, err = branchRuleChoices(ctx, tx, run, actor, targets)
	if err != nil {
		return preview, err
	}
	preview.Run, preview.Control, preview.Plan, preview.Project, preview.Calls = run, source.control, source.plan, p.Revision, len(targets)
	preview.Team = source.team
	raw, err := json.Marshal(preview)
	if err != nil {
		return preview, err
	}
	preview.Digest = hex.EncodeToString(web.Hash(string(raw)))
	return preview, nil
}

func branchRuleChoices(ctx context.Context, tx pgx.Tx, run, actor string, targets []int) ([]ruleBranchRule, error) {
	// Serialize the confirmation with a rule publication or disable.
	if _, err := tx.Exec(ctx, `select r.id from team_rules r where exists(
        select from effective_run_rules p where p.run_id=$1 and p.rule_id=r.id and p.ordinal=any($2))
        order by r.id for share of r`, run, targets); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `select e.ordinal,e.rule_id,e.rule_revision,
          case when e.usable then pinned.revision else coalesce(v.revision,0) end,
          case when e.usable then pinned.body else coalesce(v.body,'') end
        from effective_run_rules e join team_rules tr on tr.id=e.rule_id
        join rule_versions pinned on (pinned.rule_id,pinned.revision)=(e.rule_id,e.rule_revision)
        join effective_run_nodes n on(n.run_id,n.ordinal)=(e.run_id,e.ordinal)
        join projects p on p.id=n.project_id
        join nodes primary_node on (primary_node.project_id,primary_node.plan_version,primary_node.ordinal)
            =(n.project_id,n.plan_version,n.ordinal)
        left join rule_versions v on(v.rule_id,v.revision)=(tr.id,tr.published_revision)
          and e.scoped and tr.team_id=p.team_id and rule_usable(tr.id,v.revision,$2)
          and (n.agent_id=any(v.agent_scope) or primary_node.agent_id=any(v.agent_scope))
        where e.run_id=$1 and e.ordinal=any($3) and (e.scoped or not e.usable) order by e.ordinal,e.rule_id`, run, actor, targets)
	if err != nil {
		return nil, err
	}
	rules, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ruleBranchRule])
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if rule.Replacement == 0 {
			return nil, web.Fail(409, fmt.Sprintf("步骤 %d 缺少适用的有效规则，请先联系规则维护人", rule.Step))
		}
	}
	return rules, nil
}

func RebuildRuleBranch(ctx context.Context, tx pgx.Tx, run, actor, key string, in RuleBranchInput) (string, error) {
	if len(in.Digest) != 64 || in.AuthorizeCalls < 1 || key == "" || len(key) > 180 {
		return "", web.Fail(422, "请核对本次重做清单并授权其调用次数")
	}
	source, err := lockBranchSource(ctx, tx, run, actor)
	if err != nil {
		return "", err
	}
	requestKey := "rule-branch/" + key
	sign := web.Hash(fmt.Sprintf("rule-branch/%s/%s/%d", run, in.Digest, in.AuthorizeCalls))
	var next string
	var same bool
	err = tx.QueryRow(ctx, `select id,request_hash=$3 from runs where (actor,request_key)=($1,$2)`, actor, requestKey, sign).Scan(&next, &same)
	if err == nil {
		if !same {
			return "", web.Fail(409, "请求编号已用于不同的重做清单")
		}
		return next, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	preview, err := buildRuleBranchPreview(ctx, tx, run, actor, source)
	if err != nil {
		return "", err
	}
	if preview.Digest != in.Digest || preview.Calls != in.AuthorizeCalls {
		return "", web.Fail(409, "规则、成果或待重做步骤已变化，请重新核对")
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,
        app_id,delegated_by,service_id,service_version,await_lookup_receipt,await_calculation_receipt,
        step_remote_input,step_source_receipt)
        select project_id,plan_version,actor,charge_team,$2,$3,max_calls,
        app_id,delegated_by,service_id,service_version,await_lookup_receipt,await_calculation_receipt,
        step_remote_input,step_source_receipt
        from runs where id=$1 returning id`, run, requestKey, sign).Scan(&next); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "insert into rule_branch_rebuilds(run_id,source_run,authorized_calls,preview) values($1,$2,$3,$4::jsonb)",
		next, run, preview.Calls, encoded); err != nil {
		return "", err
	}
	for _, step := range preview.Steps {
		if _, err := tx.Exec(ctx, `insert into rule_branch_steps(run_id,ordinal,mode,origin_run,origin_attempt,artifact_id,base_version,result_version,finished)
            values($1,$2,$3,$4,$5,$6,$7,case when $3='reuse' then $7::integer end,$3='reuse')`,
			next, step.Step, step.Mode, step.OriginRun, step.Attempt, step.Artifact, step.Version); err != nil {
			return "", err
		}
	}
	for _, query := range []string{
		"insert into step_rule_replacements select $2,ordinal,rule_id,rule_revision from step_rule_replacements where run_id=$1",
		"insert into service_supplements select $2,ordinal,revision,message_seq from service_supplements where run_id=$1",
		`insert into run_tool_choices select $2,c.ordinal,c.choice,c.manual_choice,c.manual_by from run_tool_choices c
         join runs r on r.id=c.run_id
         join plan_tool_choices plan on (plan.project_id,plan.plan_version,plan.ordinal)=(r.project_id,r.plan_version,c.ordinal)
         join rule_branch_steps step on (step.run_id,step.ordinal)=($2,plan.selector)
         where c.run_id=$1 and step.mode='reuse'`,
	} {
		if _, err := tx.Exec(ctx, query, run, next); err != nil {
			return "", err
		}
	}
	for _, rule := range preview.Rules {
		if _, err := tx.Exec(ctx, `insert into step_rule_replacements(run_id,ordinal,rule_id,rule_revision)
          values($1,$2,$3,$4) on conflict(run_id,ordinal,rule_id) do update set rule_revision=excluded.rule_revision`,
			next, rule.Step, rule.Rule, rule.Replacement); err != nil {
			return "", err
		}
	}
	// Replace scheduling ownership without deleting old artifacts, receipts,
	// versions or ledger entries. A final control signal closes parked workers.
	if source.state == "running" {
		if _, err := tx.Exec(ctx, "update runs set status='cancelled',control_revision=control_revision+1 where id=$1", run); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `insert into controls(run_id,actor,request_key,signature,action,reason,revision)
            values($1,$2,$3,$4,'rule.branch.redirect','启动原任务的分支重做批次',$5)`,
			run, actor, "redirect/"+next, sign, source.control+1); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(ctx, "insert into outbox(run_id) values($1)", next); err != nil {
		return "", err
	}
	if err := TouchRun(ctx, tx, next, source.project, "queued"); err != nil {
		return "", err
	}
	return next, database.Audit(ctx, tx, actor, "rule.branch.rebuild", next, map[string]any{
		"originalRun": run, "authorizedCalls": preview.Calls, "digest": preview.Digest,
	})
}
