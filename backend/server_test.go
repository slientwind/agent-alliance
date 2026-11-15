package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	st, e := openStore(filepath.Join(t.TempDir(), "state.json"), bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	s := newServer(st, "test-key-123456789", true)
	t.Cleanup(s.shutdown)
	return s
}
func req(t *testing.T, s *Server, method, path string, body any, want int) []byte {
	t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, "/api/v1/"+path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}
func obj[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if e := json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}
func setup(t *testing.T, s *Server) (Provider, Agent) {
	t.Helper()
	p := obj[Provider](t, req(t, s, "POST", "providers", Provider{Name: "mock", Type: "mock", Model: "mock-v1"}, 201))
	a := obj[Agent](t, req(t, s, "POST", "agents", Agent{Name: "分析", Description: "分析输入", Kind: "managed", ProviderID: p.ID, Prompt: "原始 prompt"}, 201))
	return p, a
}
func waitRun(t *testing.T, s *Server, id string) Run {
	t.Helper()
	for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); {
		r := s.store.snapshot().Runs[id]
		if terminal(r.Status) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run timeout")
	return Run{}
}
func invokeTest(t *testing.T, s *Server, kind, id, input string) Run {
	t.Helper()
	return obj[Run](t, req(t, s, "POST", kind+"/"+id+"/invoke", map[string]string{"input": input}, 202))
}
func TestAuthValidationAndCRUD(t *testing.T) {
	s := testServer(t)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/agents", nil))
	if w.Code != 401 {
		t.Fatal("missing auth accepted")
	}
	p, a := setup(t, s)
	req(t, s, "POST", "agents", Agent{Name: "bad", Kind: "managed"}, 400)
	req(t, s, "DELETE", "providers/"+p.ID, nil, 409)
	a.Status = "disabled"
	a = obj[Agent](t, req(t, s, "PUT", "agents/"+a.ID, a, 200))
	if a.Version != 2 {
		t.Fatal("version not incremented")
	}
	req(t, s, "POST", "agents/"+a.ID+"/invoke", map[string]string{"input": "x"}, 409)
	req(t, s, "DELETE", "agents/"+a.ID, nil, 200)
	req(t, s, "GET", "agents/"+a.ID, nil, 404)
	req(t, s, "DELETE", "providers/"+p.ID, nil, 200)
}
func TestGenerateAndSecrets(t *testing.T) {
	s := testServer(t)
	p, a := setup(t, s)
	b := req(t, s, "POST", "agents/generate", map[string]string{"name": "写作", "description": "输出报告", "providerId": p.ID}, 200)
	if !strings.Contains(string(b), "输出报告") {
		t.Fatal("prompt generation failed")
	}
	a.Key = "super-private-value"
	a = obj[Agent](t, req(t, s, "PUT", "agents/"+a.ID, a, 200))
	if a.Key != "" || !a.HasKey {
		t.Fatal("key leak")
	}
	all := req(t, s, "GET", "agents", nil, 200)
	if bytes.Contains(all, []byte("super-private")) {
		t.Fatal("API secret leaked")
	}
	disk, _ := os.ReadFile(s.store.path)
	if bytes.Contains(disk, []byte("super-private")) {
		t.Fatal("disk secret not encrypted")
	}
	req(t, s, "PUT", "agents/"+a.ID, a, 200)
	plain, e := s.store.decrypt(s.store.snapshot().Secrets[a.ID])
	if e != nil || plain != "super-private-value" {
		t.Fatal("empty key overwrote secret")
	}
	if _, e = openStore(s.store.path, bytes.Repeat([]byte{8}, 32)); e == nil {
		t.Fatal("wrong master key accepted")
	}
}
func TestDAGValidation(t *testing.T) {
	s := testServer(t)
	_, a := setup(t, s)
	cases := [][]Node{{}, {{ID: "a", AgentID: a.ID, DependsOn: []string{"b"}}}, {{ID: "a", AgentID: a.ID}, {ID: "a", AgentID: a.ID}}, {{ID: "a", AgentID: a.ID, DependsOn: []string{"b"}}, {ID: "b", AgentID: a.ID, DependsOn: []string{"a"}}}, {{ID: "a", AgentID: "missing"}}, {{ID: "a", AgentID: a.ID}, {ID: "b", AgentID: a.ID, DependsOn: []string{"a", "a"}}}}
	for _, nodes := range cases {
		req(t, s, "POST", "services", Service{Name: "bad", Nodes: nodes}, 400)
	}
}
func TestPublishedSnapshotAndReferences(t *testing.T) {
	s := testServer(t)
	p, a := setup(t, s)
	v := obj[Service](t, req(t, s, "POST", "services", Service{Name: "工作流", Nodes: []Node{{ID: "a", AgentID: a.ID}}}, 201))
	req(t, s, "POST", "services/"+v.ID+"/invoke", map[string]string{"input": "hello"}, 409)
	v = obj[Service](t, req(t, s, "POST", "services/"+v.ID+"/publish", nil, 200))
	a.Prompt = "更新 prompt"
	req(t, s, "PUT", "agents/"+a.ID, a, 200)
	run := waitRun(t, s, invokeTest(t, s, "services", v.ID, "hello").ID)
	if !strings.Contains(run.Output, "原始 prompt") || run.Spans[0].AgentVersion != 1 {
		t.Fatalf("release mutated: %+v", run)
	}
	v.Nodes[0].Instruction = "草稿补充"
	req(t, s, "PUT", "services/"+v.ID, v, 200)
	run = waitRun(t, s, invokeTest(t, s, "services", v.ID, "again").ID)
	if strings.Contains(run.Spans[0].Input, "草稿补充") {
		t.Fatal("draft affected release")
	}
	req(t, s, "DELETE", "agents/"+a.ID, nil, 409)
	req(t, s, "DELETE", "providers/"+p.ID, nil, 409)
	req(t, s, "POST", "services/"+v.ID+"/publish", nil, 200)
	run = waitRun(t, s, invokeTest(t, s, "services", v.ID, "hello").ID)
	if run.Version != 2 || !strings.Contains(run.Output, "更新 prompt") {
		t.Fatal("republish did not update snapshot")
	}
	req(t, s, "DELETE", "services/"+v.ID+"/publish", nil, 200)
	req(t, s, "POST", "services/"+v.ID+"/invoke", map[string]string{"input": "hello"}, 409)
	req(t, s, "DELETE", "services/"+v.ID, nil, 200)
	req(t, s, "DELETE", "agents/"+a.ID, nil, 200)
}
func TestParallelJoinFailureCancelAndTimeout(t *testing.T) {
	s := testServer(t)
	_, a := setup(t, s)
	v := obj[Service](t, req(t, s, "POST", "services", Service{Name: "并行汇聚", Nodes: []Node{{ID: "a", AgentID: a.ID}, {ID: "b", AgentID: a.ID}, {ID: "join", AgentID: a.ID, DependsOn: []string{"a", "b"}}}}, 201))
	req(t, s, "POST", "services/"+v.ID+"/publish", nil, 200)
	r := waitRun(t, s, invokeTest(t, s, "services", v.ID, "hello").ID)
	if r.Status != "succeeded" || len(r.Spans) != 3 {
		t.Fatalf("bad DAG %+v", r)
	}
	sp := map[string]Span{}
	for _, v := range r.Spans {
		sp[v.NodeID] = v
	}
	if !sp["a"].StartedAt.Before(*sp["b"].EndedAt) || !sp["b"].StartedAt.Before(*sp["a"].EndedAt) {
		t.Fatal("roots did not overlap")
	}
	if sp["join"].StartedAt.Before(*sp["a"].EndedAt) || !strings.Contains(sp["join"].Input, "b:") {
		t.Fatal("join did not wait for inputs")
	}
	r = waitRun(t, s, invokeTest(t, s, "services", v.ID, "[fail]").ID)
	if r.Status != "failed" || len(r.Spans) != 2 {
		t.Fatal("downstream executed after failure")
	}
	r = invokeTest(t, s, "agents", a.ID, "[slow]")
	time.Sleep(30 * time.Millisecond)
	req(t, s, "POST", "runs/"+r.ID+"/cancel", nil, 200)
	s.wg.Wait()
	r = s.store.snapshot().Runs[r.ID]
	if r.Status != "cancelled" || len(r.Spans) != 1 || r.Spans[0].Status != "cancelled" {
		t.Fatalf("cancellation inconsistent %+v", r)
	}
	req(t, s, "POST", "runs/"+r.ID+"/cancel", nil, 409)
	s.timeout = 30 * time.Millisecond
	r = waitRun(t, s, invokeTest(t, s, "agents", a.ID, "[slow]").ID)
	if r.Status != "failed" || !strings.Contains(r.Error, "deadline") {
		t.Fatal("timeout not propagated")
	}
}
func TestExternalProtocolsAndModel(t *testing.T) {
	s := testServer(t)
	var receivedAuth string
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/http":
			jsonReply(w, 200, map[string]string{"output": "external result"})
		case "/a2a":
			if body["method"] != "message/send" {
				t.Error("wrong A2A method")
			}
			jsonReply(w, 200, map[string]any{"jsonrpc": "2.0", "id": body["id"], "result": map[string]any{"kind": "message", "parts": []any{map[string]string{"kind": "text", "text": "a2a result"}}}})
		case "/v1/chat/completions":
			receivedAuth = r.Header.Get("Authorization")
			jsonReply(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "model result"}}}, "usage": map[string]int{"total_tokens": 17}})
		default:
			w.WriteHeader(503)
		}
	}))
	defer remote.Close()
	for _, protocol := range []string{"http-json", "a2a-jsonrpc"} {
		path := "/http"
		if protocol == "a2a-jsonrpc" {
			path = "/a2a"
		}
		a := obj[Agent](t, req(t, s, "POST", "agents", Agent{Kind: "external", Card: &Card{Name: "remote", Description: "remote", URL: remote.URL + path, Version: "1", Protocol: protocol, ProtocolVersion: "0.3.0", Skills: []Skill{{ID: "x", Name: "x"}}}}, 201))
		r := waitRun(t, s, invokeTest(t, s, "agents", a.ID, "test").ID)
		if r.Status != "succeeded" || !strings.Contains(r.Output, "result") {
			t.Fatalf("adapter failed %+v", r)
		}
	}
	p := obj[Provider](t, req(t, s, "POST", "providers", Provider{Name: "remote model", Type: "openai", Model: "test", BaseURL: remote.URL + "/v1"}, 201))
	a := obj[Agent](t, req(t, s, "POST", "agents", Agent{Name: "model", Description: "test", Kind: "managed", ProviderID: p.ID, Prompt: "system", Key: "secret"}, 201))
	r := waitRun(t, s, invokeTest(t, s, "agents", a.ID, "hello").ID)
	if r.Output != "model result" || *r.Spans[0].Tokens != 17 || receivedAuth != "Bearer secret" {
		t.Fatal("model adapter failed")
	}
	a.Card = &Card{URL: "file:///etc/passwd"}
	a.Kind = "external"
	req(t, s, "PUT", "agents/"+a.ID, a, 400)
}
func TestNetworkPolicy(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { jsonReply(w, 200, map[string]string{"output": "x"}) }))
	defer remote.Close()
	s := testServer(t)
	s.client = httpClient(false)
	_, e := s.remote(context.Background(), remote.URL, "", map[string]string{"input": "x"})
	if e == nil {
		t.Fatal("private endpoint allowed by default")
	}
	for _, url := range []string{"file:///tmp/x", "http://user:pass@example.com", "https://example.com/#x"} {
		if validateURL(url) == nil {
			t.Fatal("invalid URL accepted")
		}
	}
}
func TestPersistenceRollbackAndRestart(t *testing.T) {
	s := testServer(t)
	_, a := setup(t, s)
	r := waitRun(t, s, invokeTest(t, s, "agents", a.ID, "history").ID)
	s.wg.Wait()
	reloaded, e := openStore(s.store.path, bytes.Repeat([]byte{7}, 32))
	if e != nil || reloaded.snapshot().Runs[r.ID].Status != "succeeded" {
		t.Fatal("history lost")
	}
	if e = s.store.update(func(st *State) error {
		st.Runs["interrupted"] = Run{ID: "interrupted", Status: "running", StartedAt: now(), Spans: []Span{{Status: "running", StartedAt: now()}}}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	reloaded, e = openStore(s.store.path, bytes.Repeat([]byte{7}, 32))
	if e != nil || reloaded.snapshot().Runs["interrupted"].Status != "failed" {
		t.Fatal("interrupted run not recovered")
	}
	before := len(s.store.snapshot().Agents)
	s.store.path = filepath.Join(s.store.path, "invalid")
	e = s.store.update(func(st *State) error { delete(st.Agents, a.ID); return nil })
	if e == nil || len(s.store.snapshot().Agents) != before {
		t.Fatal("failed commit modified memory")
	}
}
func TestMalformedInputsAndConcurrentCalls(t *testing.T) {
	s := testServer(t)
	_, a := setup(t, s)
	r := httptest.NewRequest("POST", "/api/v1/agents", strings.NewReader(`{"unknown":true}`))
	r.Header.Set("Authorization", "Bearer "+s.token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatal("unknown fields accepted")
	}
	runs := []Run{}
	for i := 0; i < 8; i++ {
		runs = append(runs, invokeTest(t, s, "agents", a.ID, fmt.Sprint(i)))
	}
	for _, r := range runs {
		if waitRun(t, s, r.ID).Status != "succeeded" {
			t.Fatal("concurrent run failed")
		}
	}
	req(t, s, "POST", "agents/"+a.ID+"/invoke", map[string]string{"input": ""}, 400)
	schema := obj[map[string]any](t, req(t, s, "GET", "openapi.json", nil, 200))
	if schema["openapi"] != "3.0.3" {
		t.Fatal("OpenAPI missing")
	}
}

func TestStandardCardAndBearer(t *testing.T) {
	s := testServer(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer remote-secret" {
			w.WriteHeader(401)
			return
		}
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		jsonReply(w, 200, map[string]any{"jsonrpc": "2.0", "id": b["id"], "result": map[string]any{"kind": "message", "parts": []any{map[string]string{"kind": "text", "text": "authorized"}}}})
	}))
	defer remote.Close()
	card := map[string]any{"name": "standard", "description": "standard card", "url": remote.URL, "version": "1.0", "protocolVersion": "0.3.0", "preferredTransport": "JSONRPC", "capabilities": map[string]bool{"streaming": false}, "defaultInputModes": []string{"text/plain"}, "defaultOutputModes": []string{"text/plain"}, "securitySchemes": map[string]any{"bearer": map[string]string{"type": "http", "scheme": "bearer"}}, "security": []any{map[string]any{"bearer": []string{}}}, "skills": []any{map[string]any{"id": "a", "name": "skill", "tags": []string{"test"}, "examples": []string{"hello"}}}}
	req(t, s, "POST", "agents", map[string]any{"kind": "external", "card": card}, 400)
	a := obj[Agent](t, req(t, s, "POST", "agents", map[string]any{"kind": "external", "card": card, "apiKey": "remote-secret"}, 201))
	if a.Key != "" || !a.HasKey || a.Card.Protocol != "a2a-jsonrpc" {
		t.Fatal("card normalization or secret failed")
	}
	b := req(t, s, "GET", "agents/"+a.ID, nil, 200)
	if !bytes.Contains(b, []byte("capabilities")) || !bytes.Contains(b, []byte("examples")) || bytes.Contains(b, []byte("remote-secret")) {
		t.Fatal("standard metadata lost or credentials leaked")
	}
	r := waitRun(t, s, invokeTest(t, s, "agents", a.ID, "x").ID)
	if r.Output != "authorized" {
		t.Fatalf("bearer failed: %+v", r)
	}
	card["preferredTransport"] = "UNSUPPORTED"
	req(t, s, "POST", "agents", map[string]any{"kind": "external", "card": card, "apiKey": "x"}, 400)
}
