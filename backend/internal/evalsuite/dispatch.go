package evalsuite

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

func (s Server) Dispatch(ctx context.Context, client client.Client, queue string) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := s.dispatchOne(ctx, client, queue); err != nil && ctx.Err() == nil {
			slog.Warn("golden evaluation dispatch pending", "error", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
	}
}

func (s Server) dispatchOne(ctx context.Context, conn client.Client, queue string) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var run string
		err := tx.QueryRow(ctx, `select id from eval_runs where state='queued' and dispatched_at is null
		 and next_dispatch_at<=clock_timestamp() order by next_dispatch_at,id limit 1 for update skip locked`).Scan(&run)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		short, stop := context.WithTimeout(ctx, 4*time.Second)
		_, err = conn.ExecuteWorkflow(short, client.StartWorkflowOptions{ID: "eval/" + run, TaskQueue: queue,
			WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionErrorWhenAlreadyStarted: true}, WorkflowName, run)
		stop()
		var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &duplicate) {
			_, err = tx.Exec(ctx, "update eval_runs set next_dispatch_at=clock_timestamp()+interval '15 seconds' where id=$1", run)
			return err
		}
		_, err = tx.Exec(ctx, "update eval_runs set dispatched_at=clock_timestamp() where id=$1", run)
		return err
	})
}
