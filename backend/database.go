package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS alliance_users (
 id text PRIMARY KEY, email text NOT NULL UNIQUE CHECK(email=lower(email)), name text NOT NULL,
 password_hash text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS alliance_tenants (
 id text PRIMARY KEY, name text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS alliance_memberships (
 tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 user_id text NOT NULL REFERENCES alliance_users(id) ON DELETE CASCADE,
 role text NOT NULL CHECK(role IN ('admin','editor','operator','viewer')),
 PRIMARY KEY(tenant_id,user_id)
);
CREATE TABLE IF NOT EXISTS alliance_states (
 tenant_id text PRIMARY KEY REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 data jsonb NOT NULL, revision bigint NOT NULL DEFAULT 0, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS alliance_sessions (
 hash text PRIMARY KEY, user_id text NOT NULL REFERENCES alliance_users(id) ON DELETE CASCADE,
 csrf text NOT NULL, expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS alliance_sessions_expiry ON alliance_sessions(expires_at);
CREATE TABLE IF NOT EXISTS alliance_api_keys (
 id text PRIMARY KEY, tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 name text NOT NULL, hash text NOT NULL UNIQUE, prefix text NOT NULL,
 role text NOT NULL CHECK(role IN ('admin','editor','operator','viewer')),
 created_by text NOT NULL REFERENCES alliance_users(id), created_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz
);
CREATE TABLE IF NOT EXISTS alliance_auth_limits (key text PRIMARY KEY, count integer NOT NULL, reset_at timestamptz NOT NULL);
CREATE TABLE IF NOT EXISTS alliance_settings (name text PRIMARY KEY, value text NOT NULL);
CREATE TABLE IF NOT EXISTS alliance_audit (
 id bigserial PRIMARY KEY, tenant_id text REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 actor_id text NOT NULL, action text NOT NULL, resource text NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS alliance_audit_tenant_time ON alliance_audit(tenant_id,created_at DESC);
CREATE TABLE IF NOT EXISTS alliance_jobs (
 id text PRIMARY KEY, tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 run_id text NOT NULL, payload jsonb NOT NULL, status text NOT NULL DEFAULT 'queued',
 owner text, fence bigint NOT NULL DEFAULT 0, lease_until timestamptz,
 attempt integer NOT NULL DEFAULT 0, max_attempts integer NOT NULL DEFAULT 5,
 cancel_requested boolean NOT NULL DEFAULT false, available_at timestamptz NOT NULL DEFAULT now(),
 deadline timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tenant_id,run_id)
);
CREATE INDEX IF NOT EXISTS alliance_jobs_claim ON alliance_jobs(status,available_at,lease_until);
CREATE TABLE IF NOT EXISTS alliance_idempotency (
 tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 key text NOT NULL, fingerprint text NOT NULL, run_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(tenant_id,key)
);
CREATE TABLE IF NOT EXISTS alliance_a2a_push (
 tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 target_id text NOT NULL, task_id text NOT NULL, id text NOT NULL, config text NOT NULL,
 PRIMARY KEY(tenant_id,task_id,id)
);
CREATE TABLE IF NOT EXISTS alliance_integrations (
 tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 kind text NOT NULL, config jsonb NOT NULL, PRIMARY KEY(tenant_id,kind)
);
CREATE TABLE IF NOT EXISTS alliance_outbox (
 id text PRIMARY KEY, tenant_id text NOT NULL REFERENCES alliance_tenants(id) ON DELETE CASCADE,
 kind text NOT NULL, payload jsonb NOT NULL, status text NOT NULL DEFAULT 'pending',
 attempts integer NOT NULL DEFAULT 0, available_at timestamptz NOT NULL DEFAULT now(), last_error text NOT NULL DEFAULT '',
 owner text, lease_until timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS alliance_outbox_pending ON alliance_outbox(status,available_at);
`

func openDatabase(dsn string, key []byte) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(24)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		db.Close()
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(7614430201)"); err == nil {
		_, err = tx.ExecContext(ctx, schemaSQL)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	store, err := newDatabaseStore(db, "", key)
	if err != nil {
		return nil, err
	}
	encrypted, err := store.encrypt("alliance-master-key-v1")
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO alliance_settings(name,value) VALUES('master_key_check',$1) ON CONFLICT DO NOTHING`, encrypted); err != nil {
		return nil, err
	}
	var check string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM alliance_settings WHERE name='master_key_check'`).Scan(&check); err != nil {
		return nil, err
	}
	plaintext, err := store.decrypt(check)
	if err != nil || plaintext != "alliance-master-key-v1" {
		return nil, fmt.Errorf("master key does not match this database")
	}
	if err = tx.Commit(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
func newDatabaseStore(db *sql.DB, tenantID string, key []byte) (*Store, error) {
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	return &Store{db: db, tenantID: tenantID, aead: aead}, nil
}
func (s *Store) readState(ctx context.Context) (State, error) {
	if s.db == nil {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return clone(s.state), nil
	}
	var b []byte
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM alliance_states WHERE tenant_id=$1`, s.tenantID).Scan(&b); err != nil {
		return State{}, fmt.Errorf("读取工作区失败: %w", err)
	}
	st := newState()
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, err
	}
	return st, nil
}
func (s *Store) snapshotE() (State, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.readState(ctx)
}
func (s *Store) updateSQL(fn func(*State) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := s.lockState(ctx, tx)
	if err != nil {
		return err
	}
	if err = fn(&st); err != nil {
		return err
	}
	if err = s.writeState(ctx, tx, st); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) lockState(ctx context.Context, tx *sql.Tx) (State, error) {
	var b []byte
	err := tx.QueryRowContext(ctx, `SELECT data FROM alliance_states WHERE tenant_id=$1 FOR UPDATE`, s.tenantID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, missing()
	}
	if err != nil {
		return State{}, err
	}
	st := newState()
	err = json.Unmarshal(b, &st)
	return st, err
}
func (s *Store) writeState(ctx context.Context, tx *sql.Tx, st State) error {
	if e := s.enqueueA2APush(ctx, tx, st); e != nil {
		return e
	}
	if e := s.enqueueObservations(ctx, tx, st); e != nil {
		return e
	}
	b, e := json.Marshal(st)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE alliance_states SET data=$2,revision=revision+1,updated_at=now() WHERE tenant_id=$1`, s.tenantID, b)
	return e
}
func createWorkspace(ctx context.Context, tx *sql.Tx, tenantID, name, userID string) error {
	if _, e := tx.ExecContext(ctx, `INSERT INTO alliance_tenants(id,name) VALUES($1,$2)`, tenantID, name); e != nil {
		return e
	}
	if _, e := tx.ExecContext(ctx, `INSERT INTO alliance_memberships(tenant_id,user_id,role) VALUES($1,$2,'admin')`, tenantID, userID); e != nil {
		return e
	}
	st := newState()
	p := Provider{ID: id("mdl_"), Name: "Mock 测试模型", Type: "mock", Model: "mock-v1"}
	st.Providers[p.ID] = p
	b, _ := json.Marshal(st)
	_, e := tx.ExecContext(ctx, `INSERT INTO alliance_states(tenant_id,data) VALUES($1,$2)`, tenantID, b)
	return e
}
