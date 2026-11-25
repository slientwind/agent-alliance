package agentloop

import (
	"alliance/internal/database"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
)

func (a Activities) Dispatch(ctx context.Context, c client.Client, queue string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.startQueued(ctx, c, queue); err != nil && ctx.Err() == nil {
				slog.Warn("general workflow dispatch pending")
			}
			if err := a.sendControl(ctx, c); err != nil && ctx.Err() == nil {
				slog.Warn("general workflow control pending")
			}
		}
	}
}

func (a Activities) startQueued(ctx context.Context, c client.Client, queue string) error {
	return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var run string
		err := tx.QueryRow(ctx, `select run_id from general_agent_dispatch where sent_at is null and next_at<=now()
		 order by next_at,run_id limit 1 for update skip locked`).Scan(&run)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		short, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		_, err = c.ExecuteWorkflow(short, client.StartWorkflowOptions{ID: "run/" + run, TaskQueue: queue,
			WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionErrorWhenAlreadyStarted: true}, WorkflowName, run)
		var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
		if err != nil && !errors.As(err, &duplicate) {
			_, err = tx.Exec(ctx, `update general_agent_dispatch set next_at=now()+interval '5 seconds',attempts=attempts+1 where run_id=$1`, run)
			return err
		}
		_, err = tx.Exec(ctx, `update general_agent_dispatch set sent_at=now(),attempts=attempts+1 where run_id=$1`, run)
		return err
	})
}

func (a Activities) sendControl(ctx context.Context, c client.Client) error {
	return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var id, run, state string
		err := tx.QueryRow(ctx, `select c.id,c.run_id,r.status from controls c join general_agent_dispatch d on d.run_id=c.run_id
		 join runs r on r.id=c.run_id where d.sent_at is not null and c.sent_at is null order by c.created_at limit 1 for update of c skip locked`).
			Scan(&id, &run, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		short, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		err = c.SignalWorkflow(short, "run/"+run, "", "control", id)
		var missing *serviceerror.NotFound
		if err != nil && !(errors.As(err, &missing) && (state == "cancelled" || state == "completed" || state == "failed")) {
			return err
		}
		_, err = tx.Exec(ctx, `update controls set sent_at=now() where id=$1`, id)
		return err
	})
}
