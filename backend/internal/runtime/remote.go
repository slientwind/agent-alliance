package runtime

import (
	"alliance/internal/identity"
	"context"
	"errors"
	"log/slog"

	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/memory"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

const stepRemoteInput = "remote_step_input"

// EnableStepRemoteInput is recorded before dispatch by the new scheduler.
// Older histories retain the original run-level remote input behavior.
func (a Activities) EnableStepRemoteInput(ctx context.Context, run string) error {
	_, err := a.Catalog.DB.Exec(ctx, `update runs set step_remote_input=true where id=$1 and not step_remote_input`, run)
	return err
}

func stepRemoteInputAllowed(ctx context.Context, tx pgx.Tx, step Step, receipt *resources.RemoteWait) (bool, error) {
	if receipt.State != "input-required" || receipt.Task == "" || receipt.Context == "" {
		return false, nil
	}
	var allowed bool
	err := tx.QueryRow(ctx, `select r.step_remote_input and remote_operation_live(p.proof,'supplement') and remote_operation_ready(p.proof)
    from runs r join effective_run_nodes n on n.run_id=r.id
    cross join lateral (select agent_released_remote_operation_evidence(n.agent_id,n.agent_revision) proof) p
    where r.id=$1 and n.ordinal=$2`, step.Run, step.Ordinal).Scan(&allowed)
	return allowed, err
}

type remoteJob struct {
	Step
	project, actor, agent, state, lease string
	deadline                            time.Time
	revision                            int
	attempt                             int
	cancel                              bool
	receipt                             resources.RemoteWait
	model                               resources.Model
	inputID                             string
}

func remoteDisposition(receipt *resources.RemoteWait) (phase, detail string) {
	if receipt.NoText {
		return "finished", "remote_no_text"
	}
	switch state := receipt.State; state {
	case "working", "submitted":
		return "checking", "remote_pending"
	case "auth-required":
		return "manual", "remote_auth"
	case "input-required":
		return "manual", "remote_input"
	case "failed", "canceled", "rejected":
		return "finished", "remote_" + state
	case "completed":
		return "finished", "remote_completed"
	default:
		return "manual", "remote_unknown"
	}
}
func remoteNote(code string) string {
	switch code {
	case "remote_auth":
		return "远端需要授权，完成授权后可核对原任务。"
	case "remote_input":
		return "远端需要补充资料，在原任务补齐后可核对回执。"
	case "remote_input_unconfirmed":
		return "补件响应待核对，请查原任务回执。"
	case "remote_question_unchanged":
		return "远端仍在等待同一个问题的答复，请核对原任务回执。"
	case "remote_no_text":
		return "远端已完成，此操作没有可读取的文本成果。"
	case "remote_failed", "remote_canceled", "remote_rejected":
		return "远端任务已经结束，未取得可交付成果。"
	case "remote_deadline":
		return "已到达外部任务核对时限，请决定是否继续查询原任务。"
	case "remote_denied":
		return "执行身份或资源授权已变化，恢复授权后可核对原任务。"
	case "remote_query_unverified":
		return "此发布修订没有有效的远端查询证据。请由连接负责人核对原任务。"
	case "remote_cancel_unverified":
		return "平台已停止后续执行。此发布修订没有可用于确认远端取消的证据，请由连接负责人核对原任务。"
	case "remote_text_unverified":
		return "已收到远端完成回执，此结果路径尚无有效验收证据，请由连接负责人核对。"
	default:
		return "未取得可验证的远端结果，可以核对原任务回执。"
	}
}
func beginRemote(ctx context.Context, tx pgx.Tx, step Step, project, state string, r *resources.RemoteWait, model resources.Model) error {
	phase, detail := remoteDisposition(r)
	terminal := phase == "finished"
	isolated, err := stepRemoteInputAllowed(ctx, tx, step, r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `update invocations set status=case when $6 then 'remote_closed' else 'remote' end,remote_task=$1,remote_context=$2,remote_status=$3,ended_at=case when $6 then now() end where run_id=$4 and ordinal=$5`, r.Task, r.Context, r.State, step.Run, step.Ordinal, terminal)
	if err != nil {
		return err
	}
	if !terminal {
		switch {
		case state == "cancelled" && (model.RemoteOperations.CanFollow(true) || model.RemoteOperations.CanFollow(false)):
			phase = "checking"
		case state == "cancelled":
			phase, detail = "manual", "remote_cancel_unverified"
		case isolated:
			detail = stepRemoteInput
		case !model.RemoteOperations.CanFollow(false):
			phase, detail = "manual", "remote_query_unverified"
		}
	}
	_, err = tx.Exec(ctx, `insert into remote_jobs(run_id,ordinal,phase,detail,check_window_seconds,deadline,input_revision,input_prompt,input_message)
       values($1,$2,$3,$4,$5::int,now()+$5::int*interval '1 second',($6='input-required')::int,$7,$8)`,
		step.Run, step.Ordinal, phase, detail, model.Policy.WindowSeconds, r.State, r.Question, r.QuestionMessage)
	if err != nil {
		return err
	}
	if phase != "checking" && detail != stepRemoteInput && state == "running" {
		return setState(ctx, tx, step, project, "reconcile", detail, remoteNote(detail))
	}
	return nil
}

func closeUnverifiedRemoteOutput(ctx context.Context, tx pgx.Tx, step Step, project, state string, receipt *resources.RemoteWait) error {
	var task, correlation, remoteState any
	if receipt != nil {
		task, correlation, remoteState = receipt.Task, receipt.Context, receipt.State
	}
	if _, err := tx.Exec(ctx, `update invocations set status='remote_closed',diagnostic='remote_text_unverified',ended_at=now(),
      remote_task=coalesce($3,remote_task),remote_context=coalesce($4,remote_context),remote_status=coalesce($5,remote_status)
      where run_id=$1 and ordinal=$2`, step.Run, step.Ordinal, task, correlation, remoteState); err != nil {
		return err
	}
	if state == "running" || state == "reconcile" {
		return setState(ctx, tx, step, project, "reconcile", "remote_text_unverified", remoteNote("remote_text_unverified"))
	}
	return nil
}

func (a Activities) FollowRemote(ctx context.Context, c client.Client) {
	for ctx.Err() == nil {
		input, sendErr := a.sendRemoteInput(ctx)
		if sendErr != nil && ctx.Err() == nil {
			slog.Warn("external input dispatch pending")
		}
		did, err := a.checkRemote(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("external receipt check pending")
		}
		if err := a.wakeRemote(ctx, c); err != nil && ctx.Err() == nil {
			slog.Warn("external receipt signal pending")
		}
		if (!did && !input) || err != nil || sendErr != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}

const dueRun = `select r.id,r.project_id,r.actor,r.status from runs r where r.status in ('running','reconcile','cancelled') and exists(
 select from remote_jobs j where j.run_id=r.id and j.phase='checking' and j.next_at<=now() and(j.lease_until is null or j.lease_until<now()))
 order by (select min(next_at) from remote_jobs j where j.run_id=r.id and j.phase='checking') limit 1 for update of r skip locked`

func (a Activities) checkRemote(ctx context.Context) (bool, error) {
	var job remoteJob
	var ready bool
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, dueRun).Scan(&job.Run, &job.project, &job.actor, &job.state); err != nil {
			return err
		}
		var expired, cancelSent bool
		err := tx.QueryRow(ctx, `select j.ordinal,i.remote_task,coalesce(i.remote_context,''),n.agent_id,n.agent_revision,j.deadline,j.deadline<=now(),j.cancel_attempted,i.attempt_no from remote_jobs j join invocations i using(run_id,ordinal) join effective_run_nodes n on(n.run_id,n.ordinal)=(j.run_id,j.ordinal)
  where j.run_id=$1 and j.phase='checking' and j.next_at<=now() and(j.lease_until is null or j.lease_until<now()) order by j.next_at,j.ordinal limit 1 for update of j`, job.Run).Scan(&job.Ordinal, &job.receipt.Task, &job.receipt.Context, &job.agent, &job.revision, &job.deadline, &expired, &cancelSent, &job.attempt)
		if err != nil {
			return err
		}
		if expired {
			return parkRemote(ctx, tx, job, "remote_deadline")
		}
		var permitted bool
		err = tx.QueryRow(ctx, identity.RunGrantSQL, job.Run).Scan(&permitted)
		if err != nil {
			return err
		}
		if !permitted {
			return parkRemote(ctx, tx, job, "remote_denied")
		}
		job.model, _, err = a.Catalog.ResolveRun(ctx, tx, job.project, job.actor, job.agent, job.revision)
		var unavailable web.Problem
		if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &unavailable) {
			return parkRemote(ctx, tx, job, "remote_denied")
		}
		if err != nil {
			return err
		}
		if job.state != "cancelled" {
			if _, _, err := knowledge.Inputs(ctx, tx, job.Run); err != nil {
				var problem web.Problem
				if errors.As(err, &problem) {
					return parkRemote(ctx, tx, job, "remote_denied")
				}
				return err
			}
			var changeOK bool
			err = tx.QueryRow(ctx, `select not exists(select from change_batches c join runs r on r.id=c.run_id join projects p on p.id=r.project_id where c.run_id=$1 and not exists(select from project_members pm where pm.project_id=p.id and pm.member_id=r.actor and(pm.can_review or p.owner=r.actor)))`, job.Run).Scan(&changeOK)
			if err != nil {
				return err
			}
			if !changeOK {
				return parkRemote(ctx, tx, job, "remote_denied")
			}
		}
		job.cancel = job.state == "cancelled" && !cancelSent && job.model.RemoteOperations.CanFollow(true)
		if !job.cancel && !job.model.RemoteOperations.CanFollow(false) {
			if job.state == "cancelled" && !cancelSent {
				return parkRemote(ctx, tx, job, "remote_cancel_unverified")
			}
			return parkRemote(ctx, tx, job, "remote_query_unverified")
		}
		err = tx.QueryRow(ctx, `update remote_jobs set lease=gen_random_uuid(),lease_until=now()+interval '35 seconds',cancel_attempted=cancel_attempted or $3,checks=checks+1 where run_id=$1 and ordinal=$2 returning lease`, job.Run, job.Ordinal, job.cancel).Scan(&job.lease)
		ready = err == nil
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !ready {
		return ready, err
	}
	queryCtx, stop := context.WithDeadline(ctx, job.deadline)
	job.model.ExternalTrace = resources.ExternalTrace{RunID: job.Run, ActorID: job.actor, Turn: job.Ordinal}
	reply, callErr := job.model.CheckRemote(queryCtx, job.receipt, job.cancel)
	stop()
	return true, a.applyRemoteOutcome(ctx, job, reply, callErr)
}

