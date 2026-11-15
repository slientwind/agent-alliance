package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/a2aproject/a2a-go-v1/a2a"
	"log"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	DB            *sql.DB
	Key           []byte
	ID            string
	AllowPrivate  bool
	LeaseDuration time.Duration
	PollInterval  time.Duration
	Concurrency   int
	Logf          func(string, ...any)
}

func newWorker(db *sql.DB, key []byte, workerID string, allowPrivate bool) *Worker {
	return &Worker{DB: db, Key: key, ID: workerID, AllowPrivate: allowPrivate, LeaseDuration: 15 * time.Second, PollInterval: 250 * time.Millisecond, Concurrency: 4, Logf: log.Printf}
}
func (w *Worker) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			if e := w.exportPush(ctx); e != nil && ctx.Err() == nil {
				w.Logf("push export failed: %v", e)
			}
			if e := w.exportObservation(ctx); e != nil && ctx.Err() == nil {
				w.Logf("observation export store failure: %v", e)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	for i := 0; i < w.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				lease, e := claimJob(ctx, w.DB, w.ID, w.LeaseDuration)
				if e != nil && ctx.Err() == nil {
					w.Logf("worker claim failed: %v", e)
				}
				if lease != nil {
					w.execute(ctx, lease)
					continue
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(w.PollInterval):
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := w.sweep(ctx); e != nil && ctx.Err() == nil {
					w.Logf("worker sweep failed: %v", e)
				}
			}
		}
	}()
	wg.Wait()
}
func (w *Worker) execute(workerCtx context.Context, l *Lease) {
	store, e := newDatabaseStore(w.DB, l.TenantID, w.Key)
	if e != nil {
		w.Logf("worker store error: %v", e)
		return
	}
	server := newServer(store, "", w.AllowPrivate)
	ctx, cancel := context.WithDeadline(workerCtx, l.Deadline)
	defer cancel()
	heartbeatDone := make(chan struct{})
	defer func() { cancel(); <-heartbeatDone }()
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(w.LeaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				beatCtx, done := context.WithTimeout(ctx, w.LeaseDuration/3)
				requested, err := l.heartbeat(beatCtx, w.DB, w.LeaseDuration)
				done()
				if err != nil || requested {
					cancel()
					return
				}
			}
		}
	}()
	completed := map[string]Span{}
	remoteTasks := map[string]*RemoteTask{}
	var run Run
	var uncertain string
	e = l.update(ctx, store, func(r *Run) error {
		r.Status = "running"
		r.Attempt = l.Attempt
		r.WorkerID = l.Owner
		r.Events = append(r.Events, RunEvent{Time: now(), Type: "claimed", Message: fmt.Sprintf("worker 已领取任务，第 %d 次执行", l.Attempt), WorkerID: l.Owner, Attempt: l.Attempt})
		nodes := map[string]Node{}
		for _, n := range l.Payload.Nodes {
			nodes[n.ID] = n
		}
		for i := range r.Spans {
			span := &r.Spans[i]
			if span.Status == "succeeded" {
				completed[span.NodeID] = *span
			} else if span.Status == "running" {
				agent := l.Payload.Agents[nodes[span.NodeID].AgentID]
				if agent.Kind == "external" && !agent.RetrySafe && span.Remote == nil {
					uncertain = span.NodeID
				}
				if span.Remote != nil {
					remoteTasks[span.NodeID] = span.Remote
				}
				t := now()
				span.Status = "failed"
				span.Error = "前任 worker 租约失效，节点执行中断"
				span.EndedAt = &t
				span.DurationMS = t.Sub(span.StartedAt).Milliseconds()
			}
		}
		if l.Attempt > 1 {
			r.Events = append(r.Events, RunEvent{Time: now(), Type: "recovered", Message: fmt.Sprintf("已从持久化 checkpoint 恢复 %d 个完成节点", len(completed)), WorkerID: l.Owner, Attempt: l.Attempt})
		}
		run = *r
		return nil
	}, "")
	if e != nil {
		return
	}
	if uncertain != "" {
		w.finish(l, store, run, "", fmt.Errorf("节点 %s 的远端执行结果不确定，未声明重试安全，停止自动重放以避免重复副作用", uncertain))
		return
	}
	groups, e := layers(l.Payload.Nodes)
	if e != nil {
		w.finish(l, store, run, "", e)
		return
	}
	results := map[string]string{}
	for nid, span := range completed {
		results[nid] = span.Output
	}
	state := newState()
	state.Agents = l.Payload.Agents
	state.Providers = l.Payload.Providers
	state.Secrets = l.Payload.Secrets
	var mu sync.Mutex
	var runErr error
	for _, group := range groups {
		if ctx.Err() != nil {
			break
		}
		var wg sync.WaitGroup
		var firstErr error
		for _, n := range group {
			if _, ok := completed[n.ID]; ok {
				continue
			}
			wg.Add(1)
			go func(n Node) {
				defer wg.Done()
				a := l.Payload.Agents[n.AgentID]
				input := run.Input
				if len(n.DependsOn) > 0 {
					parts := []string{}
					mu.Lock()
					for _, dep := range n.DependsOn {
						parts = append(parts, dep+": "+results[dep])
					}
					mu.Unlock()
					input = strings.Join(parts, "\n\n")
				}
				if n.Instruction != "" {
					input = n.Instruction + "\n\n" + input
				}
				span := Span{Model: l.Payload.Providers[a.ProviderID].Model, ID: id("span_"), ParentID: run.TraceID, NodeID: n.ID, Name: a.Name, AgentVersion: a.Version, Input: input, Status: "running", StartedAt: now()}
				err := l.update(ctx, store, func(r *Run) error { r.Spans = append(r.Spans, span); return nil }, "")
				if err != nil {
					cancel() // A checkpoint failure must recover through a new lease, never publish an incomplete run.
					return
				}
				if err == nil {
					if len(input) > 2*1024*1024 {
						err = fmt.Errorf("节点输入超过 2MB 上限")
					} else {
						callCtx := context.WithValue(ctx, invocationKey{}, run.ID+":"+n.ID)
						checkpoint := &remoteCheckpoint{Previous: remoteTasks[n.ID], CanCancel: func() bool {
							var requested bool
							ctx, done := context.WithTimeout(context.Background(), 2*time.Second)
							defer done()
							err := w.DB.QueryRowContext(ctx, `SELECT cancel_requested OR deadline<=now() FROM alliance_jobs WHERE id=$1`, l.ID).Scan(&requested)
							return err == nil && requested
						}, Save: func(remote *RemoteTask) error {
							err := l.update(ctx, store, func(r *Run) error {
								for i := range r.Spans {
									if r.Spans[i].ID == span.ID {
										r.Spans[i].Remote = remote
									}
								}
								return nil
							}, "")
							if err != nil {
								cancel()
								return err
							}
							span.Remote = remote
							return nil
						}}
						callCtx = context.WithValue(callCtx, remoteCheckpointKey{}, checkpoint)
						for attempt := 0; ; attempt++ {
							span.Output, span.Tokens, span.Mock, err = server.call(callCtx, a, input, state)
							if err == nil || attempt >= 2 || ctx.Err() != nil || !transientFailure(err) || (a.Kind == "external" && !a.RetrySafe) {
								break
							}
							span.RetryCount = attempt + 1
							saveErr := l.update(ctx, store, func(r *Run) error {
								r.Events = append(r.Events, RunEvent{Time: now(), Type: "retry", Message: fmt.Sprintf("节点 %s 遇到临时上游错误，准备第 %d 次重试", n.ID, attempt+1), WorkerID: l.Owner, Attempt: l.Attempt})
								return nil
							}, "")
							if saveErr != nil {
								cancel()
								return
							}
							if !retryDelay(ctx, attempt) {
								break
							}
						}
					}
				}
				t := now()
				span.EndedAt = &t
				span.DurationMS = t.Sub(span.StartedAt).Milliseconds()
				span.Status = "succeeded"
				if err != nil {
					span.Status = "failed"
					span.Error = err.Error()
				}
				if ctx.Err() != context.Canceled {
					persistCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
					saveErr := l.update(persistCtx, store, func(r *Run) error {
						for i := range r.Spans {
							if r.Spans[i].ID == span.ID {
								r.Spans[i] = span
							}
						}
						return nil
					}, "")
					done()
					if saveErr != nil {
						cancel()
						return
					}
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				results[n.ID] = span.Output
			}(n)
		}
		wg.Wait()
		if firstErr != nil {
			runErr = firstErr
			break
		}
	}
	if workerCtx.Err() != nil || ctx.Err() == context.Canceled {
		// Do not publish stale output. A successor reclaims the lease and resumes checkpoints.
		releaseCtx, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		_, _ = w.DB.ExecContext(releaseCtx, `UPDATE alliance_jobs SET lease_until=now(),updated_at=now() WHERE id=$1 AND owner=$2 AND fence=$3 AND status='running'`, l.ID, l.Owner, l.Fence)
		return
	}
	if ctx.Err() != nil {
		runErr = ctx.Err()
	}
	used := map[string]bool{}
	for _, n := range l.Payload.Nodes {
		for _, dep := range n.DependsOn {
			used[dep] = true
		}
	}
	outputs := []string{}
	for _, n := range l.Payload.Nodes {
		if !used[n.ID] && results[n.ID] != "" {
			outputs = append(outputs, results[n.ID])
		}
	}
	w.finish(l, store, run, strings.Join(outputs, "\n\n"), runErr)
}
func (w *Worker) finish(l *Lease, store *Store, run Run, output string, runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status := "succeeded"
	if runErr != nil {
		status = "failed"
	}
	e := l.update(ctx, store, func(r *Run) error {
		t := now()
		r.Status = status
		r.Output = output
		if r.A2A != nil && runErr == nil {
			var answer struct {
				State  string `json:"a2aState"`
				Output string `json:"output"`
			}
			if json.Unmarshal([]byte(output), &answer) == nil && answer.Output != "" {
				switch answer.State {
				case "input-required":
					r.A2A.State = a2a.TaskStateInputRequired
					r.Output = answer.Output
				case "auth-required":
					r.A2A.State = a2a.TaskStateAuthRequired
					r.Output = answer.Output
				case "rejected":
					r.A2A.State = a2a.TaskStateRejected
					r.Output = answer.Output
				}
			}
		}
		r.EndedAt = &t
		r.DurationMS = t.Sub(r.StartedAt).Milliseconds()
		if runErr != nil {
			r.Error = runErr.Error()
		}
		r.Events = append(r.Events, RunEvent{Time: t, Type: status, Message: "运行已终结", WorkerID: l.Owner, Attempt: l.Attempt})
		return nil
	}, status)
	if e != nil && !errors.Is(e, errLeaseLost) {
		w.Logf("worker finish error: %v", e)
	}
}
func (w *Worker) sweep(ctx context.Context) error {
	// Terminalize abandoned cancellation, deadline expiry and exhausted leases.
	tx, e := w.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var id, tenant, runID string
	var requested bool
	e = tx.QueryRowContext(ctx, `SELECT id,tenant_id,run_id,cancel_requested FROM alliance_jobs WHERE status IN ('queued','running') AND (cancel_requested OR deadline<=now() OR (attempt>=max_attempts AND lease_until<now())) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &tenant, &runID, &requested)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	store, e := newDatabaseStore(w.DB, tenant, w.Key)
	if e != nil {
		return e
	}
	st, e := store.lockState(ctx, tx)
	if e != nil {
		return e
	}
	r, ok := st.Runs[runID]
	if !ok {
		return fmt.Errorf("queued run missing")
	}
	if !terminal(r.Status) {
		t := now()
		r.Status = "failed"
		r.Error = "运行超过截止时间或恢复尝试次数上限"
		if requested {
			r.Status = "cancelled"
			r.Error = "用户取消运行"
		}
		r.EndedAt = &t
		r.DurationMS = t.Sub(r.StartedAt).Milliseconds()
		for i := range r.Spans {
			if !terminal(r.Spans[i].Status) {
				r.Spans[i].Status = r.Status
				r.Spans[i].Error = r.Error
				r.Spans[i].EndedAt = &t
			}
		}
		r.Events = append(r.Events, RunEvent{Time: t, Type: r.Status, Message: r.Error, Attempt: r.Attempt})
		st.Runs[runID] = r
		if e = store.writeState(ctx, tx, st); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE alliance_jobs SET status=$2,lease_until=NULL,updated_at=now() WHERE id=$1`, id, r.Status); e != nil {
		return e
	}
	return tx.Commit()
}

type invocationKey struct{}
