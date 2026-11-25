package automations

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

const catchupWindow = 5 * time.Minute

func Serve(ctx context.Context, db *pgxpool.Pool, c client.ScheduleClient, queue string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	revocations := time.NewTicker(time.Minute)
	defer revocations.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-revocations.C:
			if err := pauseRevoked(ctx, db); err != nil && ctx.Err() == nil {
				slog.Warn("automation membership reconciliation pending")
			}
		case <-ticker.C:
			if err := syncSchedule(ctx, db, c, queue); err != nil && ctx.Err() == nil {
				slog.Warn("automation schedule synchronization pending")
			}
		}
	}
}

func pauseRevoked(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `with revoked as (
		select a.id from automation_schedules a where a.state='active' and not exists(
		 select from members u join team_members tm on (tm.team_id,tm.member_id)=(a.team_id,u.id)
		 where u.id=a.owner and u.enabled and u.kind='person')
		order by a.id limit 50 for update skip locked
	) update automation_schedules a set state='paused',revision=revision+1,sync_next_at=now(),updated_at=now()
	  from revoked where a.id=revoked.id`)
	return err
}

func syncSchedule(ctx context.Context, db *pgxpool.Pool, c scheduleClient, queue string) error {
	return database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var ref ScheduleRef
		var spec config
		var state string
		err := tx.QueryRow(ctx, `select id,revision,cron,once_at,time_zone,state from automation_schedules
			where synced_revision<>revision and sync_next_at<=now() order by sync_next_at,id for update skip locked limit 1`).
			Scan(&ref.ID, &ref.Revision, &spec.Cron, &spec.OnceAt, &spec.Zone, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := applySchedule(short, c, queue, ref, spec, state); err != nil {
			_, err = tx.Exec(ctx, `update automation_schedules set sync_failed=true,sync_next_at=now()+interval '10 seconds' where id=$1`, ref.ID)
			return err
		}
		_, err = tx.Exec(ctx, `update automation_schedules set sync_failed=false,synced_revision=$2 where id=$1`, ref.ID, ref.Revision)
		return err
	})
}

func applySchedule(ctx context.Context, c scheduleClient, queue string, ref ScheduleRef, specification config, state string) error {
	id := "automation/" + ref.ID
	handle := c.GetHandle(ctx, id)
	if state == "deleted" {
		err := handle.Delete(ctx)
		var missing *serviceerror.NotFound
		if errors.As(err, &missing) {
			return nil
		}
		return err
	}
	spec := specification.spec()
	policy := client.SchedulePolicies{Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP, CatchupWindow: catchupWindow}
	next := client.ScheduleState{Paused: state == "paused"}
	action := &client.ScheduleWorkflowAction{ID: id, Workflow: WorkflowName, TaskQueue: queue, Args: []any{ref}}
	update := client.ScheduleUpdateOptions{DoUpdate: func(client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
		return &client.ScheduleUpdate{Schedule: &client.Schedule{Spec: &spec, Action: action, Policy: &policy, State: &next}}, nil
	}}
	err := handle.Update(ctx, update)
	var missing *serviceerror.NotFound
	if !errors.As(err, &missing) {
		return err
	}
	options := client.ScheduleOptions{ID: id, Spec: spec, Action: action, Overlap: policy.Overlap, CatchupWindow: policy.CatchupWindow,
		Paused: next.Paused}
	if at := specification.OnceAt; !next.Paused && at != nil && !at.After(time.Now()) && time.Since(*at) <= catchupWindow {
		options.ScheduleBackfill = []client.ScheduleBackfill{{Start: at.Add(-time.Second), End: at.Add(time.Second)}}
	}
	_, err = c.Create(ctx, options)
	if errors.Is(err, temporal.ErrScheduleAlreadyRunning) {
		return handle.Update(ctx, update)
	}
	return err
}
