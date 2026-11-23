package main

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Compare the dispatched run with Temporal's current execution and its visibility
// record. A missing record is reported; it never creates an outbox entry.
type workflowReport struct{ Dispatched, AwaitingDispatch, Matched, MissingHistory, MissingVisibility, DifferentStatus int }
type historyEntry struct {
	WorkflowID, RunID string
	Status            int
}

func compareWorkflows(ctx context.Context, names map[string]string) (workflowReport, error) {
	var report workflowReport
	business, err := connect(ctx, names["business"])
	if err != nil {
		return report, err
	}
	defer business.Close(ctx)
	history, err := connect(ctx, names["temporal"])
	if err != nil {
		return report, err
	}
	defer history.Close(ctx)
	visibility, err := connect(ctx, names["visibility"])
	if err != nil {
		return report, err
	}
	defer visibility.Close(ctx)
	var namespace string
	if err = history.QueryRow(ctx, "select encode(id,'hex')::uuid::text from namespaces where name='default'").Scan(&namespace); err != nil {
		return report, err
	}
	rows, err := business.Query(ctx, "select 'run/'||r.id, o.sent_at is not null from runs r left join outbox o on o.run_id=r.id order by r.id")
	if err != nil {
		return report, err
	}
	defer rows.Close()
	var batch []string
	for rows.Next() {
		var id string
		var dispatched bool
		if err = rows.Scan(&id, &dispatched); err != nil {
			return report, err
		}
		if !dispatched {
			report.AwaitingDispatch++
			continue
		}
		report.Dispatched++
		batch = append(batch, id)
		if len(batch) == 256 {
			if err = compareBatch(ctx, namespace, batch, history, visibility, &report); err != nil {
				return report, err
			}
			batch = batch[:0]
		}
	}
	if err = rows.Err(); err != nil {
		return report, err
	}
	if len(batch) > 0 {
		err = compareBatch(ctx, namespace, batch, history, visibility, &report)
	}
	return report, err
}
func compareBatch(ctx context.Context, ns string, ids []string, history, visibility *pgx.Conn, report *workflowReport) error {
	rows, err := history.Query(ctx, `select c.workflow_id,encode(c.run_id,'hex')::uuid::text,c.status from current_executions c join executions e using(shard_id,namespace_id,workflow_id,run_id) where c.namespace_id=uuid_send($1::uuid) and c.workflow_id=any($2) and e.next_event_id>1`, ns, ids)
	if err != nil {
		return err
	}
	records, err := pgx.CollectRows(rows, pgx.RowToStructByPos[historyEntry])
	if err != nil {
		return err
	}
	report.MissingHistory += len(ids) - len(records)
	rows, err = visibility.Query(ctx, `select workflow_id,trim(run_id),status from executions_visibility where namespace_id=$1 and workflow_id=any($2)`, ns, ids)
	if err != nil {
		return err
	}
	found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[historyEntry])
	if err != nil {
		return err
	}
	current := map[string]historyEntry{}
	for _, r := range found {
		current[r.RunID] = r
	}
	for _, r := range records {
		listed, ok := current[r.RunID]
		switch {
		case !ok || listed.WorkflowID != r.WorkflowID:
			report.MissingVisibility++
		case listed.Status != r.Status:
			report.DifferentStatus++
		default:
			report.Matched++
		}
	}
	return nil
}
