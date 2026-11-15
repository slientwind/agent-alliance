package main

import (
	"context"
	"database/sql"
	"time"
)

type auditIntentKey struct{}
type auditIntent struct {
	Principal        Principal
	Action, Resource string
	Recorded         bool
}

func auditTransaction(ctx context.Context, tx *sql.Tx) error {
	intent, _ := ctx.Value(auditIntentKey{}).(*auditIntent)
	if intent == nil || intent.Recorded {
		return nil
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO alliance_audit(tenant_id,actor_id,action,resource) VALUES($1,$2,$3,$4)`, intent.Principal.TenantID, intent.Principal.UserID, intent.Action, intent.Resource)
	if e == nil {
		intent.Recorded = true
	}
	return e
}
func (s *Store) updateContext(parent context.Context, fn func(*State) error) error {
	if s.db == nil {
		return s.update(fn)
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	st, e := s.lockState(ctx, tx)
	if e != nil {
		return e
	}
	if e = fn(&st); e != nil {
		return e
	}
	if e = s.writeState(ctx, tx, st); e != nil {
		return e
	}
	if e = auditTransaction(ctx, tx); e != nil {
		return e
	}
	return tx.Commit()
}
