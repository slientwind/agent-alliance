package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSearchProvidersOnlyReturnBoundedLinks(t *testing.T) {
	for _, tt := range []struct{ provider, json string }{
		{Tavily, `{"results":[{"title":"Public report","url":"https://example.test/report","content":"summary"},
			{"url":"javascript:alert(1)"},{"url":"https://user:pass@example.test/report"},{"url":"https://example.test/report"}]}`},
		{Bocha, `{"code":200,"data":{"webPages":{"value":[{"name":"Public report","url":"https://example.test/report","snippet":"summary"},
			{"url":"file:///private"},{"url":"https://user:pass@example.test/report"},{"url":"https://example.test/report"}]}}}`},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			items, err := parseResults(tt.provider, []byte(tt.json))
			if err != nil || len(items) != 1 || items[0] != (Result{Title: "Public report", URL: "https://example.test/report", Snippet: "summary"}) {
				t.Fatalf("unexpected results: %+v %v", items, err)
			}
			if _, err := parseResults(tt.provider, []byte(`{"code":500,"message":"secret upstream error"}`)); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("upstream status was not mapped to a safe error: %v", err)
			}
		})
	}
	for _, query := range []string{"", "secret\nmore", strings.Repeat("文", 321)} {
		if ValidQuery(query) {
			t.Fatalf("unsafe query accepted: %q", query)
		}
	}
}

func TestSearchAdminScopesCredentialAndProbesBothProviders(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a9", 32))
	if err != nil {
		t.Fatal(err)
	}
	admin := identity.Member{ID: uuid.NewString()}
	for _, fixture := range []struct{ id, role string }{{admin.ID, "admin"}, {uuid.NewString(), "member"}} {
		_, err = db.Exec(ctx, `insert into members(id,email,display_name,password_hash,role)
			values($1,$2,'search fixture','',$3)`, fixture.id, fixture.id+"@example.test", fixture.role)
		if err != nil {
			t.Fatal(err)
		}
	}
	var requests atomic.Int32
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var in struct {
			Query       string `json:"query"`
			MaxResults  int    `json:"max_results"`
			Count       int    `json:"count"`
			SearchDepth string `json:"search_depth"`
		}
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer key-"+strings.TrimPrefix(r.URL.Path, "/") {
			http.Error(w, "private upstream error", http.StatusUnauthorized)
			return
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Query != "weather" {
			t.Errorf("unexpected probe query: %+v", in)
		}
		switch r.URL.Path {
		case "/tavily":
			if in.MaxResults != resultLimit || in.SearchDepth != "basic" {
				t.Errorf("unexpected Tavily options: %+v", in)
			}
			fmt.Fprint(w, `{"results":[]}`)
		case "/bocha":
			if in.Count != resultLimit {
				t.Errorf("unexpected Bocha options: %+v", in)
			}
			fmt.Fprint(w, `{"code":200,"data":{"_type":"SearchResponse","webPages":{"value":[]}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixture.Close()
	address, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(ctx, `insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])`, address.Hostname(), port)
	if err != nil {
		t.Fatal(err)
	}
	service := Server{DB: db, Client: Client{Vault: vault, HTTP: fixture.Client(), Egress: egress.Policy{DB: db}}}
	settingsFor := func() settings {
		t.Helper()
		w := httptest.NewRecorder()
		if err := service.settings(w, httptest.NewRequestWithContext(ctx, "GET", "/", nil), admin); err != nil {
			t.Fatal(err)
		}
		var config settings
		if err := json.Unmarshal(w.Body.Bytes(), &config); err != nil || strings.Contains(w.Body.String(), "key-") {
			t.Fatalf("settings exposed a credential or invalid JSON")
		}
		return config
	}
	put := func(provider, endpoint, key string, revision int, actor identity.Member) error {
		body := map[string]any{"provider": provider, "endpoint": endpoint, "expected": revision, "enabled": true}
		if key != "" {
			body["key"] = key
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		return service.configure(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "PUT", "/", strings.NewReader(string(raw))), actor)
	}
	for _, provider := range []string{Tavily, Bocha} {
		before := settingsFor()
		endpoint := fixture.URL + "/" + provider
		if provider != before.Provider && put(provider, endpoint, "", before.Revision, admin) == nil {
			t.Fatal("changing provider reused a previous key")
		}
		if put(provider, endpoint, "key-"+provider, before.Revision, identity.Member{}) == nil {
			t.Fatal("unauthorized search configuration was accepted")
		}
		if err := put(provider, endpoint, "key-"+provider, before.Revision, admin); err != nil {
			t.Fatal(err)
		}
		if err := put(provider, endpoint, "", before.Revision, admin); err == nil {
			t.Fatal("stale revision changed configuration")
		}
		if err := service.probe(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, "POST", "/", nil), admin); err != nil {
			t.Fatal(err)
		}
		current := settingsFor()
		if current.Health != "ready" || current.CheckedAt == nil || !current.HasCredential || current.Revision != before.Revision+1 {
			t.Fatalf("unexpected persisted probe result: %+v", current)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("unexpected probe requests: %d", requests.Load())
	}
	if _, err := db.Exec(ctx, `update outbound_host_allowlist set enabled=false where (host,port)=($1,$2)`, address.Hostname(), port); err != nil {
		t.Fatal(err)
	}
	probe := httptest.NewRecorder()
	if err := service.probe(probe, httptest.NewRequestWithContext(ctx, "POST", "/", nil), admin); err != nil ||
		!strings.Contains(probe.Body.String(), "unavailable") || requests.Load() != 2 {
		t.Fatalf("revoked egress reached provider: %s %v", probe.Body, err)
	}
	if err := put(Bocha, fixture.URL+"/bocha", "", settingsFor().Revision, admin); err != (web.Problem{Status: 422, Message: "请先在出站白名单授权该搜索服务"}) {
		t.Fatalf("expected inactive destination error, got %v", err)
	}
}
