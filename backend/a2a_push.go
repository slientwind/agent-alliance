package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/a2aproject/a2a-go-v1/a2a"
	"github.com/a2aproject/a2a-go-v1/a2acompat/a2av0"
	"github.com/a2aproject/a2a-go-v1/a2asrv"
	"io"
	"net/http"
	"strings"
	"time"
)

func (h *a2aEndpoint) CreateTaskPushConfig(ctx context.Context, q *a2a.CreateTaskPushConfigRequest) (*a2a.TaskPushConfig, error) {
	if e := h.authorize(q.Tenant, true); e != nil {
		return nil, e
	}
	if _, e := h.getRun(ctx, q.TaskID); e != nil {
		return nil, e
	}
	c := q.Config
	if e := validateURL(c.URL); e != nil {
		return nil, a2a.ErrInvalidParams
	}
	if len(c.ID) > 128 || len(c.Token) > 4096 {
		return nil, a2a.ErrInvalidParams
	}
	if c.Auth != nil && (!strings.EqualFold(c.Auth.Scheme, "Bearer") && !strings.EqualFold(c.Auth.Scheme, "Basic")) {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "回调认证支持 Basic 或 Bearer")
	}
	if c.ID == "" {
		c.ID = id("push_")
	}
	b, _ := json.Marshal(c)
	var stored map[string]any
	_ = json.Unmarshal(b, &stored)
	if call, ok := a2asrv.CallContextFrom(ctx); ok {
		versions, _ := call.ServiceParams().Get("A2A-Version")
		stored["_legacy"] = len(versions) == 0 || strings.HasPrefix(versions[0], "0.3")
	}
	b, _ = json.Marshal(stored)
	enc, e := h.server.store.encrypt(string(b))
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	_, e = h.server.store.db.ExecContext(ctx, `INSERT INTO alliance_a2a_push(tenant_id,target_id,task_id,id,config) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,task_id,id) DO UPDATE SET config=excluded.config`, h.principal.TenantID, h.target, q.TaskID, c.ID, enc)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	// A task may have completed before the callback was registered; enqueue its current snapshot.
	r, e := h.getRun(ctx, q.TaskID)
	if e == nil {
		e = h.server.store.update(func(st *State) error {
			current, ok := st.Runs[r.ID]
			if !ok {
				return missing()
			}
			st.Runs[r.ID] = current
			return nil
		})
	}
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	return &a2a.TaskPushConfig{TaskID: q.TaskID, Config: c}, nil
}
func (h *a2aEndpoint) GetTaskPushConfig(ctx context.Context, q *a2a.GetTaskPushConfigRequest) (*a2a.TaskPushConfig, error) {
	if e := h.authorize(q.Tenant, false); e != nil {
		return nil, e
	}
	if _, e := h.getRun(ctx, q.TaskID); e != nil {
		return nil, e
	}
	var enc string
	e := h.server.store.db.QueryRowContext(ctx, `SELECT config FROM alliance_a2a_push WHERE tenant_id=$1 AND task_id=$2 AND id=$3 AND target_id=$4`, h.principal.TenantID, q.TaskID, q.ID, h.target).Scan(&enc)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, a2a.ErrTaskNotFound
	}
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	raw, e := h.server.store.decrypt(enc)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	var c a2a.PushConfig
	if json.Unmarshal([]byte(raw), &c) != nil {
		return nil, a2a.ErrInternalError
	}
	return &a2a.TaskPushConfig{TaskID: q.TaskID, Config: c}, nil
}
func (h *a2aEndpoint) ListTaskPushConfigs(ctx context.Context, q *a2a.ListTaskPushConfigRequest) ([]*a2a.TaskPushConfig, error) {
	if e := h.authorize(q.Tenant, false); e != nil {
		return nil, e
	}
	if _, e := h.getRun(ctx, q.TaskID); e != nil {
		return nil, e
	}
	rows, e := h.server.store.db.QueryContext(ctx, `SELECT id FROM alliance_a2a_push WHERE tenant_id=$1 AND task_id=$2 AND target_id=$3 ORDER BY id`, h.principal.TenantID, q.TaskID, h.target)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, a2a.ErrInternalError
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	out := []*a2a.TaskPushConfig{}
	for _, id := range ids {
		v, e := h.GetTaskPushConfig(ctx, &a2a.GetTaskPushConfigRequest{TaskID: q.TaskID, ID: id})
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}
func (h *a2aEndpoint) DeleteTaskPushConfig(ctx context.Context, q *a2a.DeleteTaskPushConfigRequest) error {
	if e := h.authorize(q.Tenant, true); e != nil {
		return e
	}
	if _, e := h.getRun(ctx, q.TaskID); e != nil {
		return e
	}
	_, e := h.server.store.db.ExecContext(ctx, `DELETE FROM alliance_a2a_push WHERE tenant_id=$1 AND task_id=$2 AND id=$3 AND target_id=$4`, h.principal.TenantID, q.TaskID, q.ID, h.target)
	if e != nil {
		return a2a.ErrInternalError
	}
	return nil
}

type pushEnvelope struct {
	Config   string    `json:"config"`
	ConfigID string    `json:"configId"`
	Task     *a2a.Task `json:"task"`
}

func (s *Store) enqueueA2APush(ctx context.Context, tx *sql.Tx, st State) error {
	rows, e := tx.QueryContext(ctx, `SELECT task_id,id,config FROM alliance_a2a_push WHERE tenant_id=$1`, s.tenantID)
	if e != nil {
		return e
	}
	type config struct{ task, id, enc string }
	var configs []config
	for rows.Next() {
		var c config
		if e = rows.Scan(&c.task, &c.id, &c.enc); e != nil {
			rows.Close()
			return e
		}
		configs = append(configs, c)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	latest := map[a2a.TaskID]Run{}
	for _, run := range st.Runs {
		if run.A2A != nil {
			old, ok := latest[run.A2A.TaskID]
			if !ok || run.StartedAt.After(old.StartedAt) {
				latest[run.A2A.TaskID] = run
			}
		}
	}
	for _, r := range latest {
		if r.A2A == nil {
			continue
		}
		for _, c := range configs {
			if c.task != string(r.A2A.TaskID) {
				continue
			}
			eventID := "push_" + stableHex(s.tenantID+":"+r.ID+":"+r.Status+":"+c.id, 16)
			b, _ := json.Marshal(pushEnvelope{c.enc, c.id, taskFromRun(r, nil)})
			if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_outbox(id,tenant_id,kind,payload) VALUES($1,$2,'a2a-push',$3) ON CONFLICT(id) DO NOTHING`, eventID, s.tenantID, b); e != nil {
				return e
			}
		}
	}
	return nil
}
func (w *Worker) exportPush(ctx context.Context) error {
	var event, tenant string
	var raw []byte
	var attempt int
	e := w.DB.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM alliance_outbox WHERE kind='a2a-push' AND (status='pending' OR (status='sending' AND lease_until<now())) AND available_at<=now() ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE alliance_outbox o SET status='sending',owner=$1,attempts=attempts+1,lease_until=now()+interval '60 seconds' FROM candidate c WHERE o.id=c.id RETURNING o.id,o.tenant_id,o.payload,o.attempts`, w.ID).Scan(&event, &tenant, &raw, &attempt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var payload pushEnvelope
	e = json.Unmarshal(raw, &payload)
	if e == nil {
		e = w.sendPush(ctx, event, tenant, payload)
	}
	status, last := "sent", ""
	if e != nil {
		status = "pending"
		last = "A2A 回调暂未送达"
		if attempt >= 10 {
			status = "failed"
		}
	}
	_, e = w.DB.ExecContext(ctx, `UPDATE alliance_outbox SET status=$4,last_error=$5,available_at=now()+($6*interval '1 second'),lease_until=NULL WHERE id=$1 AND owner=$2 AND attempts=$3 AND status='sending'`, event, w.ID, attempt, status, last, 1<<min(attempt, 10))
	return e
}
func (w *Worker) sendPush(ctx context.Context, event, tenant string, p pushEnvelope) error {
	var exists bool
	e := w.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM alliance_a2a_push WHERE tenant_id=$1 AND task_id=$2 AND id=$3)`, tenant, p.Task.ID, p.ConfigID).Scan(&exists)
	if e != nil {
		return e
	}
	if !exists {
		return nil
	}
	store, e := newDatabaseStore(w.DB, tenant, w.Key)
	if e != nil {
		return e
	}
	plain, e := store.decrypt(p.Config)
	if e != nil {
		return e
	}
	var c a2a.PushConfig
	if e = json.Unmarshal([]byte(plain), &c); e != nil {
		return e
	}
	var format struct {
		Legacy bool `json:"_legacy"`
	}
	_ = json.Unmarshal([]byte(plain), &format)
	var payload any = p.Task
	if format.Legacy {
		payload = a2av0.FromV1Task(p.Task)
	}
	body, _ := json.Marshal(payload)
	req, e := http.NewRequestWithContext(ctx, "POST", c.URL, bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", event)
	if c.Token != "" {
		req.Header.Set("X-A2A-Notification-Token", c.Token)
	}
	if c.Auth != nil {
		req.Header.Set("Authorization", c.Auth.Scheme+" "+c.Auth.Credentials)
	}
	client := httpClient(w.AllowPrivate)
	client.Timeout = 20 * time.Second
	defer client.CloseIdleConnections()
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 65536))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("push HTTP %d", res.StatusCode)
	}
	return nil
}
