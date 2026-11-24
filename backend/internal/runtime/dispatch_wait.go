package runtime

import (
	"context"

	"alliance/internal/projects"
	"github.com/jackc/pgx/v5"
)

func (a Activities) EnableStepDispatchWaits(ctx context.Context, run string) error {
	_, err := a.Catalog.DB.Exec(ctx, "update runs set step_dispatch_wait=true where id=$1 and not step_dispatch_wait", run)
	return err
}

// Callers hold the run lock. A wait is stored before the first invocation,
// with just the fixed step and the reason needed to retry its checks.
func parkDispatchStep(ctx context.Context, tx pgx.Tx, step Step, d contextData, reason, note string) error {
	if !d.canWaitForDispatch {
		return setState(ctx, tx, step, d.project, "blocked", reason, note)
	}
	_, err := tx.Exec(ctx, `insert into step_dispatch_waits(run_id,ordinal,plan_version,agent_id,agent_revision,reason,note)
        values($1,$2,$3,$4,$5,$6,$7)`, step.Run, step.Ordinal, d.planVersion, d.agent, d.revision, reason, note)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update runs set status='running' where id=$1 and status='queued'", step.Run); err != nil {
		return err
	}
	if err := projects.TouchRun(ctx, tx, step.Run, d.project, "running"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'wait',$3)", step.Run, step.Ordinal, reason); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "select record_service_event($1,'step','dispatch_waiting',$2)", step.Run, step.Ordinal)
	return err
}
