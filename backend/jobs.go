package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type JobPayload struct {
	Nodes     []Node              `json:"nodes"`
	Agents    map[string]Agent    `json:"agents"`
	Providers map[string]Provider `json:"providers"`
	Secrets   map[string]string   `json:"secrets"`
}
type Lease struct {
	ID, TenantID, RunID, Owner string
	Fence                      int64
	Attempt                    int
	Deadline                   time.Time
	Payload                    JobPayload
}

var errLeaseLost = errors.New("worker lease lost")

func (s *Server) enqueue(r *http.Request, run Run, nodes []Node, agents map[string]Agent, st State) (Run, error) {
	ctx := r.Context()
	if p, ok := ctx.Value(principalKey{}).(Principal); ok {
		run.ActorID = p.UserID
	}
	tx, e := s.store.db.BeginTx(ctx, nil)
	if e != nil {
		return Run{}, e
	}
	defer tx.Rollback()
	current, e := s.store.lockState(ctx, tx)
	if e != nil {
		return Run{}, e
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > 128 {
		return Run{}, bad("Idempotency-Key 最长 128 字节")
	}
	fingerprint := hashToken(r.URL.Path + "\x00" + run.Input)
	if key != "" {
		var oldFingerprint, oldRun string
		e = tx.QueryRowContext(ctx, `SELECT fingerprint,run_id FROM alliance_idempotency WHERE tenant_id=$1 AND key=$2`, s.store.tenantID, key).Scan(&oldFingerprint, &oldRun)
		if e == nil {
			if oldFingerprint != fingerprint {
				return Run{}, conflict("幂等键已用于不同请求")
			}
			old, ok := current.Runs[oldRun]
			if !ok {
				return Run{}, fmt.Errorf("idempotency reference missing")
			}
			return old, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return Run{}, e
		}
	}
	active := 0
	for _, r := range current.Runs {
		if !terminal(r.Status) {
			active++
		}
	}
	if active >= 100 {
		return Run{}, apiError{429, "当前工作区运行队列已达上限"}
	}
	payload := JobPayload{Nodes: nodes, Agents: agents, Providers: map[string]Provider{}, Secrets: map[string]string{}}
	for aid, a := range agents {
		if _, exists := current.Agents[aid]; !exists || current.Agents[aid].Status != "active" {
			return Run{}, conflict("Agent 已停用或删除")
		}
		if a.Kind == "managed" {
			p, ok := st.Providers[a.ProviderID]
			if !ok {
				return Run{}, conflict("模型配置不存在")
			}
			payload.Providers[p.ID] = p
		}
		if secret := st.Secrets[aid]; secret != "" {
			payload.Secrets[aid] = secret
		}
	}
	deadline := now().Add(s.timeout)
	run.Deadline = &deadline
	run.Events = []RunEvent{{Time: now(), Type: "queued", Message: "任务已持久化，等待 worker 领取"}}
	if info, ok := ctx.Value(a2aSubmissionKey{}).(*A2AInfo); ok {
		if info.PriorRunID != "" {
			for _, old := range current.Runs {
				if old.A2A != nil && old.A2A.TaskID == info.TaskID && old.ID != info.PriorRunID && old.StartedAt.After(current.Runs[info.PriorRunID].StartedAt) {
					return Run{}, conflict("任务已被另一条消息更新，请刷新")
				}
			}
		}
		run.A2A = info
	}
	current.Runs[run.ID] = run
	if e = s.store.writeState(ctx, tx, current); e != nil {
		return Run{}, e
	}
	b, e := json.Marshal(payload)
	if e != nil {
		return Run{}, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_jobs(id,tenant_id,run_id,payload,deadline) VALUES($1,$2,$3,$4,$5)`, id("job_"), s.store.tenantID, run.ID, b, deadline); e != nil {
		return Run{}, e
	}
	if key != "" {
		if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_idempotency(tenant_id,key,fingerprint,run_id) VALUES($1,$2,$3,$4)`, s.store.tenantID, key, fingerprint, run.ID); e != nil {
			return Run{}, e
		}
	}
	if e = auditTransaction(ctx, tx); e != nil {
		return Run{}, e
	}
	return run, tx.Commit()
}
func (s *Server) cancelDurable(ctx context.Context, runID string) error {
	tx, e := s.store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var jobID string
	e = tx.QueryRowContext(ctx, `SELECT id FROM alliance_jobs WHERE tenant_id=$1 AND run_id=$2 FOR UPDATE`, s.store.tenantID, runID).Scan(&jobID)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	st, e := s.store.lockState(ctx, tx)
	if e != nil {
		return e
	}
	run, ok := st.Runs[runID]
	if !ok {
		return missing()
	}
	if terminal(run.Status) {
		return conflict("运行已结束")
	}
	t := now()
	run.Status = "cancelled"
	run.Error = "用户取消运行"
	run.EndedAt = &t
	run.DurationMS = t.Sub(run.StartedAt).Milliseconds()
	run.Events = append(run.Events, RunEvent{Time: t, Type: "cancelled", Message: "取消已持久化，将通知执行 worker", Attempt: run.Attempt})
	for i := range run.Spans {
		if !terminal(run.Spans[i].Status) {
			run.Spans[i].Status = "cancelled"
			run.Spans[i].Error = run.Error
			run.Spans[i].EndedAt = &t
			run.Spans[i].DurationMS = t.Sub(run.Spans[i].StartedAt).Milliseconds()
		}
	}
	st.Runs[runID] = run
	if e = s.store.writeState(ctx, tx, st); e != nil {
		return e
	}
	if jobID != "" {
		if _, e = tx.ExecContext(ctx, `UPDATE alliance_jobs SET cancel_requested=true,status=CASE WHEN status='queued' THEN 'cancelled' ELSE status END,updated_at=now() WHERE id=$1`, jobID); e != nil {
			return e
		}
	}
	if e = auditTransaction(ctx, tx); e != nil {
		return e
	}
	return tx.Commit()
}
func claimJob(ctx context.Context, db *sql.DB, owner string, leaseDuration time.Duration) (*Lease, error) {
	row := db.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM alliance_jobs WHERE (status='queued' OR (status='running' AND lease_until<now())) AND available_at<=now() AND deadline>now() AND attempt<max_attempts AND NOT cancel_requested ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE alliance_jobs j SET status='running',owner=$1,fence=fence+1,attempt=attempt+1,lease_until=now()+($2*interval '1 millisecond'),updated_at=now() FROM candidate c WHERE j.id=c.id RETURNING j.id,j.tenant_id,j.run_id,j.fence,j.attempt,j.deadline,j.payload`, owner, leaseDuration.Milliseconds())
	var l Lease
	var raw []byte
	if e := row.Scan(&l.ID, &l.TenantID, &l.RunID, &l.Fence, &l.Attempt, &l.Deadline, &raw); errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	} else if e != nil {
		return nil, e
	}
	l.Owner = owner
	if e := json.Unmarshal(raw, &l.Payload); e != nil {
		return nil, e
	}
	return &l, nil
}
func (l *Lease) update(ctx context.Context, store *Store, fn func(*Run) error, jobStatus string) error {
	tx, e := store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var valid bool
	e = tx.QueryRowContext(ctx, `SELECT owner=$2 AND fence=$3 AND lease_until>now() AND status='running' AND NOT cancel_requested FROM alliance_jobs WHERE id=$1 FOR UPDATE`, l.ID, l.Owner, l.Fence).Scan(&valid)
	if e != nil {
		return e
	}
	if !valid {
		return errLeaseLost
	}
	st, e := store.lockState(ctx, tx)
	if e != nil {
		return e
	}
	run, ok := st.Runs[l.RunID]
	if !ok {
		return missing()
	}
	if terminal(run.Status) {
		return errLeaseLost
	}
	if e = fn(&run); e != nil {
		return e
	}
	st.Runs[l.RunID] = run
	if e = store.writeState(ctx, tx, st); e != nil {
		return e
	}
	if jobStatus != "" {
		if _, e = tx.ExecContext(ctx, `UPDATE alliance_jobs SET status=$2,lease_until=NULL,updated_at=now() WHERE id=$1`, l.ID, jobStatus); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (l *Lease) heartbeat(ctx context.Context, db *sql.DB, duration time.Duration) (bool, error) {
	var cancelRequested bool
	e := db.QueryRowContext(ctx, `UPDATE alliance_jobs SET lease_until=now()+($4*interval '1 millisecond'),updated_at=now() WHERE id=$1 AND owner=$2 AND fence=$3 AND lease_until>now() AND status='running' RETURNING cancel_requested`, l.ID, l.Owner, l.Fence, duration.Milliseconds()).Scan(&cancelRequested)
	if errors.Is(e, sql.ErrNoRows) {
		return false, errLeaseLost
	}
	return cancelRequested, e
}
