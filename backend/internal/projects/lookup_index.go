package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

const StepIndexWaitsSQL = `coalesce((select jsonb_agg(jsonb_build_object('step',w.ordinal,
 'until',w.deadline,'automatic',w.deadline>clock_timestamp(),'waitRevision',w.step_wait_revision) order by w.ordinal)
 from lookup_index_waits w where w.run_id=r.id and w.plan_version=r.plan_version
 and w.active and w.step_wait_revision is not null and r.status='running'),'[]'::jsonb)`

type IndexRecheckInput struct {
	Step, Expected, WaitRevision int
}

func (s Server) recheckSourceIndex(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in IndexRecheckInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		return RecheckSourceIndex(ctx, tx, r.PathValue("id"), u.ID, r.Header.Get("Idempotency-Key"), in)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]int{"step": in.Step, "controlRevision": in.Expected})
}

// QueueStepIndexProbe releases the exact wait and its optional supplement in
// the same transaction as the durable wake. Its callers hold the run lock.
func QueueStepIndexProbe(ctx context.Context, tx pgx.Tx, run string, step, revision, plan int) (bool, error) {
	tag, err := tx.Exec(ctx, `update lookup_index_waits
        set active=false,wake_pending=true,next_check_at=clock_timestamp()
        where (run_id,ordinal,step_wait_revision,plan_version)=($1,$2,$3,$4) and active`, run, step, revision, plan)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	_, err = tx.Exec(ctx, `delete from calculation_requests
        where (run_id,ordinal,kind)=($1,$2,'lookup') and request_revision is not null`, run, step)
	return err == nil, err
}

func RecheckSourceIndex(ctx context.Context, tx pgx.Tx, run, actor, key string, in IndexRecheckInput) error {
	if key == "" || len(key) > 180 || in.Step < 1 || in.WaitRevision < 1 {
		return web.Fail(422, "请指定等待索引的步骤、当前待办和请求编号")
	}
	var project, state string
	var plan, control int
	if err := tx.QueryRow(ctx, `select r.project_id,r.status,r.plan_version,r.control_revision from runs r
        join projects p on p.id=r.project_id and p.plan_version=r.plan_version
        join project_members pm on (pm.project_id,pm.member_id)=(p.id,r.actor)
        join members u on u.id=r.actor and u.enabled
        where r.id=$1 and p.owner=$2 and r.actor=$2 for update of r`, run, actor).
		Scan(&project, &state, &plan, &control); err != nil {
		return err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return web.Fail(403, "此任务的服务授权已失效")
	}
	sign := web.Hash(fmt.Sprintf("recheck-source-index/%s/%d/%d/%d", run, in.Step, in.WaitRevision, in.Expected))
	var same bool
	err := tx.QueryRow(ctx, "select signature=$1 from controls where actor=$2 and request_key=$3", sign, actor, key).Scan(&same)
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
	queued, err := QueueStepIndexProbe(ctx, tx, run, in.Step, in.WaitRevision, plan)
	if err != nil {
		return err
	}
	if !queued {
		return web.Fail(409, "等待索引的步骤已变化，请重新核对")
	}
	_, err = tx.Exec(ctx, `insert into controls(run_id,actor,request_key,signature,action,reason,revision,sent_at)
        values($1,$2,$3,$4,'lookup.index.recheck','重新核对原资料索引',$5,now())`, run, actor, key, sign, control)
	if err != nil {
		return err
	}
	if err := TouchRun(ctx, tx, run, project, "running"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "select record_service_event($1,'step','lookup_index_rechecking',$2)", run, in.Step); err != nil {
		return err
	}
	return database.Audit(ctx, tx, actor, "lookup.index.recheck", run, map[string]any{
		"step": in.Step, "waitRevision": in.WaitRevision, "controlRevision": control,
	})
}
