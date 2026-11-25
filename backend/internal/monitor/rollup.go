package monitor

import (
	"context"
	_ "embed"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed rollup.sql
var rollupQuery string

const rollupInterval = 5 * time.Minute

func ServeRollups(ctx context.Context, db *pgxpool.Pool) {
	ticker := time.NewTicker(rollupInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		work, cancel := context.WithTimeout(ctx, rollupInterval-time.Second)
		for work.Err() == nil {
			changed, err := rollupOnce(work, db)
			if err != nil && work.Err() == nil {
				slog.Warn("monitor rollup pending")
			}
			if err != nil || !changed {
				break
			}
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func rollupOnce(ctx context.Context, db interface {
	Begin(context.Context) (pgx.Tx, error)
}) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `select pg_try_advisory_xact_lock(17952108)`).Scan(&locked); err != nil || !locked {
			return err
		}
		rows, err := tx.Query(ctx, `select id,bucket_hour from monitor_rollup_dirty order by id limit 1024`)
		if err != nil {
			return err
		}
		var ids []int64
		var buckets []time.Time
		seen := make(map[time.Time]bool)
		for rows.Next() {
			var id int64
			var hour time.Time
			if err := rows.Scan(&id, &hour); err != nil {
				rows.Close()
				return err
			}
			if !seen[hour] {
				if len(buckets) == 24 {
					break
				}
				seen[hour] = true
				buckets = append(buckets, hour)
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		for _, query := range []string{
			`delete from monitor_agent_hourly_stats where bucket_hour=any($1)`,
			`delete from monitor_model_hourly_stats where bucket_hour=any($1)`, rollupQuery,
		} {
			if _, err := tx.Exec(ctx, query, buckets); err != nil {
				return err
			}
		}
		// Only consume events from the batch. An event inserted by a concurrent
		// transaction stays queued even if its identity sorts before this batch.
		if _, err := tx.Exec(ctx, `delete from monitor_rollup_dirty where id=any($1)`, ids); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed && err == nil, err
}
