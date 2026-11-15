package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerProcess(t *testing.T) {
	if os.Getenv("ALLIANCE_WORKER_HELPER") != "true" {
		return
	}
	key, _ := base64.StdEncoding.DecodeString(os.Getenv("ALLIANCE_MASTER_KEY"))
	db, e := openDatabase(os.Getenv("ALLIANCE_DATABASE_URL"), key)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	w := newWorker(db, key, os.Getenv("ALLIANCE_WORKER_ID"), true)
	w.LeaseDuration = 900 * time.Millisecond
	w.PollInterval = 20 * time.Millisecond
	w.Concurrency = 1
	w.Run(context.Background())
}
func waitUntil(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}
func TestWorkerKillRecoveryAndFencing(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	client := newIdentityClient(t, ts.URL)
	client.register("worker@example.test")
	s, _ := g.server(client.workspace)
	started := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	requests := []string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
			return
		case <-time.After(1200 * time.Millisecond):
		}
		jsonReply(w, 200, map[string]string{"output": "recovered output"})
	}))
	defer remote.Close()
	providers := obj[[]Provider](t, client.request("GET", "providers", nil, 200))
	a := obj[Agent](t, client.request("POST", "agents", Agent{Name: "checkpoint", Description: "checkpoint", Kind: "managed", ProviderID: providers[0].ID, Prompt: "first stage"}, 201))
	b := obj[Agent](t, client.request("POST", "agents", Agent{Kind: "external", RetrySafe: true, Card: &Card{Name: "slow", Description: "slow", URL: remote.URL, Version: "1", Protocol: "http-json", Skills: []Skill{{ID: "x", Name: "x"}}}}, 201))
	service := obj[Service](t, client.request("POST", "services", Service{Name: "recovery", Nodes: []Node{{ID: "first", AgentID: a.ID}, {ID: "second", AgentID: b.ID, DependsOn: []string{"first"}}}}, 201))
	client.request("POST", "services/"+service.ID+"/publish", nil, 200)
	run := obj[Run](t, client.request("POST", "services/"+service.ID+"/invoke", map[string]string{"input": "worker failover"}, 202))
	if run.Status != "queued" {
		t.Fatal("API executed inline")
	}
	var schema string
	if e := db.QueryRow(`SELECT current_schema()`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(os.Getenv("ALLIANCE_TEST_DATABASE_URL"))
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	launch := func(id string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestWorkerProcess$")
		cmd.Env = append(os.Environ(), "ALLIANCE_WORKER_HELPER=true", "ALLIANCE_DATABASE_URL="+u.String(), "ALLIANCE_MASTER_KEY="+base64.StdEncoding.EncodeToString(key), "ALLIANCE_WORKER_ID="+id)
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		return cmd
	}
	first := launch("worker-to-kill")
	defer func() { _ = first.Process.Kill() }()
	select {
	case <-started:
	case <-time.After(15 * time.Second):
		t.Fatal("worker never reached remote node")
	}
	snapshot, _ := s.store.snapshotE()
	if snapshot.Runs[run.ID].Spans[0].Status != "succeeded" {
		t.Fatal("first checkpoint not committed")
	}
	if e := first.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	_ = first.Wait()
	second := launch("recovery-worker")
	defer func() { _ = second.Process.Kill(); _ = second.Wait() }()
	waitUntil(t, func() bool { st, e := s.store.snapshotE(); return e == nil && terminal(st.Runs[run.ID].Status) })
	st, _ := s.store.snapshotE()
	result := st.Runs[run.ID]
	if result.Status != "succeeded" || result.Output != "recovered output" || result.Attempt != 2 || result.WorkerID != "recovery-worker" {
		t.Fatalf("failover failed: %+v", result)
	}
	firstCount := 0
	for _, span := range result.Spans {
		if span.NodeID == "first" {
			firstCount++
		}
	}
	if firstCount != 1 {
		t.Fatal("committed node replayed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0] == "" || requests[0] != requests[1] {
		t.Fatalf("idempotency identity changed: %v", requests)
	}
	var jobID string
	db.QueryRow(`SELECT id FROM alliance_jobs WHERE tenant_id=$1 AND run_id=$2`, client.workspace, run.ID).Scan(&jobID)
	stale := &Lease{ID: jobID, RunID: run.ID, TenantID: client.workspace, Owner: "worker-to-kill", Fence: 1}
	if e := stale.update(context.Background(), s.store, func(r *Run) error { r.Output = "stale overwrite"; return nil }, ""); e != errLeaseLost {
		t.Fatalf("stale worker accepted: %v", e)
	}
}
func TestDurableIdempotencyCancellationAndDeadline(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	c := newIdentityClient(t, ts.URL)
	c.register("durable@example.test")
	s, _ := g.server(c.workspace)
	p := obj[[]Provider](t, c.request("GET", "providers", nil, 200))[0]
	a := obj[Agent](t, c.request("POST", "agents", Agent{Name: "durable", Description: "durable", Kind: "managed", ProviderID: p.ID, Prompt: "test"}, 201))
	send := func(input string, want int) Run {
		body, _ := json.Marshal(map[string]string{"input": input})
		r := httptest.NewRequest("POST", "/api/v1/agents/"+a.ID+"/invoke", bytes.NewReader(body))
		r.Header.Set("Idempotency-Key", "same-key")
		w := httptest.NewRecorder()
		code, out, e := s.invoke(w, r, "agents", a.ID)
		if want == 409 {
			if e == nil || !strings.Contains(e.Error(), "幂等键") {
				t.Fatalf("wanted conflict: %v", e)
			}
			return Run{}
		}
		if e != nil || code != want {
			t.Fatalf("invoke: %v %d", e, code)
		}
		return out.(Run)
	}
	first := send("hello", 202)
	second := send("hello", 202)
	if first.ID != second.ID {
		t.Fatal("duplicate run enqueued")
	}
	send("different", 409)
	c.request("POST", "runs/"+first.ID+"/cancel", map[string]any{}, 200)
	var status string
	db.QueryRow(`SELECT status FROM alliance_jobs WHERE tenant_id=$1 AND run_id=$2`, c.workspace, first.ID).Scan(&status)
	if status != "cancelled" {
		t.Fatal("queued cancel not persisted")
	}
	s.timeout = 100 * time.Millisecond
	r := obj[Run](t, c.request("POST", "agents/"+a.ID+"/invoke", map[string]string{"input": "expire before claim"}, 202))
	time.Sleep(130 * time.Millisecond)
	worker := newWorker(db, key, "sweeper", true)
	if e := worker.sweep(context.Background()); e != nil {
		t.Fatal(e)
	}
	st, _ := s.store.snapshotE()
	if st.Runs[r.ID].Status != "failed" {
		t.Fatal("deadline not enforced")
	}
	s.timeout = time.Minute
	r = obj[Run](t, c.request("POST", "agents/"+a.ID+"/invoke", map[string]string{"input": "[slow]"}, 202))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.LeaseDuration = 600 * time.Millisecond
	worker.PollInterval = 10 * time.Millisecond
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitUntil(t, func() bool { st, _ := s.store.snapshotE(); return len(st.Runs[r.ID].Spans) > 0 })
	c.request("POST", "runs/"+r.ID+"/cancel", map[string]any{}, 200)
	waitUntil(t, func() bool {
		var status string
		db.QueryRow(`SELECT status FROM alliance_jobs WHERE tenant_id=$1 AND run_id=$2`, c.workspace, r.ID).Scan(&status)
		return status == "cancelled"
	})
	st, _ = s.store.snapshotE()
	if st.Runs[r.ID].Spans[0].Status != "cancelled" {
		t.Fatal("cross-process cancel left span running")
	}
}

func TestWorkerTransientRetryRequiresSafety(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	client := newIdentityClient(t, ts.URL)
	client.register("retry@example.test")
	var mu sync.Mutex
	counts := map[string]int{}
	keys := map[string][]string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Input string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		mu.Lock()
		counts[q.Input]++
		attempt := counts[q.Input]
		keys[q.Input] = append(keys[q.Input], r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if attempt < 3 {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"output": "recovered"})
	}))
	defer remote.Close()
	makeAgent := func(name string, safe bool) Agent {
		return obj[Agent](t, client.request("POST", "agents", Agent{Kind: "external", RetrySafe: safe, Card: &Card{Name: name, Description: name, Protocol: "http-json", URL: remote.URL, Version: "1", Skills: []Skill{{ID: "retry", Name: "retry"}}}}, 201))
	}
	safe, unsafe := makeAgent("safe", true), makeAgent("unsafe", false)
	ctx, cancel := context.WithCancel(context.Background())
	worker := newWorker(db, key, "retry-worker", true)
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for _, item := range []struct {
		agent       Agent
		input, want string
	}{{safe, "safe", "succeeded"}, {unsafe, "unsafe", "failed"}} {
		run := obj[Run](t, client.request("POST", "agents/"+item.agent.ID+"/invoke", map[string]string{"input": item.input}, 202))
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			run = obj[Run](t, client.request("GET", "runs/"+run.ID, nil, 200))
			if terminal(run.Status) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if run.Status != item.want {
			t.Fatalf("%s status %s", item.input, run.Status)
		}
		if item.input == "safe" && run.Spans[0].RetryCount != 2 {
			t.Fatal("missing retry evidence")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if counts["safe"] != 3 || counts["unsafe"] != 1 {
		t.Fatalf("retry counts %+v", counts)
	}
	for _, key := range keys["safe"] {
		if key == "" || key != keys["safe"][0] {
			t.Fatal("retry idempotency key changed")
		}
	}
}

func TestA2ARemoteCheckpointResumesWithoutResending(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	client := newIdentityClient(t, ts.URL)
	client.register("a2a-recovery@example.test")
	var sends, cancels atomic.Int32
	var complete atomic.Bool
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&q)
		if q.Method == "message/send" {
			sends.Add(1)
		}
		if q.Method == "tasks/cancel" {
			cancels.Add(1)
		}
		state := "working"
		task := map[string]any{"kind": "task", "id": "durable-remote-task", "contextId": "remote-context"}
		if complete.Load() {
			state = "completed"
			task["artifacts"] = []any{map[string]any{"artifactId": "result", "parts": []any{map[string]string{"kind": "text", "text": "remote checkpoint recovered"}}}}
		}
		task["status"] = map[string]string{"state": state}
		json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID, "result": task})
	}))
	defer remote.Close()
	agent := obj[Agent](t, client.request("POST", "agents", Agent{Kind: "external", Card: &Card{Name: "remote checkpoint", Description: "resume task", URL: remote.URL, Protocol: "a2a-jsonrpc", ProtocolVersion: "0.3.0", Version: "1", Skills: []Skill{{ID: "one", Name: "one"}}}}, 201))
	run := obj[Run](t, client.request("POST", "agents/"+agent.ID+"/invoke", map[string]string{"input": "resume"}, 202))
	s, _ := g.server(client.workspace)
	ctx1, stop1 := context.WithCancel(context.Background())
	first := newWorker(db, key, "remote-worker-1", true)
	first.LeaseDuration = 900 * time.Millisecond
	first.Concurrency = 1
	done1 := make(chan struct{})
	go func() { first.Run(ctx1); close(done1) }()
	defer stop1()
	waitUntil(t, func() bool {
		st, e := s.store.snapshotE()
		return e == nil && len(st.Runs[run.ID].Spans) > 0 && st.Runs[run.ID].Spans[0].Remote != nil
	})
	stop1()
	<-done1
	if cancels.Load() != 0 {
		t.Fatal("worker shutdown canceled a recoverable remote task")
	}
	complete.Store(true)
	ctx2, stop2 := context.WithCancel(context.Background())
	second := newWorker(db, key, "remote-worker-2", true)
	second.LeaseDuration = 900 * time.Millisecond
	second.Concurrency = 1
	done2 := make(chan struct{})
	go func() { second.Run(ctx2); close(done2) }()
	defer func() { stop2(); <-done2 }()
	waitUntil(t, func() bool { st, e := s.store.snapshotE(); return e == nil && terminal(st.Runs[run.ID].Status) })
	st, _ := s.store.snapshotE()
	result := st.Runs[run.ID]
	if result.Status != "succeeded" || result.Output != "remote checkpoint recovered" || result.Attempt != 2 || sends.Load() != 1 {
		t.Fatalf("remote replay: sends=%d result=%+v", sends.Load(), result)
	}
}
