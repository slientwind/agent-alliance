package runtime

import (
	"alliance/internal/database"
	"alliance/internal/knowledge"
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
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
		if _, err := a.Catalog.DB.Exec(ctx, "delete from run_draft_fragments where created_at<=now()-interval '1 hour'"); err != nil && ctx.Err() == nil {
			slog.Warn("expired text preview cleanup pending")
		}
		if _, err := a.Catalog.DB.Exec(ctx, "delete from project_session_events where created_at<=now()-interval '1 hour'"); err != nil && ctx.Err() == nil {
			slog.Warn("expired conversation event cleanup pending")
		}
		if err := a.retireWorkingPayloads(ctx, 128); err != nil && ctx.Err() == nil {
			slog.Warn("invalidated working payload cleanup pending")
		}
		_, err := a.Catalog.DB.Exec(ctx, `delete from trace_events e using runs r where r.id=e.run_id and r.trace_expires_at <= now()`)
		if err == nil {
			_, err = a.Catalog.DB.Exec(ctx, "delete from trace_grants g using runs r where r.id=g.run_id and (g.expires_at<=now() or r.trace_expires_at<=now())")
		}
		if err == nil {
			_, err = a.Catalog.DB.Exec(ctx, "delete from telemetry_outbox o using runs r where r.id=o.run_id and r.trace_expires_at <= now()")
		}
		if err == nil {
			_, err = a.Catalog.DB.Exec(ctx, "delete from genai_telemetry_outbox o using runs r where r.id=o.run_id and r.trace_expires_at <= now()")
		}
		if err == nil {
			_, err = a.Catalog.DB.Exec(ctx, `update external_agent_receipts e set trace_url=null from runs r
				where r.id=e.run_id and r.trace_expires_at<=now() and e.trace_url is not null`)
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

func (a Activities) retireWorkingPayloads(ctx context.Context, batchSize int) error {
	for {
		var retired int
		err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
			var acquired bool
			// One cleanup writer per database. Busy runs can still finish their
			// own transactions without waiting for a sweep of other runs.
			err := tx.QueryRow(ctx, "select pg_try_advisory_xact_lock(hashtextextended('alliance.retire_working_payloads',0))").Scan(&acquired)
			if err != nil || !acquired {
				return err
			}
			return tx.QueryRow(ctx, "select retire_invalid_working_payloads($1)", batchSize).Scan(&retired)
		})
		if err != nil || retired < batchSize {
			return err
		}
	}
}
