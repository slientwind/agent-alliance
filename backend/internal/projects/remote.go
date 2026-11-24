package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strconv"
)

func (s Server) recheckRemote(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Reason        string
		Expected      int
		InputRevision int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) {
		return web.Fail(422, "请说明继续核对的原因")
	}
	step, err := strconv.Atoi(r.PathValue("step"))
	if err != nil || step < 1 || step > 12 {
		return web.Fail(422, "请选择原任务的步骤")
	}
	ctx := r.Context()
	run := r.PathValue("id")
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = fmt.Sprintf("remote-check/%s/%d/%d", run, step, in.Expected)
		if in.InputRevision > 0 {
			key += fmt.Sprintf("/%d", in.InputRevision)
		}
	}
	if len(key) > 180 {
		return web.Fail(422, "幂等键过长")
	}
	signature := web.Hash(fmt.Sprintf("remote-check/%s/%d/%d/%s", run, step, in.Expected, in.Reason))
	if in.InputRevision > 0 {
		signature = web.Hash(fmt.Sprintf("%x/remote-question/%d", signature, in.InputRevision))
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var rev, question int
		var phase, state string
		var allowed, queryVerified, stepScoped bool
		err := tx.QueryRow(ctx, `select r.control_revision,j.phase,r.status,j.input_revision,j.detail='remote_step_input',not a.emergency and
 project_agent_access(r.project_id,a.id,n.agent_revision,r.actor) and exists(select from project_members pm where pm.project_id=p.id and pm.member_id=r.actor),
 coalesce((remote_operation_availability(agent_released_remote_operation_evidence(n.agent_id,n.agent_revision))->>'query')::boolean,true)
 from runs r join projects p on p.id=r.project_id join remote_jobs j on j.run_id=r.id
 join effective_run_nodes n on(n.run_id,n.ordinal)=(j.run_id,j.ordinal)
 join agents a on a.id=n.agent_id where r.id=$1 and j.ordinal=$2 and (p.owner=$3 or (r.actor=$3 and exists(select from change_batches where run_id=r.id) and exists(select from project_members m where m.project_id=p.id and m.member_id=$3 and m.can_review))) for update of r,j`, run, step, u.ID).Scan(&rev, &phase, &state, &question, &stepScoped, &allowed, &queryVerified)
		if err != nil {
			return err
		}
		var match bool
		err = tx.QueryRow(ctx, "select signature=$1 from controls where actor=$2 and request_key=$3", signature, u.ID, key).Scan(&match)
		if err == nil {
			if !match {
				return web.Fail(409, "幂等键内容冲突")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if rev != in.Expected || phase != "manual" || (stepScoped && question != in.InputRevision) {
			return web.Fail(409, "运行或远端核对状态已变化")
		}
		var pendingInput bool
		if err := tx.QueryRow(ctx, `select exists(select from remote_task_inputs where (run_id,ordinal)=($1,$2)
             and state in ('queued','submitted'))`, run, step).Scan(&pendingInput); err != nil {
			return err
		}
		if pendingInput {
			return web.Fail(409, "本轮补件仍在发送，请查看原任务状态")
		}
		if !allowed {
			return web.Fail(409, "当前执行人无权核对原远端任务，请先恢复项目及绑定修订的授权")
		}
		if !queryVerified {
			return web.Fail(409, "此发布修订的远端查询未通过验证或证据已撤回")
		}
		if state == "paused" || state == "blocked" {
			return web.Fail(409, "请先恢复运行条件")
		}
		_, err = tx.Exec(ctx, `update remote_jobs set phase='checking',detail='remote_pending',deadline=now()+check_window_seconds*interval '1 second',consecutive_errors=0,next_at=now() where run_id=$1 and ordinal=$2`, run, step)
		if err != nil {
			return err
		}
		if !stepScoped {
			rev++
			if _, err = tx.Exec(ctx, "update runs set control_revision=$2 where id=$1", run, rev); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into controls(run_id,actor,request_key,signature,action,reason,revision,sent_at) values($1,$2,$3,$4,'remote.check',$5,$6,now())`, run, u.ID, key, signature, in.Reason, rev); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "remote.check", run, map[string]any{"step": step, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(202)
	return nil
}
