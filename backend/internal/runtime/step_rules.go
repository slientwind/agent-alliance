package runtime

import (
	"context"
	"errors"

	"alliance/internal/memory"
	"alliance/internal/projects"

	"github.com/jackc/pgx/v5"
)

func parkStepRule(ctx context.Context, tx pgx.Tx, step Step, project, rule string, revision int, rebuild bool) error {
	tag, err := tx.Exec(ctx, `insert into step_rule_waits(run_id,ordinal,rule_id,rule_revision,rebuild_required)
        values($1,$2,$3,$4,$5)
        on conflict(run_id,ordinal) do update
        set rule_id=excluded.rule_id,rule_revision=excluded.rule_revision,wait_revision=excluded.wait_revision,rebuild_required=excluded.rebuild_required
        where (step_rule_waits.rule_id,step_rule_waits.rule_revision,step_rule_waits.rebuild_required)
           is distinct from (excluded.rule_id,excluded.rule_revision,excluded.rebuild_required)`, step.Run, step.Ordinal, rule, revision, rebuild)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint','step_rule_unavailable')", step.Run, step.Ordinal); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "update runs set status='running' where id=$1 and status='queued'", step.Run); err != nil {
		return err
	}
	if err := projects.TouchRun(ctx, tx, step.Run, project, "running"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "select record_service_event($1,'step','step_rule_unavailable',$2)", step.Run, step.Ordinal)
	return err
}

func parkInputRule(ctx context.Context, tx pgx.Tx, step Step, project string, err error) (bool, error) {
	var rule *memory.RuleUnavailable
	if !errors.As(err, &rule) {
		return false, nil
	}
	var running bool
	if err := tx.QueryRow(ctx, "select status in ('queued','running') from runs where id=$1", step.Run).Scan(&running); err != nil || !running {
		return false, err
	}
	return true, parkStepRule(ctx, tx, step, project, rule.ID, rule.Revision, false)
}

// An existing result or prerequisite cannot be revalidated by selecting a new
// rule. Leave its receipt in the ledger and wait only on the affected branch.
func parkRevokedWorkingRule(ctx context.Context, tx pgx.Tx, step Step, project string, attempt int) (bool, error) {
	var rule string
	var revision int
	var scoped bool
	err := tx.QueryRow(ctx, `select d.rule_id,d.rule_revision,d.scoped
        from (
          select $1::uuid origin_run,$2::integer ordinal,$3::integer attempt_no where $3>0
          union all
          select i.origin_run,e.depends_on,i.attempt_no from runs r
          join node_edges e on (e.project_id,e.plan_version)=(r.project_id,r.plan_version)
          join run_observations i on (i.run_id,i.ordinal)=(r.id,e.depends_on)
          where r.id=$1 and e.ordinal=$2 and $3=0
        ) a cross join lateral invalid_working_step_rules(a.origin_run,a.ordinal,a.attempt_no) d
        where exists(select from runs where id=$1 and status='running')
        order by d.scoped,d.rule_id limit 1`, step.Run, step.Ordinal, attempt).Scan(&rule, &revision, &scoped)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || !scoped {
		return false, err
	}
	return true, parkStepRule(ctx, tx, step, project, rule, revision, true)
}
