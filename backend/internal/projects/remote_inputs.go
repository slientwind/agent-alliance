package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RemoteInputRequest struct {
	Expected, InputRevision int
	Input                   string
	AuthorizeOneCall        bool
}

type RemoteInputReceipt struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	ControlRevision int    `json:"controlRevision"`
}

type RemoteInputTarget struct {
	Run, Destination             string
	Step, Control, InputRevision int
}

const remoteInputTarget = ` from runs r join projects p on p.id=r.project_id
  join project_members pm on(pm.project_id,pm.member_id)=(p.id,$1)
  join members actor on actor.id=pm.member_id and actor.enabled and actor.kind='person'
  join remote_jobs j on j.run_id=r.id join invocations i using(run_id,ordinal)
  join effective_run_nodes n on(n.run_id,n.ordinal)=(j.run_id,j.ordinal)
  join agents a on a.id=n.agent_id
  join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision)
  where (p.owner=$1 or (r.actor=$1 and pm.can_review and exists(select from change_batches where run_id=r.id)))`

const remoteInputAuthorized = `not j.cancel_attempted and not a.emergency and
  run_working_set_usable(r.id,j.ordinal,i.attempt_no) and
  project_agent_access(r.project_id,n.agent_id,n.agent_revision,r.actor) and
  remote_operation_live(agent_released_remote_operation_evidence(n.agent_id,n.agent_revision),'supplement')`

func AvailableRemoteInputs(ctx context.Context, tx pgx.Tx, actor, project string) ([]RemoteInputTarget, error) {
	rows, err := tx.Query(ctx, `select r.id,j.ordinal,r.control_revision,j.input_revision,ar.name`+
		remoteInputTarget+` and p.id=$2 and remote_input_window_open(r.status,j.phase,j.detail)
		and i.remote_status='input-required' and i.remote_task<>'' and i.remote_context<>'' and j.input_revision>0
		and `+remoteInputAuthorized+`
		and not exists(select from remote_task_inputs q where (q.run_id,q.ordinal,q.input_revision)=(r.id,j.ordinal,j.input_revision)
		  and q.state<>'rejected')
		order by r.created_at,j.ordinal,r.id`, actor, project)
	if err != nil {
		return nil, err
	}
	targets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RemoteInputTarget, error) {
		var target RemoteInputTarget
		err := row.Scan(&target.Run, &target.Step, &target.Control, &target.InputRevision, &target.Destination)
		return target, err
	})
	if err != nil {
		return nil, err
	}
	permitted := targets[:0]
	for _, target := range targets {
		var allowed bool
		if err := tx.QueryRow(ctx, identity.RunGrantSQL, target.Run).Scan(&allowed); err != nil {
			return nil, err
		}
		if allowed {
			permitted = append(permitted, target)
		}
	}
	return permitted, nil
}

func (s Server) queueRemoteInput(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in RemoteInputRequest
	if err := web.Read(r, &in); err != nil {
		return err
	}
	step, err := strconv.Atoi(r.PathValue("step"))
	if err != nil {
		return web.Fail(422, "请选择原任务的步骤")
	}
	var result RemoteInputReceipt
	ctx := r.Context()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		result, err = QueueRemoteInput(ctx, tx, s.Vault, u.ID, r.PathValue("id"), step, r.Header.Get("Idempotency-Key"), "", in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, result)
}

// QueueRemoteInput consumes the same scoped question across web and enterprise IM.
// The caller owns the transaction and has authenticated the actor and optional binding.
func QueueRemoteInput(ctx context.Context, tx pgx.Tx, vault resources.Vault, actor, run string, step int,
	key, binding string, in RemoteInputRequest) (RemoteInputReceipt, error) {
	var result RemoteInputReceipt
	if step < 1 || step > 12 || key == "" || len(key) > 180 || in.InputRevision < 1 || !in.AuthorizeOneCall {
		return result, web.Fail(422, "请核对原任务并授权一次未知费用的补件调用")
	}
	input, err := resources.RemoteInput(in.Input)
	if err != nil {
		return result, err
	}
	signature := web.Hash(fmt.Sprintf("remote-input/%s/%d/%d/%d/%s", run, step, in.InputRevision, in.Expected, input))
	if binding != "" {
		signature = web.Hash(fmt.Sprintf("%x/feishu/%s", signature, binding))
	}

	var project, remoteState, task, correlation string
	var current, requestRevision, attempt int
	var allowed, open, stepScoped bool
	if err := tx.QueryRow(ctx, `select p.id,r.control_revision,j.input_revision,i.attempt_no,i.remote_status,
		remote_input_window_open(r.status,j.phase,j.detail),j.detail='remote_step_input',
		coalesce(i.remote_task,''),coalesce(i.remote_context,''),`+remoteInputAuthorized+remoteInputTarget+
		` and r.id=$2 and j.ordinal=$3 for update of r,j`, actor, run, step).
		Scan(&project, &current, &requestRevision, &attempt, &remoteState, &open, &stepScoped, &task, &correlation, &allowed); err != nil {
		return result, err
	}
	var same bool
	err = tx.QueryRow(ctx, `select signature=$3,id,state,control_revision from remote_task_inputs where (actor,request_key)=($1,$2)`,
		actor, key, signature).Scan(&same, &result.ID, &result.Status, &result.ControlRevision)
	if err == nil {
		if !same {
			return RemoteInputReceipt{}, web.Fail(409, "补件请求已用于其他内容")
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if current != in.Expected || requestRevision != in.InputRevision || !open ||
		remoteState != "input-required" || task == "" || correlation == "" {
		return result, web.Fail(409, "远端已离开本轮补件状态，请刷新并核对原任务")
	}
	var permitted bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&permitted); err != nil {
		return result, err
	}
	if !allowed || !permitted {
		return result, web.Fail(409, "当前授权或发布修订的远端补件证据已失效")
	}
	revision := current
	if !stepScoped {
		revision++
	}
	result = RemoteInputReceipt{ID: uuid.NewString(), Status: "queued", ControlRevision: revision}
	body, err := vault.Seal(input, "remote-input/"+result.ID)
	if err != nil {
		return result, err
	}
	tag, err := tx.Exec(ctx, `insert into remote_task_inputs(id,run_id,ordinal,input_revision,attempt_no,control_revision,
		actor,task_id,context_id,request_key,signature,encrypted_body,channel_binding_id)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,nullif($13,'')::uuid) on conflict do nothing`,
		result.ID, run, step, in.InputRevision, attempt, result.ControlRevision, actor, task, correlation, key, signature, body, binding)
	if err != nil {
		return result, err
	}
	if tag.RowsAffected() == 0 {
		return result, web.Fail(409, "本轮补件已提交，请核对原任务")
	}
	if !stepScoped {
		if _, err := tx.Exec(ctx, "update runs set control_revision=$2 where id=$1", run, revision); err != nil {
			return result, err
		}
	}
	if _, err := tx.Exec(ctx, "update projects set revision=revision+1,updated_at=now() where id=$1", project); err != nil {
		return result, err
	}
	source := "web"
	if binding != "" {
		source = "feishu"
	}
	err = database.Audit(ctx, tx, actor, "remote.input.queue", run, map[string]any{
		"id": result.ID, "step": step, "inputRevision": in.InputRevision, "source": source,
	})
	return result, err
}
