package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

func (g *Gateway) importLegacy(ctx context.Context, path string) error {
	uid, e := g.ensureDemo(ctx)
	if e != nil {
		return e
	}
	spaces, e := g.workspaces(ctx, uid)
	if e != nil {
		return e
	}
	if len(spaces) != 1 {
		return fmt.Errorf("legacy import requires a single demo workspace")
	}
	tenantID := spaces[0].ID
	tx, e := g.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7614430203)`); e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM alliance_settings WHERE name=$1)`, "legacy-import:"+tenantID).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return nil
	}
	raw, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	st := newState()
	if e = json.Unmarshal(raw, &st); e != nil {
		return e
	}
	if st.Secrets == nil || st.Agents == nil {
		return fmt.Errorf("invalid legacy file")
	}
	migrateAgentCredentials(&st)
	legacy, e := newDatabaseStore(nil, "", g.masterKey)
	if e != nil {
		return e
	}
	target, e := newDatabaseStore(g.db, tenantID, g.masterKey)
	if e != nil {
		return e
	}
	for resource, encrypted := range st.Secrets {
		plain, e := legacy.decrypt(encrypted)
		if e != nil {
			return fmt.Errorf("legacy credential decryption failed; preserve original master key")
		}
		st.Secrets[resource], e = target.encrypt(plain)
		if e != nil {
			return e
		}
	}
	for id, r := range st.Runs {
		if !terminal(r.Status) {
			t := now()
			r.Status = "failed"
			r.Error = "从旧单进程版本迁移，任务未完成"
			r.EndedAt = &t
			for i := range r.Spans {
				if !terminal(r.Spans[i].Status) {
					r.Spans[i].Status = "failed"
					r.Spans[i].Error = r.Error
					r.Spans[i].EndedAt = &t
				}
			}
			st.Runs[id] = r
		}
	}
	current, e := target.lockState(ctx, tx)
	if e != nil {
		return e
	}
	if len(current.Agents) > 0 || len(current.Services) > 0 || len(current.Runs) > 0 || len(current.Providers) > 1 {
		return fmt.Errorf("refusing to overwrite nonempty demo workspace")
	}
	if e = target.writeState(ctx, tx, st); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_settings(name,value) VALUES($1,'completed')`, "legacy-import:"+tenantID); e != nil {
		return e
	}
	return tx.Commit()
}
