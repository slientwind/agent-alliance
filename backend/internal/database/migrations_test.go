package database

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBaselineContainsTheOriginal157Migrations(t *testing.T) {
	plan, err := migrationPlan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) < 158 || plan[156].name != baselineThrough {
		t.Fatalf("migration ordering has changed before %s", baselineThrough)
	}
	for _, item := range plan[:157] {
		if len(item.checksum) != 64 || !strings.HasSuffix(strings.TrimSpace(item.body), ";") {
			t.Fatalf("baseline entry has no complete SQL boundary or checksum: %s", item.name)
		}
	}
	if _, err := baselineSQL(plan[:157]); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationBaselineMatchesIncrementalPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_MIGRATION_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_MIGRATION_TEST_DATABASE to an empty scratch pgvector database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var blank bool
	if err := pool.QueryRow(ctx, "select to_regclass('public.schema_versions') is null and to_regclass('public.projects') is null").Scan(&blank); err != nil || !blank {
		t.Fatalf("migration test requires an empty database: %v", err)
	}
	plan, err := migrationPlan()
	if err != nil {
		t.Fatal(err)
	}

	digest := func(tx pgx.Tx) string {
		t.Helper()
		var state string
		err := tx.QueryRow(ctx, `select jsonb_build_object(
		 'versions',(select jsonb_object_agg(name,checksum) from schema_versions),
		 'columns',(select jsonb_agg(jsonb_build_array(c.relname,a.attname,format_type(a.atttypid,a.atttypmod)) order by c.relname,a.attnum)
		  from pg_class c join pg_namespace n on n.oid=c.relnamespace join pg_attribute a on c.oid=a.attrelid
		  where n.nspname='public' and c.relkind in ('r','v','m') and a.attnum>0 and not a.attisdropped),
		 'relations',(select jsonb_object_agg(indexname,indexdef) from pg_indexes where schemaname='public'),
		 'constraints',(select jsonb_agg(def order by def) from (
		  select conname||':'||pg_get_constraintdef(c.oid) def from pg_constraint c join pg_namespace n on n.oid=c.connamespace
		  where n.nspname='public') available),
		 'modelTiers',(select jsonb_agg(tier order by tier) from model_tier_settings))::text`).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}

	var baseline string
	if err := testMigrationInTransaction(t, pool, func(tx pgx.Tx) error {
		start := time.Now()
		if err := migrate(ctx, tx); err != nil {
			return err
		}
		t.Logf("fresh schema with baseline: %s", time.Since(start))
		baseline = digest(tx)
		return migrate(ctx, tx)
	}); err != nil {
		t.Fatal(err)
	}
	if err := testMigrationInTransaction(t, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, createVersions); err != nil {
			return err
		}
		start := time.Now()
		for _, item := range plan[:157] {
			if err := applyMigration(ctx, tx, item); err != nil {
				return err
			}
		}
		if err := migrate(ctx, tx); err != nil {
			return err
		}
		t.Logf("fresh schema with original incremental migrations: %s", time.Since(start))
		if incremental := digest(tx); incremental != baseline {
			t.Fatal("fresh baseline and upgrade paths produced different schemas or checksums")
		}
		if _, err := tx.Exec(ctx, "update schema_versions set checksum='changed' where name=$1", baselineThrough); err != nil {
			return err
		}
		if err := migrate(ctx, tx); err == nil || !strings.Contains(err.Error(), "migration changed: "+baselineThrough) {
			t.Fatalf("existing historical checksums were not validated: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func testMigrationInTransaction(t *testing.T, pool *pgxpool.Pool, check func(pgx.Tx) error) error {
	t.Helper()
	tx, err := pool.Begin(t.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(t.Context())
	return check(tx)
}
