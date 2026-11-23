package database

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io/fs"
)

//go:embed schema/*.sql
var migrations embed.FS

func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	err = WithTx(ctx, db, func(tx pgx.Tx) error {
		if err := RequireServingDatabase(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(17952101)"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "create table if not exists schema_versions (name text primary key, checksum text not null, applied_at timestamptz not null default now())"); err != nil {
			return err
		}
		files, err := fs.Glob(migrations, "schema/*.sql")
		if err != nil {
			return err
		}
		for _, file := range files {
			body, err := migrations.ReadFile(file)
			if err != nil {
				return err
			}
			checksum := fmt.Sprintf("%x", sha256.Sum256(body))
			var current string
			err = tx.QueryRow(ctx, "select checksum from schema_versions where name=$1", file).Scan(&current)
			if err == nil {
				if current != checksum {
					return fmt.Errorf("migration changed: %s", file)
				}
				continue
			}
			if err != pgx.ErrNoRows {
				return err
			}
			if _, err = tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("%s: %w", file, err)
			}
			if _, err = tx.Exec(ctx, "insert into schema_versions(name,checksum) values($1,$2)", file, checksum); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func WithTx(ctx context.Context, db *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, db, fn)
}
func Audit(ctx context.Context, tx pgx.Tx, actor, action, subject string, detail any) error {
	_, err := tx.Exec(ctx, "insert into audit_events(actor,action,subject,detail) values($1,$2,$3,$4)", actor, action, subject, detail)
	return err
}