func (a Activities) applyRemoteOutcome(ctx context.Context, job remoteJob, reply resources.Reply, callErr error) error {
	return database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "select status from runs where id=$1 for update", job.Run).Scan(&job.state); err != nil {
			return err
		}
		var failures int
		var cancelSent bool
		err := tx.QueryRow(ctx, "select consecutive_errors,cancel_attempted from remote_jobs where run_id=$1 and ordinal=$2 and lease=$3 for update", job.Run, job.Ordinal, job.lease).Scan(&failures, &cancelSent)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		operation := "poll"
		if job.inputID != "" {
			operation = "supplement"
		} else if job.cancel {
			operation = "cancel"
		}
		if err := resources.RecordExternalReceipt(ctx, tx, resources.ExternalReceiptRef{Request: "external/" + job.lease,
			Run: job.Run, Turn: job.Ordinal, Attempt: job.attempt, Operation: operation}, reply.ExternalMetadata); err != nil {
			return err
		}
		receipt := reply.Receipt
		if job.inputID != "" {
			if callErr != nil {
				_ = errors.As(callErr, &receipt)
			}
			if receipt == nil || receipt.State == "unknown" {
				if err := finishRemoteInput(ctx, tx, job.inputID, "uncertain", "receipt_unconfirmed"); err != nil {
					return err
				}
				if job.state == "cancelled" {
					_, err := tx.Exec(ctx, `update remote_jobs set lease=null,lease_until=null,next_at=now()
                      where (run_id,ordinal)=($1,$2)`, job.Run, job.Ordinal)
					return err
				}
				return parkRemote(ctx, tx, job, "remote_input_unconfirmed")
			}
			if err := finishRemoteInput(ctx, tx, job.inputID, "received", receipt.State); err != nil {
				return err
			}
		}
		if callErr != nil && !errors.As(callErr, &receipt) {
			if failures >= job.model.Policy.ReadRetries {
				return parkRemote(ctx, tx, job, "remote_query_failed")
			}
			_, err = tx.Exec(ctx, `update remote_jobs set lease=null,lease_until=null,consecutive_errors=consecutive_errors+1,next_at=now()+$3*interval '1 second',detail='remote_query_retry' where run_id=$1 and ordinal=$2`, job.Run, job.Ordinal, job.model.Policy.IntervalSeconds*(failures+1))
			return err
		}
		if receipt == nil {
			return parkRemote(ctx, tx, job, "remote_unknown")
		}
		phase, code := remoteDisposition(receipt)
		if job.state == "cancelled" && phase == "manual" && !job.cancel &&
			(cancelSent || !job.model.RemoteOperations.CanFollow(true)) {
			return parkRemote(ctx, tx, job, code)
		}
		if job.state == "cancelled" && phase == "manual" && (!cancelSent || job.cancel) {
			phase = "checking"
		}
		if job.state != "cancelled" && code == "remote_input" {
			isolated, err := stepRemoteInputAllowed(ctx, tx, job.Step, receipt)
			if err != nil {
				return err
			}
			if isolated {
				code = stepRemoteInput
			}
		}
		if _, err = tx.Exec(ctx, `update remote_jobs j set phase=$1,detail=$2,lease=null,lease_until=null,consecutive_errors=0,next_at=now()+$3*interval '1 second',
              input_revision=j.input_revision+q.fresh::int,
              input_prompt=case when q.fresh then $7 else j.input_prompt end,
              input_message=case when q.fresh or ($6='input-required' and $8<>'') then $8 else j.input_message end
            from remote_jobs prior join invocations i using (run_id,ordinal)
            cross join lateral (select a2a_new_question(i.remote_status,prior.input_prompt,prior.input_message,$6,$7,$8) fresh) q
            where (j.run_id,j.ordinal)=(prior.run_id,prior.ordinal) and (j.run_id,j.ordinal)=($4,$5)`,
			phase, code, job.model.Policy.IntervalSeconds, job.Run, job.Ordinal, receipt.State, receipt.Question, receipt.QuestionMessage); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update invocations set remote_status=$1 where run_id=$2 and ordinal=$3", receipt.State, job.Run, job.Ordinal); err != nil {
			return err
		}
		if phase == "checking" {
			return nil
		}
		if code == stepRemoteInput {
			// A repeated question does not grant a second send for a revision
			// that already has a non-rejected input receipt.
			var canSupply bool
			if err := tx.QueryRow(ctx, `select not exists(select from remote_task_inputs q join remote_jobs j using(run_id,ordinal)
                where (q.run_id,q.ordinal,q.input_revision)=(j.run_id,j.ordinal,j.input_revision)
                  and (j.run_id,j.ordinal)=($1,$2) and q.state<>'rejected')`, job.Run, job.Ordinal).Scan(&canSupply); err != nil {
				return err
			}
			if canSupply {
				if job.state == "reconcile" {
					return resumeReturnedSubmission(ctx, tx, job.Run, job.project)
				}
				return nil
			}
			return parkRemote(ctx, tx, job, "remote_question_unchanged")
		}
		if receipt.State != "completed" || receipt.NoText {
			if phase == "finished" {
				_, err = tx.Exec(ctx, "update invocations set status='remote_closed',ended_at=now() where run_id=$1 and ordinal=$2", job.Run, job.Ordinal)
				if err != nil {
					return err
				}
			}
			if job.state == "running" {
				return setState(ctx, tx, job.Step, job.project, "reconcile", code, remoteNote(code))
			}
			return nil
		}
		var qualifiedRemote bool
		if job.state != "cancelled" {
			operations, err := resources.ReadRemoteOperations(ctx, tx, job.agent, job.revision)
			if err != nil {
				return err
			}
			if (job.inputID == "" && !operations.CanReturnText(true)) || (job.inputID != "" && !operations.CanReturnSupplementText()) {
				if _, err := tx.Exec(ctx, "update remote_jobs set detail='remote_text_unverified' where run_id=$1 and ordinal=$2", job.Run, job.Ordinal); err != nil {
					return err
				}
				return closeUnverifiedRemoteOutput(ctx, tx, job.Step, job.project, job.state, receipt)
			}
			qualifiedRemote = operations != nil
		}
		var body any
		working := true
		if job.state != "cancelled" {
			working, err = memory.Usable(ctx, tx, memory.Key{Run: job.Run, Step: job.Ordinal, Attempt: job.attempt})
			if err != nil {
				return err
			}
		}
		var excerpt string
		if working && job.state != "cancelled" {
			var base string
			err = tx.QueryRow(ctx, `select coalesce(cb.excerpt,''),case when coalesce(cb.excerpt,'')<>'' then av.body else '' end from runs r left join change_batches cb on cb.run_id=r.id left join artifact_versions av on(av.artifact_id,av.version)=(cb.artifact_id,cb.base_version) where r.id=$1`, job.Run).Scan(&excerpt, &base)
			if err != nil {
				return err
			}
			body = reply.Text
			if excerpt != "" {
				body = strings.Replace(base, excerpt, reply.Text, 1)
			}
		}
		if _, err = tx.Exec(ctx, "update invocations set status='completed',result=$1,ended_at=now() where run_id=$2 and ordinal=$3", body, job.Run, job.Ordinal); err != nil {
			return err
		}
		if working && qualifiedRemote && excerpt == "" && !runEnded(job.state) && !job.cancel {
			way := "query"
			if job.inputID != "" {
				way = "supplement"
			}
			if err := recordRemoteCompletion(ctx, tx, job.Step, job.attempt, job.agent, job.revision, receipt, reply.Text, way, job.inputID); err != nil {
				return err
			}
		}
		if !working {
			if job.state == "running" || job.state == "reconcile" {
				if parked, err := parkRevokedWorkingRule(ctx, tx, job.Step, job.project, job.attempt); parked || err != nil {
					return err
				}
				return setState(ctx, tx, job.Step, job.project, "blocked", "working_memory_invalidated", memory.Unavailable)
			}
			return nil
		}
		if job.state == "reconcile" {
			return resumeReturnedSubmission(ctx, tx, job.Run, job.project)
		}
		if job.state != "cancelled" {
			_, err = tx.Exec(ctx, "update remote_jobs set wake=true where run_id=$1 and ordinal=$2", job.Run, job.Ordinal)
		}
		return err
	})
}
func parkRemote(ctx context.Context, tx pgx.Tx, j remoteJob, code string) error {
	_, err := tx.Exec(ctx, "update remote_jobs set phase='manual',detail=$3,lease=null,lease_until=null where run_id=$1 and ordinal=$2", j.Run, j.Ordinal, code)
	if err != nil {
		return err
	}
	if j.state == "running" {
		return setState(ctx, tx, j.Step, j.project, "reconcile", code, remoteNote(code))
	}
	return nil
}
func (a Activities) wakeRemote(ctx context.Context, c client.Client) error {
	return database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var j Step
		var state string
		err := tx.QueryRow(ctx, `select j.run_id,j.ordinal,r.status from remote_jobs j join runs r on r.id=j.run_id join outbox o on o.run_id=r.id where wake and o.sent_at is not null limit 1 for update of j skip locked`).Scan(&j.Run, &j.Ordinal, &state)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		sub, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		err = c.SignalWorkflow(sub, "run/"+j.Run, "", "control", remoteReceiptSignal(j.Ordinal))
		var gone *serviceerror.NotFound
		if err != nil && !(errors.As(err, &gone) && runEnded(state)) {
			return err
		}
		_, err = tx.Exec(ctx, "update remote_jobs set wake=false where run_id=$1 and ordinal=$2", j.Run, j.Ordinal)
		return err
	})
}
