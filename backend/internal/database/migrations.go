package database

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed schema/*.sql
var migrations embed.FS

// The PostgreSQL 16 baseline captures an empty database after migration 157,
// including its initial records. The archive header pins the original inputs.
//
//go:embed baseline/157.sql.gz
var baselineArchive []byte

const baselineThrough = "schema/157_upstream_review_evidence.sql"
const createVersions = `create table if not exists schema_versions (
 name text primary key, checksum text not null, applied_at timestamptz not null default now())`

type migration struct{ name, checksum, body string }

func migrationPlan() ([]migration, error) {
	files, err := fs.Glob(migrations, "schema/*.sql")
	if err != nil {
		return nil, err
	}
	plan := make([]migration, 0, len(files))
	for _, name := range files {
		body, err := migrations.ReadFile(name)
		if err != nil {
			return nil, err
		}
		plan = append(plan, migration{name, fmt.Sprintf("%x", sha256.Sum256(body)), string(body)})
	}
	return plan, nil
}

func migrate(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, createVersions); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, "select name,checksum from schema_versions")
	if err != nil {
		return err
	}
	type version struct{ Name, Checksum string }
	applied, err := pgx.CollectRows(rows, pgx.RowToStructByPos[version])
	if err != nil {
		return err
	}
	checksums := make(map[string]string, len(applied))
	for _, entry := range applied {
		checksums[entry.Name] = entry.Checksum
	}
	plan, err := migrationPlan()
	if err != nil {
		return err
	}
	if len(checksums) == 0 {
		var schema string
		if err := tx.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
			return err
		}
		if schema == "public" {
			end := 0
			for i, item := range plan {
				if item.name == baselineThrough {
					end = i + 1
					break
				}
			}
			if end == 0 {
				return fmt.Errorf("missing migration baseline %s", baselineThrough)
			}
			if err := applyBaseline(ctx, tx, plan[:end]); err != nil {
				return err
			}
			plan = plan[end:]
		}
	}
	for _, item := range plan {
		if previous, ok := checksums[item.name]; ok {
			if previous != item.checksum {
				return fmt.Errorf("migration changed: %s", item.name)
			}
			continue
		}
		if err := applyMigration(ctx, tx, item); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(ctx context.Context, tx pgx.Tx, item migration) error {
	if _, err := tx.Exec(ctx, item.body); err != nil {
		return fmt.Errorf("%s: %w", item.name, err)
	}
	_, err := tx.Exec(ctx, "insert into schema_versions(name,checksum) values($1,$2)", item.name, item.checksum)
	return err
}

func applyBaseline(ctx context.Context, tx pgx.Tx, entries []migration) error {
	script, err := baselineSQL(entries)
	if err != nil {
		return err
	}
	var searchPath, functionChecks string
	if err := tx.QueryRow(ctx, "select current_setting('search_path'), current_setting('check_function_bodies')").
		Scan(&searchPath, &functionChecks); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, script); err != nil {
		return fmt.Errorf("schema baseline through %s: %w", baselineThrough, err)
	}
	if _, err := tx.Exec(ctx, "select set_config('search_path',$1,true),set_config('check_function_bodies',$2,true)", searchPath, functionChecks); err != nil {
		return err
	}
	names, checksums := make([]string, len(entries)), make([]string, len(entries))
	for i, item := range entries {
		names[i], checksums[i] = item.name, item.checksum
	}
	_, err = tx.Exec(ctx, `insert into schema_versions(name,checksum) select * from unnest($1::text[],$2::text[])`, names, checksums)
	return err
}

func baselineSQL(entries []migration) (string, error) {
	archive, err := gzip.NewReader(bytes.NewReader(baselineArchive))
	if err != nil {
		return "", err
	}
	defer archive.Close()
	data, err := io.ReadAll(archive)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, item := range entries {
		_, _ = fmt.Fprintf(digest, "%s\x00%s\n", item.name, item.checksum)
	}
	script := string(data)
	if !strings.HasPrefix(script, fmt.Sprintf("-- Historical migrations SHA-256 %x\n", digest.Sum(nil))) {
		return "", fmt.Errorf("migration checksums do not match the archive through %s", baselineThrough)
	}
	return script, nil
}
