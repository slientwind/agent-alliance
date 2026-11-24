package runtime

import (
	"alliance/internal/billing"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/memory"
	"alliance/internal/web"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Only queued inputs are eligible for a send. A lost worker lease after
// submission becomes an uncertain receipt and never re-enters the send queue.
func (a Activities) sendRemoteInput(ctx context.Context) (bool, error) {
	var job remoteJob
	var input string
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select r.id,r.project_id,r.actor,r.status from runs r
            where exists(select from remote_task_inputs q where q.run_id=r.id and
              (q.state='queued' or (q.state='submitted' and q.claim_until<=now())))
            order by (select min(q.created_at) from remote_task_inputs q where q.run_id=r.id and q.state in ('queued','submitted'))
            limit 1 for update of r skip locked`).Scan(&job.Run, &job.project, &job.actor, &job.state); err != nil {
			return err
		}
		var state, operator string
		var matches, bound bool
		var sealed []byte
		err := tx.QueryRow(ctx, `select q.id,q.ordinal,q.state,q.actor,q.encrypted_body,n.agent_id,n.agent_revision,i.attempt_no,
              coalesce(i.remote_task,''),coalesce(i.remote_context,''),j.phase='manual' and r.status='reconcile'
                and i.remote_status='input-required' and not j.cancel_attempted
                and (r.control_revision,j.input_revision,i.attempt_no,i.remote_task,i.remote_context)=
                    (q.control_revision,q.input_revision,q.attempt_no,q.task_id,q.context_id)
                and q.created_at+make_interval(secs=>j.check_window_seconds)>now(),
              q.channel_binding_id is null or exists(select from channel_links l
                  where (l.member_id,l.binding_id)=(q.actor,q.channel_binding_id))
            from remote_task_inputs q join remote_jobs j using(run_id,ordinal)
            join runs r on r.id=q.run_id join invocations i on(i.run_id,i.ordinal)=(q.run_id,q.ordinal)
            join effective_run_nodes n on(n.run_id,n.ordinal)=(q.run_id,q.ordinal)
            where q.run_id=$1 and (q.state='queued' or (q.state='submitted' and q.claim_until<=now()))
            order by q.created_at,q.id limit 1 for update of q,j`, job.Run).Scan(&job.inputID, &job.Ordinal, &state, &operator, &sealed,
			&job.agent, &job.revision, &job.attempt, &job.receipt.Task, &job.receipt.Context, &matches, &bound)
		if err != nil {
			return err
		}
		if state == "submitted" {
			if err := finishRemoteInput(ctx, tx, job.inputID, "uncertain", "receipt_unconfirmed"); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `update remote_jobs set lease=null,lease_until=null,
                detail=case when $4='cancelled' then detail else 'remote_input_unconfirmed' end
                where (run_id,ordinal)=($1,$2) and lease=$3`, job.Run, job.Ordinal, job.inputID, job.state)
			return err
		}
		if !matches {
			return finishRemoteInput(ctx, tx, job.inputID, "rejected", "question_changed")
		}
		if !bound {
			return finishRemoteInput(ctx, tx, job.inputID, "rejected", "scope_changed")
		}
		if err := a.prepareRemoteInput(ctx, tx, &job, operator); err != nil {
			var denied web.Problem
			if errors.As(err, &denied) || errors.Is(err, pgx.ErrNoRows) {
				return finishRemoteInput(ctx, tx, job.inputID, "rejected", "scope_changed")
			}
			return err
		}

		text, err := a.Catalog.Vault.Open(sealed, "remote-input/"+job.inputID)
		if err != nil {
			return finishRemoteInput(ctx, tx, job.inputID, "rejected", "input_unavailable")
		}
		op := billing.Operation{Key: "remote-input/" + job.inputID, Kind: "remote-input", Actor: operator, Run: job.Run,
			Project: job.project, Team: job.model.Team, Resource: job.model.BudgetID, SourceName: job.model.Name,
			SourceRevision: job.model.SourceRevision, AllowUnknown: true}
		if err := billing.Reserve(ctx, tx, op); err != nil {
			var denied web.Problem
			if errors.As(err, &denied) {
				return finishRemoteInput(ctx, tx, job.inputID, "rejected", "budget_denied")
			}
			return err
		}
		if job.model.ProjectGrant != "" {
			if _, err := tx.Exec(ctx, "insert into project_agent_calls(op_key,grant_id) values($1,$2)", op.Key, job.model.ProjectGrant); err != nil {
				return err
			}
		}
		job.lease = job.inputID
		if _, err := tx.Exec(ctx, `update remote_jobs set lease=$3,lease_until=now()+interval '100 seconds',
             deadline=now()+make_interval(secs=>check_window_seconds)
           where (run_id,ordinal)=($1,$2)`, job.Run, job.Ordinal, job.lease); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update remote_task_inputs set state='submitted',encrypted_body=null,
             claim_until=now()+interval '100 seconds' where id=$1`, job.inputID); err != nil {
			return err
		}
		input = text
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || input == "" {
		return job.inputID != "", err
	}
	reply, callErr := job.model.SupplementRemote(ctx, job.receipt, job.inputID, input)
	return true, a.applyRemoteOutcome(ctx, job, reply, callErr)
}

func (a Activities) prepareRemoteInput(ctx context.Context, tx pgx.Tx, job *remoteJob, operator string) error {
	var permitted bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, job.Run).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return web.Fail(403, "原任务委托已结束")
	}
	if err := tx.QueryRow(ctx, `select exists(select from runs r join projects p on p.id=r.project_id
            join project_members m on(m.project_id,m.member_id)=(p.id,$2)
            join members u on u.id=m.member_id and u.enabled and u.kind='person'
            where r.id=$1 and (p.owner=$2 or (r.actor=$2 and m.can_review and exists(select from change_batches where run_id=r.id))))`,
		job.Run, operator).Scan(&permitted); err != nil {
		return err
	}
	if !permitted {
		return web.Fail(403, "补件职责已变化")
	}
	model, _, err := a.Catalog.ResolveRun(ctx, tx, job.project, job.actor, job.agent, job.revision)
	if err != nil {
		return err
	}
	if !model.RemoteOperations.CanSupplement() || !model.RemoteOperations.CanStart() {
		return web.Fail(409, "远端补件或成果验证证据已失效")
	}
	if _, _, err := knowledge.Inputs(ctx, tx, job.Run); err != nil {
		return err
	}
	usable, err := memory.Usable(ctx, tx, memory.Key{Run: job.Run, Step: job.Ordinal, Attempt: job.attempt})
	if err != nil {
		return err
	}
	if !usable {
		return web.Fail(409, memory.Unavailable)
	}
	if model.ProjectGrant != "" {
		if err := tx.QueryRow(ctx, `select call_limit>(select count(*) from project_agent_calls where grant_id=g.id)
            from project_agent_grants g where id=$1`, model.ProjectGrant).Scan(&permitted); err != nil {
			return err
		}
		if !permitted {
			return web.Fail(409, "项目授权调用次数已用完")
		}
	}
	job.model = model
	return nil
}

func finishRemoteInput(ctx context.Context, tx pgx.Tx, id, state, detail string) error {
	_, err := tx.Exec(ctx, `update remote_task_inputs set state=$2,detail=$3,encrypted_body=null,
           claim_until=null,decided_at=now() where id=$1 and state in ('queued','submitted')`, id, state, detail)
	return err
}
