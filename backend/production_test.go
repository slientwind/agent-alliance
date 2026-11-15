package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

func databaseFixture(t *testing.T) (*sql.DB, []byte) {
	t.Helper()
	dsn := os.Getenv("ALLIANCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set ALLIANCE_TEST_DATABASE_URL for real PostgreSQL integration tests")
	}
	admin, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := id("test_")
	if _, e = admin.Exec(`CREATE SCHEMA ` + schema); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	key := bytes.Repeat([]byte{31}, 32)
	db, e := openDatabase(u.String(), key)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close(); _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	return db, key
}

type identityClient struct {
	t                              *testing.T
	client                         *http.Client
	base                           string
	csrf, workspace, userID, email string
}

func newIdentityClient(t *testing.T, base string) *identityClient {
	jar, _ := cookiejar.New(nil)
	return &identityClient{t: t, client: &http.Client{Jar: jar}, base: base}
}
func (c *identityClient) request(method, path string, body any, want int) []byte {
	c.t.Helper()
	b, _ := json.Marshal(body)
	r, _ := http.NewRequest(method, c.base+"/api/v1/"+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Workspace-ID", c.workspace)
	if c.csrf != "" {
		r.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, e := c.client.Do(r)
	if e != nil {
		c.t.Fatal(e)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	return raw
}
func (c *identityClient) register(email string) {
	c.t.Helper()
	c.email = email
	b := c.request("POST", "auth/register", map[string]string{"email": email, "password": "integration-password-123", "name": "Test User", "workspace": "Workspace " + email}, 200)
	v := obj[map[string]string](c.t, b)
	c.csrf = v["csrfToken"]
	var info struct{ User Principal }
	json.Unmarshal(c.request("GET", "auth/me", nil, 200), &info)
	c.workspace = info.User.TenantID
	c.userID = info.User.UserID
}
func TestPostgresTenantIsolationAndRBAC(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	a := newIdentityClient(t, ts.URL)
	a.register("a@example.test")
	b := newIdentityClient(t, ts.URL)
	b.register("b@example.test")
	providers := obj[[]Provider](t, a.request("GET", "providers", nil, 200))
	agent := obj[Agent](t, a.request("POST", "agents", Agent{Name: "private-a", Description: "tenant scoped", Kind: "managed", ProviderID: providers[0].ID, Prompt: "hello", Key: "agent-a-only"}, 201))
	if got := obj[[]Agent](t, b.request("GET", "agents", nil, 200)); len(got) != 0 {
		t.Fatal("cross-tenant listing leak")
	}
	b.request("GET", "agents/"+agent.ID, nil, 404)
	b.request("DELETE", "agents/"+agent.ID, nil, 404)
	b.request("POST", "agents/"+agent.ID+"/invoke", map[string]string{"input": "x"}, 404)
	original := b.workspace
	b.workspace = a.workspace
	b.request("GET", "agents", nil, 403)
	b.workspace = original
	a.request("POST", "workspaces/members", map[string]string{"email": b.email, "role": "viewer"}, 200)
	b.workspace = a.workspace
	b.request("GET", "agents/"+agent.ID, nil, 200)
	b.request("DELETE", "agents/"+agent.ID, nil, 403)
	b.request("POST", "agents/"+agent.ID+"/invoke", map[string]string{"input": "x"}, 403)
	a.request("PUT", "workspaces/members", map[string]string{"email": b.email, "role": "operator"}, 200)
	b.request("POST", "agents/"+agent.ID+"/invoke", map[string]string{"input": "x"}, 202)
	b.request("POST", "providers", Provider{Name: "bad", Type: "mock", Model: "mock"}, 403)
	b.request("GET", "api-keys", nil, 403)
	a.request("DELETE", "workspaces/members", map[string]string{"email": a.email}, 409)
	a.request("PUT", "workspaces/members", map[string]string{"email": a.email, "role": "viewer"}, 409)
	a.request("DELETE", "workspaces/members", map[string]string{"email": b.email}, 200)
	b.request("GET", "agents", nil, 403)
	b.workspace = original
	b.request("GET", "agents", nil, 200)
	stA, _ := newDatabaseStore(db, a.workspace, key)
	stB, _ := newDatabaseStore(db, b.workspace, key)
	state, e := stA.snapshotE()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = stB.decrypt(state.Secrets[agent.ID]); e == nil {
		t.Fatal("cross-tenant ciphertext decrypted")
	}
	raw := a.request("GET", "agents", nil, 200)
	if bytes.Contains(raw, []byte("agent-a-only")) {
		t.Fatal("credential leaked")
	}
	csrf := a.csrf
	a.csrf = "bad"
	a.request("POST", "agents", Agent{}, 403)
	a.csrf = csrf
	keyObj := obj[map[string]string](t, a.request("POST", "api-keys", map[string]string{"name": "client", "role": "operator"}, 201))
	r, _ := http.NewRequest("POST", ts.URL+"/api/v1/agents/"+agent.ID+"/invoke", strings.NewReader(`{"input":"key call"}`))
	r.Header.Set("Authorization", "Bearer "+keyObj["key"])
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal("API key could not invoke")
	}
	a.request("DELETE", "api-keys/"+keyObj["id"], nil, 200)
	resp, e = http.DefaultClient.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("revoked key accepted")
	}
	a.request("POST", "auth/logout", map[string]any{}, 200)
	a.request("GET", "auth/me", nil, 401)
}
func TestPostgresConcurrentStateAndRestart(t *testing.T) {
	db, key := databaseFixture(t)
	g := newGateway(db, key, true)
	g.allowRegistration = true
	ts := httptest.NewServer(g)
	defer ts.Close()
	defer g.shutdown()
	a := newIdentityClient(t, ts.URL)
	a.register("concurrent@example.test")
	first, _ := newDatabaseStore(db, a.workspace, key)
	second, _ := newDatabaseStore(db, a.workspace, key)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := first
			if i%2 == 1 {
				s = second
			}
			errs <- s.update(func(st *State) error {
				id := fmt.Sprintf("parallel-%d", i)
				st.Providers[id] = Provider{ID: id, Name: id, Type: "mock", Model: "mock"}
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	reopened, e := newDatabaseStore(db, a.workspace, key)
	if e != nil {
		t.Fatal(e)
	}
	state, e := reopened.snapshotE()
	if e != nil || len(state.Providers) != 17 {
		t.Fatalf("lost concurrent state: %d %v", len(state.Providers), e)
	}
	if e = first.update(func(st *State) error { delete(st.Providers, "parallel-0"); return fmt.Errorf("abort") }); e == nil {
		t.Fatal("rollback missing")
	}
	state, _ = second.snapshotE()
	if _, ok := state.Providers["parallel-0"]; !ok {
		t.Fatal("rolled back data deleted")
	}
}
func TestIndependentAgentModelKeys(t *testing.T) {
	s := testServer(t)
	var mu sync.Mutex
	received := map[string]string{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		input := b.Messages[len(b.Messages)-1].Content
		mu.Lock()
		received[input] = r.Header.Get("Authorization")
		mu.Unlock()
		jsonReply(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}}}})
	}))
	defer remote.Close()
	p := obj[Provider](t, req(t, s, "POST", "providers", Provider{Name: "shared endpoint", Type: "openai", Model: "test", BaseURL: remote.URL}, 201))
	badP := p
	badP.Key = "not-allowed"
	req(t, s, "PUT", "providers/"+p.ID, badP, 400)
	a := obj[Agent](t, req(t, s, "POST", "agents", Agent{Name: "A", Description: "a", Kind: "managed", ProviderID: p.ID, Prompt: "a", Key: "key-A"}, 201))
	b := obj[Agent](t, req(t, s, "POST", "agents", Agent{Name: "B", Description: "b", Kind: "managed", ProviderID: p.ID, Prompt: "b", Key: "key-B"}, 201))
	c := obj[Agent](t, req(t, s, "POST", "agents", Agent{Name: "C", Description: "c", Kind: "managed", ProviderID: p.ID, Prompt: "c"}, 201))
	for input, agent := range map[string]Agent{"a": a, "b": b, "c": c} {
		waitRun(t, s, invokeTest(t, s, "agents", agent.ID, input).ID)
	}
	a.Key = "key-A-rotated"
	a = obj[Agent](t, req(t, s, "PUT", "agents/"+a.ID, a, 200))
	waitRun(t, s, invokeTest(t, s, "agents", a.ID, "a2").ID)
	waitRun(t, s, invokeTest(t, s, "agents", b.ID, "b2").ID)
	b.ClearKey = true
	b = obj[Agent](t, req(t, s, "PUT", "agents/"+b.ID, b, 200))
	waitRun(t, s, invokeTest(t, s, "agents", b.ID, "b3").ID)
	mu.Lock()
	defer mu.Unlock()
	for input, want := range map[string]string{"a": "Bearer key-A", "b": "Bearer key-B", "c": "", "a2": "Bearer key-A-rotated", "b2": "Bearer key-B", "b3": ""} {
		if received[input] != want {
			t.Errorf("%s got %q want %q", input, received[input], want)
		}
	}
	if b.HasKey {
		t.Fatal("clear credential not reflected")
	}
}
func TestLegacyProviderKeyMigration(t *testing.T) {
	s := testServer(t)
	p, a := setup(t, s)
	b := a
	b.ID = "second-agent"
	b.Name = "B"
	secret, e := s.store.encrypt("legacy-key")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.store.update(func(st *State) error { st.Agents[b.ID] = b; st.Secrets[p.ID] = secret; return nil }); e != nil {
		t.Fatal(e)
	}
	reopened, e := openStore(s.store.path, bytes.Repeat([]byte{7}, 32))
	if e != nil {
		t.Fatal(e)
	}
	st := reopened.snapshot()
	if st.Secrets[p.ID] != "" || !st.Agents[a.ID].HasKey || !st.Agents[b.ID].HasKey {
		t.Fatal("legacy credentials not independently migrated")
	}
	for _, id := range []string{a.ID, b.ID} {
		key, e := reopened.decrypt(st.Secrets[id])
		if e != nil || key != "legacy-key" {
			t.Fatal("credential lost")
		}
	}
}

var _ = context.Background
