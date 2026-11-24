package projects

import (
	"context"
	"fmt"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

func StepRuleWaitsSQL(viewer string) string {
	return `coalesce((select jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
 'step',w.ordinal,'rule',w.rule_id,'ruleRevision',w.rule_revision,'waitRevision',w.wait_revision,
 'rebuildRequired',w.rebuild_required,'availableRevision',c.available_revision,
 'availableBody',case when r.actor=` + viewer + ` then v.body end)) order by w.ordinal)
 from step_rule_waits w left join step_rule_recovery_choices c using(run_id,ordinal)
 left join rule_versions v on(v.rule_id,v.revision)=(c.rule_id,c.available_revision)
 where w.run_id=r.id and r.status='running'),'[]'::jsonb)`
}

type RuleRecheckInput struct {
	Step, Expected, WaitRevision, RuleRevision int
}

func (s Server) recheckStepRule(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in RuleRecheckInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		return RecheckStepRule(ctx, tx, r.PathValue("id"), u.ID, r.Header.Get("Idempotency-Key"), in)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]int{"step": in.Step, "controlRevision": in.Expected})
}

// A waiting, untouched step can adopt an explicitly selected version of the
// same rule. A paid invocation and its dependent drafts are never replayed.
func RecheckStepRule(ctx context.Context, tx pgx.Tx, run, actor, key string, in RuleRecheckInput) error {
	if in.Step < 1 || in.WaitRevision < 1 || in.RuleRevision < 1 || key == "" || len(key) > 180 {
		return web.Fail(422, "请指定待处理步骤、规则版本和请求编号")
	}
	var project, state string
	var control int
	err := tx.QueryRow(ctx, `select r.project_id,r.status,r.control_revision from runs r join projects p on p.id=r.project_id
      join project_members m on (m.project_id,m.member_id)=(p.id,r.actor)
      join members u on u.id=r.actor and u.enabled
      where r.id=$1 and r.actor=$2 and p.owner=$2 for update of r`, run, actor).Scan(&project, &state, &control)
	if err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return web.Fail(403, "此任务的服务授权已失效")
	}
	sign := web.Hash(fmt.Sprintf("step-rule/%s/%d/%d/%d/%d", run, in.Step, in.Expected, in.WaitRevision, in.RuleRevision))
	var same bool
	err = tx.QueryRow(ctx, "select signature=$1 from controls where actor=$2 and request_key=$3", sign, actor, key).Scan(&same)
	if err == nil {
		if !same {
			return web.Fail(409, "此请求编号已用于其他操作")
		}
		return nil
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if state != "running" || in.Expected != control {
		return web.Fail(409, "运行状态已变化，请重新核对")
	}
	var rule string
	var pinned int
	err = tx.QueryRow(ctx, `select tr.id,w.rule_revision from step_rule_waits w join team_rules tr on tr.id=w.rule_id
        where (w.run_id,w.ordinal,w.wait_revision)=($1,$2,$3) for share of tr`, run, in.Step, in.WaitRevision).Scan(&rule, &pinned)
	if err == pgx.ErrNoRows {
		return web.Fail(409, "规则待办已更新，请重新核对")
	}
	if err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `select exists(select from step_rule_recovery_choices
        where (run_id,ordinal,wait_revision,available_revision)=($1,$2,$3,$4))`, run, in.Step, in.WaitRevision, in.RuleRevision).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return web.Fail(409, "此规则版本当前不可用，或原步骤已执行，需要核对后重做")
	}
	if in.RuleRevision != pinned {
		if _, err := tx.Exec(ctx, `insert into step_rule_replacements(run_id,ordinal,rule_id,rule_revision)
          values($1,$2,$3,$4) on conflict(run_id,ordinal,rule_id) do update set rule_revision=excluded.rule_revision`,
			run, in.Step, rule, in.RuleRevision); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, "delete from step_rule_waits where run_id=$1 and ordinal=$2", run, in.Step); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into controls(run_id,actor,request_key,signature,action,reason,revision,wake_ordinal)
        values($1,$2,$3,$4,'rule.step.recheck','确认团队规则后继续步骤',$5,$6)`, run, actor, key, sign, control, in.Step); err != nil {
		return err
	}
	if err := TouchRun(ctx, tx, run, project, state); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "select record_service_event($1,'step','step_rule_confirmed',$2)", run, in.Step); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "rule.step.recheck", run, map[string]any{
		"step": in.Step, "rule": rule, "previous": pinned, "revision": in.RuleRevision, "waitRevision": in.WaitRevision,
	})
}
