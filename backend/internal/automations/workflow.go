package automations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"alliance/internal/agentloop"
	"alliance/internal/database"
	"alliance/internal/llmgateway"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WorkflowName     = "AutomationWorkflow"
	PrepareActivity  = "Automation.Prepare"
	ProgressActivity = "Automation.Progress"
)

var scheduledStart = temporal.NewSearchAttributeKeyTime("TemporalScheduledStartTime")

type ScheduleRef struct {
	ID       string
	Revision int
}

type Occurrence struct {
	Schedule ScheduleRef
	Key      string
	At       time.Time
}

type Activities struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

func AutomationWorkflow(ctx workflow.Context, ref ScheduleRef) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumInterval: 30 * time.Second},
	})
	info := workflow.GetInfo(ctx)
	at, ok := workflow.GetTypedSearchAttributes(ctx).GetTime(scheduledStart)
	if !ok {
		at = info.WorkflowStartTime
	}
	occurrence := Occurrence{Schedule: ref, Key: info.WorkflowExecution.ID, At: at}
	var run string
	if err := workflow.ExecuteActivity(ctx, PrepareActivity, occurrence).Get(ctx, &run); err != nil || run == "" {
		return err
	}
	for range 200 {
		var complete bool
		if err := workflow.ExecuteActivity(ctx, ProgressActivity, occurrence, run).Get(ctx, &complete); err != nil || complete {
			return err
		}
		if err := workflow.Sleep(ctx, 15*time.Second); err != nil {
			return err
		}
	}
	return workflow.NewContinueAsNewError(ctx, WorkflowName, ref)
}

func (a Activities) Prepare(ctx context.Context, occurrence Occurrence) (string, error) {
	var run string
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		id := occurrence.Schedule.ID
		var owner, team, state string
		var cipher []byte
		var tier llmgateway.Tier
		var cursor, onceAt *time.Time
		var revision int
		var eligible bool
		err := tx.QueryRow(ctx, `select a.owner,a.team_id,a.state,a.revision,a.goal_cipher,a.tier,a.cursor_at,a.once_at,
			exists(select from members u join team_members tm on (tm.team_id,tm.member_id)=(a.team_id,u.id)
			 where u.id=a.owner and u.enabled and u.kind='person')
			from automation_schedules a where a.id=$1 for update of a`, id).
			Scan(&owner, &team, &state, &revision, &cipher, &tier, &cursor, &onceAt, &eligible)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `select coalesce(run_id::text,'') from automation_runs where (schedule_id,occurrence)=($1,$2)`,
			id, occurrence.Key).Scan(&run)
		if err == nil || !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		at := occurrence.At
		if onceAt != nil && revision == occurrence.Schedule.Revision {
			at = *onceAt
		}
		if revision != occurrence.Schedule.Revision || state != "active" || !eligible {
			return recordRun(ctx, tx, occurrence, at, cursor, "", "", "skipped")
		}
		var occupied bool
		err = tx.QueryRow(ctx, `select exists(select from automation_runs where schedule_id=$1
			and (state='running' or (scheduled_at=$2 and run_id is not null)))`, id, at).Scan(&occupied)
		if err != nil {
			return err
		}
		if occupied || (cursor != nil && !at.After(*cursor)) {
			return recordRun(ctx, tx, occurrence, at, cursor, "", "", "skipped")
		}
		goal, err := a.Vault.Open(cipher, goalScope(id))
		if err != nil {
			return err
		}
		brief := goal + "\n\n本次处理截至 " + at.UTC().Format(time.RFC3339) + "。"
		if cursor != nil {
			brief += "\n上次成功处理至 " + cursor.UTC().Format(time.RFC3339) + "。若数据源提供时间范围，请处理此时间之后且在本次截止时间以内的更新。"
		}
		var project string
		err = pgx.BeginFunc(ctx, tx, func(nested pgx.Tx) error {
			var err error
			project, err = projects.CreateProjectDraft(ctx, nested, owner, team, brief)
			if err != nil {
				return err
			}
			key := fmt.Sprintf("automation/%x", web.Hash(occurrence.Key))
			run, err = (agentloop.Server{Vault: a.Vault}).StartTx(ctx, nested, project, owner, key,
				agentloop.StartInput{Goal: brief, Tier: tier, Expected: 1})
			return err
		})
		if err != nil {
			var problem web.Problem
			if errors.As(err, &problem) && problem.Status < 500 {
				run = ""
				return recordRun(ctx, tx, occurrence, at, cursor, "", "", "failed")
			}
			return err
		}
		return recordRun(ctx, tx, occurrence, at, cursor, project, run, "running")
	})
	return run, err
}

func recordRun(ctx context.Context, tx pgx.Tx, occurrence Occurrence, at time.Time, cursor *time.Time, project, run, state string) error {
	_, err := tx.Exec(ctx, `insert into automation_runs(schedule_id,occurrence,schedule_revision,scheduled_at,cursor_at,project_id,run_id,state,finished_at)
		values($1,$2,$3,$4,$5,nullif($6,'')::uuid,nullif($7,'')::uuid,$8,case when $8='running' then null else now() end)`,
		occurrence.Schedule.ID, occurrence.Key, occurrence.Schedule.Revision, at, cursor, project, run, state)
	return err
}

func (a Activities) Progress(ctx context.Context, occurrence Occurrence, run string) (bool, error) {
	var complete bool
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		id := occurrence.Schedule.ID
		var state, status string
		var at time.Time
		err := tx.QueryRow(ctx, `select r.status,a.state,a.scheduled_at from automation_schedules s join automation_runs a on a.schedule_id=s.id
			join runs r on r.id=a.run_id where (s.id,a.occurrence,r.id)=($1,$2,$3) for update of s,a`, id, occurrence.Key, run).
			Scan(&status, &state, &at)
		if err != nil {
			return err
		}
		complete = state != "running" || status == "completed" || status == "failed" || status == "cancelled"
		if state != "running" || !complete {
			return nil
		}
		if _, err := tx.Exec(ctx, `update automation_runs set state=$3,finished_at=now() where (schedule_id,occurrence)=($1,$2)`,
			id, occurrence.Key, status); err != nil {
			return err
		}
		if status == "completed" {
			_, err = tx.Exec(ctx, `update automation_schedules set cursor_at=greatest(cursor_at,$2) where id=$1`, id, at)
		}
		return err
	})
	return complete, err
}
