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

const StepDispatchWaitsSQL = `coalesce((select jsonb_agg(jsonb_build_object(
 'step',w.ordinal,'waitRevision',w.wait_revision,'reason',w.reason,'detail',w.note,
 'name',a.name,'revision',w.agent_revision) order by w.ordinal)
 from current_step_dispatch_waits w join agent_revisions a
 on (a.agent_id,a.revision)=(w.agent_id,w.agent_revision)
 where w.run_id=r.id),'[]'::jsonb)`

type DispatchRecheckInput struct {
	Step, Expected, WaitRevision int
}

func (s Server) recheckStepDispatch(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in DispatchRecheckInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		return RecheckStepDispatch(ctx, tx, r.PathValue("id"), u.ID, r.Header.Get("Idempotency-Key"), in)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusAccepted, map[string]int{"step": in.Step, "controlRevision": in.Expected})
}

func RecheckStepDispatch(ctx context.Context, tx pgx.Tx, run, actor, key string, in DispatchRecheckInput) error {
	if key == "" || len(key) > 180 || in.Step < 1 || in.Step > 12 || in.Expected < 1 || in.WaitRevision < 1 {
		return web.Fail(422, "请指定当前步骤、待办和请求编号")
	}
	var project, state string
	var control int
	err := tx.QueryRow(ctx, `select p.id,r.status,r.control_revision
        from runs r join projects p on(p.id,p.plan_version)=(r.project_id,r.plan_version)
        join project_members pm on(pm.project_id,pm.member_id)=(p.id,$2)
        join members m on m.id=pm.member_id and m.enabled and m.kind='person'
        where r.id=$1 and (p.owner=$2 or (r.app_id is not null and r.delegated_by is null and p.final_reviewer=$2 and pm.can_review))
        for update of r for share of pm,m`, run, actor).Scan(&project, &state, &control)
	if err != nil {
		return err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return web.Fail(403, "此任务的服务授权已失效")
	}
	sign := web.Hash(fmt.Sprintf("dispatch.step.recheck/%s/%d/%d/%d", run, in.Step, in.WaitRevision, in.Expected))
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
	if state != "running" || control != in.Expected {
		return web.Fail(409, "运行状态已变化，请重新核对")
	}
	tag, err := tx.Exec(ctx, `delete from step_dispatch_waits w using current_step_dispatch_waits active
        where (w.run_id,w.ordinal,w.wait_revision)=(active.run_id,active.ordinal,active.wait_revision)
          and (active.run_id,active.ordinal,active.wait_revision)=($1,$2,$3)`, run, in.Step, in.WaitRevision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return web.Fail(409, "步骤待办已变化，请重新核对")
	}
	tag, err = tx.Exec(ctx, `insert into controls(run_id,actor,request_key,signature,action,reason,revision,wake_ordinal)
        values($1,$2,$3,$4,'dispatch.step.recheck','重新核对步骤执行条件',$5,$6) on conflict(actor,request_key) do nothing`,
		run, actor, key, sign, control, in.Step)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return web.Fail(409, "此请求编号已用于其他操作")
	}
	if err = TouchRun(ctx, tx, run, project, state); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "select record_service_event($1,'step','dispatch_rechecking',$2)", run, in.Step); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "dispatch.step.recheck", run, map[string]any{
		"step": in.Step, "waitRevision": in.WaitRevision, "controlRevision": control,
	})
}
