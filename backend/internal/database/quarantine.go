package database

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

func RequireServingDatabase(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var held bool
	err := q.QueryRow(ctx, `select starts_with(current_database(),'alliance_restore_') or
 coalesce(current_setting('alliance.restore_quarantined',true),'off')='on' or
 to_regclass('public.alliance_restore_quarantine') is not null`).Scan(&held)
	if err != nil {
		return err
	}
	if held {
		return errors.New("恢复副本处于隔离状态，禁止启动服务或执行任务")
	}
	return nil
}
