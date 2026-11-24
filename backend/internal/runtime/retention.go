package runtime

import (
	"alliance/internal/knowledge"
	"context"
	"log/slog"
	"time"
)

func (a Activities) CleanRetention(ctx context.Context) {
	timer := time.NewTicker(time.Hour)
	defer timer.Stop()
	for {
		if err := knowledge.ConfiguredObjects().CleanSources(ctx, a.Catalog.DB); err != nil && ctx.Err() == nil {
			slog.Warn("source content cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, "delete from oidc_flows where expires_at<=now()"); err != nil && ctx.Err() == nil {
			slog.Warn("expired OIDC flow cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, "select purge_due_preferences()"); err != nil && ctx.Err() == nil {
			slog.Warn("preference retention cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, `delete from run_draft_fragments
            where created_at <= now()-interval '1 hour'
                or not run_working_set_usable(run_id,ordinal,attempt_no)`); err != nil && ctx.Err() == nil {
			slog.Warn("expired text preview cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, `update lookup_turns set decision=null,outcome='invalidated'
            where decision is not null and not run_working_set_usable(run_id,ordinal,attempt_no)`); err != nil && ctx.Err() == nil {
			slog.Warn("invalidated lookup cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, `update invocations i set result=null from run_working_sets s
            where (s.run_id,s.ordinal,s.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
              and i.status='completed' and i.result is not null
              and not run_working_set_usable(s.run_id,s.ordinal,s.attempt_no)
              and not exists(select from artifacts a where(a.run_id,a.ordinal)=(i.run_id,i.ordinal))
              and not exists(select from change_batches c where c.run_id=i.run_id and c.applied_version is not null)`); err != nil && ctx.Err() == nil {
			slog.Warn("invalidated pending results cleanup pending")
		}
		_, err := a.Catalog.DB.Exec(ctx, `delete from trace_events e using runs r where r.id=e.run_id and r.trace_expires_at <= now()`)
		if err == nil {
			_, err = a.Catalog.DB.Exec(ctx, "delete from trace_grants g using runs r where r.id=g.run_id and (g.expires_at<=now() or r.trace_expires_at<=now())")
		}
		if err == nil {
			_, err = a.Catalog.DB.Exec(ctx, "delete from telemetry_outbox o using runs r where r.id=o.run_id and r.trace_expires_at <= now()")
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("diagnostic retention cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, `update management_exports e set payload=null from management_grants g
             where g.id=e.grant_id and e.payload is not null and (e.expires_at<=now() or g.revoked_at is not null or not (case when e.cost_lines then g.financial_export else g.allow_export end) or (e.include_costs and not g.include_costs))`); err != nil && ctx.Err() == nil {
			slog.Warn("management export cleanup pending")
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
