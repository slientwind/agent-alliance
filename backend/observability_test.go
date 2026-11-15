package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestObservationOutboxIsolationRedactionAndRetry(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	a := newIdentityClient(t, ts.URL)
	a.register("observe-a@example.test")
	b := newIdentityClient(t, ts.URL)
	b.register("observe-b@example.test")
	var fail atomic.Bool
	fail.Store(true)
	var seen atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, `{"data":[],"meta":{}}`)
			return
		}
		pub, secret, ok := r.BasicAuth()
		if !ok || pub != "pk-local" || secret != "sk-local-test-secret" {
			t.Error("incorrect project credentials")
		}
		if r.URL.Path != "/api/public/otel/v1/traces" || r.Header.Get("x-langfuse-ingestion-version") != "4" {
			t.Error("wrong OTLP path or version")
		}
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "private-agent-token") || strings.Contains(string(raw), "sk-raw-secret1234") {
			t.Error("credential leaked")
		}
		if !strings.Contains(string(raw), "[REDACTED]") {
			t.Error("expected redacted content")
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil {
			t.Error("invalid OTLP JSON")
		}
		seen.Add(1)
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer collector.Close()
	cfg := LangfuseConfig{Enabled: true, BaseURL: collector.URL, PublicKey: "pk-local", SecretKey: "sk-local-test-secret", IncludeContent: true, ProjectID: "local"}
	response := a.request("PUT", "integrations/langfuse", cfg, 200)
	if strings.Contains(string(response), cfg.SecretKey) {
		t.Fatal("secret echoed")
	}
	other := obj[LangfuseConfig](t, b.request("GET", "integrations/langfuse", nil, 200))
	if other.HasSecret || other.Enabled {
		t.Fatal("cross-tenant configuration leak")
	}
	store, _ := newDatabaseStore(db, a.workspace, key)
	encrypted, _ := store.encrypt("private-agent-token")
	start := now()
	end := start.Add(time.Second)
	run := Run{ID: "run_observe", TraceID: "trace_observe", TargetName: "Observed Agent", Status: "running", StartedAt: start, Version: 1, Input: "private-agent-token"}
	if e := store.update(func(st *State) error { st.Secrets["agent"] = encrypted; st.Runs[run.ID] = run; return nil }); e != nil {
		t.Fatal(e)
	}
	finish := func(st *State) error {
		run.Status = "succeeded"
		run.Output = "private-agent-token sk-raw-secret1234"
		run.EndedAt = &end
		run.Spans = []Span{{ID: "span_one", Name: "Model", Status: "succeeded", StartedAt: start, EndedAt: &end}}
		st.Runs[run.ID] = run
		return nil
	}
	if e := store.update(finish); e != nil {
		t.Fatal(e)
	}
	if e := store.update(finish); e != nil {
		t.Fatal(e)
	}
	var count int
	db.QueryRow(`SELECT count(*) FROM alliance_outbox`).Scan(&count)
	if count != 1 {
		t.Fatalf("duplicate export count=%d", count)
	}
	var raw string
	db.QueryRow(`SELECT payload::text FROM alliance_outbox`).Scan(&raw)
	if strings.Contains(raw, cfg.SecretKey) || strings.Contains(raw, "private-agent-token") {
		t.Fatal("plaintext in outbox")
	}
	worker := newWorker(db, key, "export-worker", true)
	if e := worker.exportObservation(context.Background()); e != nil {
		t.Fatal(e)
	}
	var status string
	db.QueryRow(`SELECT status FROM alliance_outbox`).Scan(&status)
	if status != "pending" {
		t.Fatal(status)
	}
	fail.Store(false)
	db.Exec(`UPDATE alliance_outbox SET available_at=now()`)
	if e := worker.exportObservation(context.Background()); e != nil {
		t.Fatal(e)
	}
	db.QueryRow(`SELECT status FROM alliance_outbox`).Scan(&status)
	if status != "sent" || seen.Load() != 2 {
		t.Fatalf("status %s attempts %d", status, seen.Load())
	}
	if e := worker.exportObservation(context.Background()); e != nil {
		t.Fatal(e)
	}
	if seen.Load() != 2 {
		t.Fatal("exported delivered event twice")
	}
	// Default metadata mode never exports input/output or error body.
	run.Error = "personal confidential data"
	payload := otlpBody(a.workspace, run, false, nil)
	if strings.Contains(string(payload), "private-agent-token") || strings.Contains(string(payload), run.Error) {
		t.Fatal("metadata mode leaks content")
	}
	// A different workspace cannot see export state.
	if string(b.request("GET", "integrations/langfuse/exports", nil, 200)) != "[]\n" {
		t.Fatal("cross-tenant exports leak")
	}
}
func TestRealLangfuseIngestion(t *testing.T) {
	base := os.Getenv("ALLIANCE_TEST_LANGFUSE_URL")
	if base == "" {
		t.Skip("set ALLIANCE_TEST_LANGFUSE_URL for real Langfuse integration")
	}
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	a := newIdentityClient(t, ts.URL)
	a.register("real-langfuse@example.test")
	store, _ := newDatabaseStore(db, a.workspace, key)
	secret, _ := store.encrypt("sk-lf-alliance-local")
	start := now().Add(-time.Second)
	end := now()
	tokens := 42
	run := Run{ID: id("run_"), TraceID: id("trace_"), TargetName: "Alliance real integration", Status: "succeeded", Version: 1, Input: "mock input", Output: "mock output", StartedAt: start, EndedAt: &end, Spans: []Span{{ID: id("span_"), Name: "Mock model generation", Status: "succeeded", Input: "mock input", Output: "mock output", StartedAt: start, EndedAt: &end, Tokens: &tokens, AgentVersion: 1}}}
	worker := newWorker(db, key, "real-export", true)
	e := worker.sendObservation(context.Background(), a.workspace, observationEnvelope{Config: LangfuseConfig{BaseURL: base, PublicKey: "pk-lf-alliance-local", SecretKey: secret}, Body: otlpBody(a.workspace, run, true, nil)})
	if e != nil {
		t.Fatal(e)
	}
	traceID := stableHex(a.workspace+":"+run.TraceID, 16)
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest("GET", base+"/api/public/v2/observations?traceId="+traceID+"&fields=basic,usage,metadata", nil)
		req.SetBasicAuth("pk-lf-alliance-local", "sk-lf-alliance-local")
		resp, e := client.Do(req)
		if e == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.Contains(string(body), "Mock model generation") && strings.Contains(string(body), `"totalUsage":42`) && strings.Contains(string(body), `"parentObservationId":"`+stableHex(run.ID, 8)+`"`) {
				t.Logf("verified trace %s/project/alliance-local-project/traces/%s", base, traceID)
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("real Langfuse trace was not queryable after ingestion")
}
