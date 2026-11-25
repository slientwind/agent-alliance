package database

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

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
		return migrate(ctx, tx)
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
