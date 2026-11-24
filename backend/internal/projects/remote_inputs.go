package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s Server) queueRemoteInput(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected, InputRevision int
		Input                   string
		AuthorizeOneCall        bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	step, err := strconv.Atoi(r.PathValue("step"))
	if err != nil || step < 1 || step > 12 || key == "" || len(key) > 180 || in.InputRevision < 1 || !in.AuthorizeOneCall {
		return web.Fail(422, "请核对原任务并授权一次未知费用的补件调用")
	}
	input, err := resources.RemoteInput(in.Input)
	if err != nil {
		return err
	}
	ctx, run := r.Context(), r.PathValue("id")
	signature := web.Hash(fmt.Sprintf("remote-input/%s/%d/%d/%d/%s", run, step, in.InputRevision, in.Expected, input))
	var id, status string
	var resultingRevision int
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var project, stage, runState, phase, remoteState, task, correlation string
		var current, requestRevision, attempt int
		var allowed bool
		if err := tx.QueryRow(ctx, `select p.id,p.stage,r.status,r.control_revision,j.phase,j.input_revision,i.attempt_no,i.remote_status,
             coalesce(i.remote_task,''),coalesce(i.remote_context,''),not j.cancel_attempted and not a.emergency and
             project_agent_access(r.project_id,n.agent_id,n.agent_revision,r.actor) and
             remote_operation_live(agent_released_remote_operation_evidence(n.agent_id,n.agent_revision),'supplement')
          from runs r join projects p on p.id=r.project_id join project_members pm on(pm.project_id,pm.member_id)=(p.id,$3)
          join remote_jobs j on j.run_id=r.id join invocations i using(run_id,ordinal)
          join effective_run_nodes n on(n.run_id,n.ordinal)=(j.run_id,j.ordinal)
          join agents a on a.id=n.agent_id where r.id=$1 and j.ordinal=$2 and
           (p.owner=$3 or (r.actor=$3 and pm.can_review and exists(select from change_batches where run_id=r.id)))
          for update of r,j`, run, step, u.ID).Scan(&project, &stage, &runState, &current, &phase, &requestRevision,
			&attempt, &remoteState, &task, &correlation, &allowed); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `select signature=$3,id,state,control_revision from remote_task_inputs where (actor,request_key)=($1,$2)`,
			u.ID, key, signature).Scan(&same, &id, &status, &resultingRevision)
		if err == nil {
			if !same {
				return web.Fail(409, "补件请求已用于其他内容")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if current != in.Expected || requestRevision != in.InputRevision || runState != "reconcile" || phase != "manual" ||
			remoteState != "input-required" || task == "" || correlation == "" {
			return web.Fail(409, "远端已离开本轮补件状态，请刷新并核对原任务")
		}
		var permitted bool
		if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&permitted); err != nil {
			return err
		}
		if !allowed || !permitted {
			return web.Fail(409, "当前授权或发布修订的远端补件证据已失效")
		}
		id, status, resultingRevision = uuid.NewString(), "queued", current+1
		body, err := s.Vault.Seal(input, "remote-input/"+id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into remote_task_inputs(id,run_id,ordinal,input_revision,attempt_no,control_revision,
            actor,task_id,context_id,request_key,signature,encrypted_body) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			id, run, step, in.InputRevision, attempt, resultingRevision, u.ID, task, correlation, key, signature, body); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update runs set control_revision=$2 where id=$1", run, resultingRevision); err != nil {
			return err
		}
		if err := Touch(ctx, tx, project, stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "remote.input.queue", run, map[string]any{"id": id, "step": step, "inputRevision": in.InputRevision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]any{"id": id, "status": status, "controlRevision": resultingRevision})
}
