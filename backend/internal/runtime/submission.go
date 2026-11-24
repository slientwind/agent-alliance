package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"alliance/internal/database"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

// A worker can disappear after committing the intent. Detection also runs on
// replacement workers and never sends the original request again.
func (a Activities) checkSubmissions(ctx context.Context) error {
	return database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var step Step
		var project string
		err := tx.QueryRow(ctx, `select r.id,w.ordinal,r.project_id from runs r join submission_windows w on w.run_id=r.id
         where r.status='running' and w.execution_due<=clock_timestamp()
         order by w.execution_due limit 1 for update of r skip locked`).Scan(&step.Run, &step.Ordinal, &project)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var stillDue bool
		if err = tx.QueryRow(ctx, "select exists(select from submission_windows where run_id=$1 and ordinal=$2 and execution_due<=clock_timestamp())", step.Run, step.Ordinal).Scan(&stillDue); err != nil || !stillDue {
			return err
		}
		return setState(ctx, tx, step, project, "reconcile", "submission_overdue", "调用超过执行时限，尚无可查询回执。核对窗口结束后可决定是否授权新增调用。")
	})
}

func (a Activities) wakeReturnedSubmission(ctx context.Context, c client.Client) error {
	return database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var run, state string
		err := tx.QueryRow(ctx, `select w.run_id,r.status from receipt_wakes w join runs r on r.id=w.run_id
            join outbox o on o.run_id=r.id where o.sent_at is not null limit 1 for update of w skip locked`).Scan(&run, &state)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		sub, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		err = c.SignalWorkflow(sub, "run/"+run, "", "control", "original-receipt")
		var gone *serviceerror.NotFound
		if err != nil && !(errors.As(err, &gone) && runEnded(state)) {
			return err
		}
		_, err = tx.Exec(ctx, "delete from receipt_wakes where run_id=$1", run)
		return err
	})
}

// The successful original receipt is already in the ledger. Wake the workflow
// to recheck the usual publication gates, including any remaining branches.
func resumeReturnedSubmission(ctx context.Context, tx pgx.Tx, run, project string) error {
	if err := projects.ReleaseReviewedCalls(ctx, tx, run); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `update runs r set status='running' where id=$1 and status='reconcile'
     and not exists(select from invocations i left join remote_jobs j using(run_id,ordinal) where i.run_id=r.id
         and (i.status in('submitted','unknown','remote_closed','input_rejected') or
           (j.phase='manual' and not remote_step_input_parked(j.run_id,j.ordinal,j.phase,j.detail))))`, run)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if err := projects.TouchRun(ctx, tx, run, project, "running"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "insert into receipt_wakes values($1) on conflict do nothing", run)
	return err
}

func receiptDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The old attempt has lost the right to publish. Keep reconciliation evidence
// against its own operation; never attach its body or file to the new artifact.
func saveLateReceipt(ctx context.Context, tx pgx.Tx, step Step, attempt int, m resources.Model, reply resources.Reply, callErr error) error {
	outcome := "unknown"
	var tokens, body, file, task, remoteContext, state any
	var invalid *resources.ToolInputError
	receipt := reply.Receipt
	if callErr == nil {
		outcome = "completed"
		tokens = callTokens(m, reply)
		body = receiptDigest([]byte(reply.Text))
		if reply.File != nil {
			file = receiptDigest(reply.File.Data)
		}
	} else if errors.As(callErr, &invalid) {
		outcome = "input_rejected"
	} else if errors.As(callErr, &receipt) {
		outcome = "remote"
	}
	if receipt != nil {
		task, remoteContext, state = receipt.Task, receipt.Context, receipt.State
	}
	_, err := tx.Exec(ctx, `insert into late_call_receipts(run_id,ordinal,attempt_no,outcome,tokens,body_sha256,file_sha256,remote_task,remote_context,remote_state)
     values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) on conflict do nothing`, step.Run, step.Ordinal, attempt, outcome, tokens, body, file, task, remoteContext, state)
	return err
}
