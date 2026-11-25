package ecosystem

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

type scheduleClient interface {
	Create(context.Context, client.ScheduleOptions) (client.ScheduleHandle, error)
	GetHandle(context.Context, string) client.ScheduleHandle
}

func Serve(ctx context.Context, db *pgxpool.Pool, c client.Client, queue string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := syncSchedule(ctx, db, c.ScheduleClient(), queue); err != nil && ctx.Err() == nil {
				slog.Warn("ecosystem schedule synchronization pending")
			}
			if err := dispatchScan(ctx, db, c, queue); err != nil && ctx.Err() == nil {
				slog.Warn("ecosystem scan dispatch pending")
			}
		}
	}
}

func syncSchedule(ctx context.Context, db *pgxpool.Pool, client scheduleClient, queue string) error {
	return database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var ref ScanRef
		var expression, zone string
		var active bool
		err := tx.QueryRow(ctx, `select id,revision,cron,time_zone,enabled from ecosystem_sources
		 where schedule_revision<>revision and schedule_next_at<=now()
		 order by schedule_next_at,id limit 1 for update skip locked`).Scan(&ref.ID, &ref.Revision, &expression, &zone, &active)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		short, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		if err := applySchedule(short, client, queue, ref, expression, zone, active); err != nil {
			_, err = tx.Exec(ctx, `update ecosystem_sources set schedule_sync='retrying',
			 schedule_next_at=now()+interval '10 seconds' where id=$1`, ref.ID)
			return err
		}
		_, err = tx.Exec(ctx, `update ecosystem_sources set schedule_revision=$2,schedule_sync='ready' where id=$1`, ref.ID, ref.Revision)
		return err
	})
}

func applySchedule(ctx context.Context, c scheduleClient, queue string, ref ScanRef, expression, zone string, active bool) error {
	id := "ecosystem/" + ref.ID
	spec := client.ScheduleSpec{CronExpressions: []string{expression}, TimeZoneName: zone}
	policy := client.SchedulePolicies{Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: 5 * time.Minute}
	state := client.ScheduleState{Paused: !active}
	action := &client.ScheduleWorkflowAction{ID: id, Workflow: ScanWorkflowName, TaskQueue: queue, Args: []any{ref}}
	handle := c.GetHandle(ctx, id)
	update := client.ScheduleUpdateOptions{DoUpdate: func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
		return &client.ScheduleUpdate{Schedule: &client.Schedule{Spec: &spec, Action: action, Policy: &policy, State: &state}}, nil
	}}
	err := handle.Update(ctx, update)
	var missing *serviceerror.NotFound
	if !errors.As(err, &missing) {
		return err
	}
	_, err = c.Create(ctx, client.ScheduleOptions{ID: id, Spec: spec, Action: action, Overlap: policy.Overlap,
		CatchupWindow: policy.CatchupWindow, Paused: state.Paused})
	if errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return handle.Update(ctx, update)
	}
	return err
}

type scanDispatcher interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, interface{}, ...interface{}) (client.WorkflowRun, error)
}

func dispatchScan(ctx context.Context, db *pgxpool.Pool, c scanDispatcher, queue string) error {
	return database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var id string
		var ref ScanRef
		var current bool
		err := tx.QueryRow(ctx, `select q.id,q.source_id,q.source_revision,
		 s.enabled and s.revision=q.source_revision and actor.enabled and actor.role='admin' and actor.kind='person'
		 from ecosystem_scan_requests q join ecosystem_sources s on s.id=q.source_id join members actor on actor.id=q.actor
		 where q.sent_at is null and q.next_at<=now() order by q.next_at,q.id limit 1 for update of q skip locked`).
			Scan(&id, &ref.ID, &ref.Revision, &current)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if current {
			short, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			_, err = c.ExecuteWorkflow(short, client.StartWorkflowOptions{ID: "ecosystem/manual/" + id, TaskQueue: queue,
				WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowExecutionErrorWhenAlreadyStarted: true},
				ScanWorkflowName, ref)
			var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
			if err != nil && !errors.As(err, &duplicate) {
				_, err := tx.Exec(ctx, `update ecosystem_scan_requests set next_at=now()+interval '10 seconds' where id=$1`, id)
				return err
			}
		}
		_, err = tx.Exec(ctx, `update ecosystem_scan_requests set sent_at=clock_timestamp() where id=$1`, id)
		return err
	})
}
