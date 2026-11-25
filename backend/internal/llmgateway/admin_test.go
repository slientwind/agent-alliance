package llmgateway

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGatewayAdminRoutesRequireLogin(t *testing.T) {
	mux := http.NewServeMux()
	(Admin{}).Routes(mux, identity.Server{})
	for _, path := range []string{"tiers", "providers", "pricing"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/admin/models/"+path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s returned %d, want 401", path, w.Code)
		}
	}
}

func TestAdminConfiguresProviderTierAndImmutablePrices(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	vault, err := resources.NewVault(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	actor := identity.Member{ID: uuid.NewString(), Role: "admin", Enabled: true}
	if _, err := tx.Exec(ctx, `insert into members(id,email,display_name,password_hash,role) values($1,$2,'gateway admin','','admin')`, actor.ID, actor.ID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	seenProbe := false
	a := Admin{DB: tx, Vault: vault, Provider: fixtureCaller(func(_ context.Context, model Candidate, in Request) (Result, error) {
		seenProbe = true
		if model.Name != "test-model" || in.MaxTokens != 32 || in.Messages[0].Content != "Reply with OK." {
			t.Fatal("unexpected probe input")
		}
		return Result{}, ProviderError{Status: 429, Retryable: true}
	})}
	check := func(handler identity.Action, request string, paths map[string]string, status int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, "POST", "/api/admin/models/", strings.NewReader(request))
		for k, v := range paths {
			r.SetPathValue(k, v)
		}
		w := httptest.NewRecorder()
		web.Handler(func(w http.ResponseWriter, r *http.Request) error { return handler(w, r, actor) }).ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("got %d, want %d, response=%s", w.Code, status, w.Body.String())
		}
		return w.Body.String()
	}
	readID := func(body string) string {
		t.Helper()
		var data struct{ ID string }
		if err := json.Unmarshal([]byte(body), &data); err != nil || data.ID == "" {
			t.Fatalf("missing id in %s, %v", body, err)
		}
		return data.ID
	}
	provider := readID(check(a.createProvider, `{"name":"Demo","kind":"openai","endpoint":"https://model.example.test/v1/","key":"unit-test-key"}`, nil, 201))
	providerPath := map[string]string{"id": provider}
	providers := check(a.listProviders, "", nil, 200)
	if strings.Contains(providers, "unit-test-key") || !strings.Contains(providers, `"hasKey":true`) {
		t.Fatalf("provider response exposed credentials or omitted state: %s", providers)
	}
	check(a.updateProvider, `{"name":"Demo","kind":"openai","endpoint":"https://different.example.test/v1","expectedRevision":1}`, providerPath, 422)
	check(a.updateProvider, `{"name":"Renamed","kind":"openai","endpoint":"https://model.example.test/v1","expectedRevision":1}`, providerPath, 200)
	check(a.updateProvider, `{"name":"Old","kind":"openai","endpoint":"https://model.example.test/v1","expectedRevision":1}`, providerPath, 409)

	model := readID(check(a.createModel, `{"name":"test-model","capabilities":["text","tool_call"],"contextTokens":4096,"maxOutputTokens":512,"rpm":10}`, providerPath, 201))
	price := `{"modelId":"` + model + `","effectiveAt":"2026-01-01T00:00:00Z","inputPerMillionMicroCny":"9007199254740993","outputPerMillionMicroCny":"2","cachedPerMillionMicroCny":"1","perImageMicroCny":"0"}`
	check(a.createPrice, price, nil, 201)
	check(a.createPrice, price, nil, 409)
	if listed := check(a.listPrices, "", nil, 200); !strings.Contains(listed, `"9007199254740993"`) {
		t.Fatalf("lost integer price precision: %s", listed)
	}
	if got := strings.Count(check(a.listTiers, "", nil, 200), `"tier":`); got != 6 {
		t.Fatalf("listed %d tiers, want 6", got)
	}
	var revision int
	if err := tx.QueryRow(ctx, `select revision from model_tier_settings where tier='fast'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(tierInput{ExpectedRevision: revision, Routes: []routeInput{{ModelID: model}}})
	if err != nil {
		t.Fatal(err)
	}
	check(a.updateTier, string(data), map[string]string{"tier": "vision"}, 422)
	check(a.updateTier, string(data), map[string]string{"tier": "fast"}, 200)
	check(a.updateTier, string(data), map[string]string{"tier": "fast"}, 409)
	if impact := check(a.providerImpact, "", providerPath, 200); !strings.Contains(impact, `"fast"`) {
		t.Fatalf("provider route missing from impact: %s", impact)
	}
	if probe := check(a.probeProvider, `{"modelId":"`+model+`","expectedRevision":2}`, providerPath, 200); !seenProbe || !strings.Contains(probe, `"providerStatus":429`) {
		t.Fatalf("probe did not report sanitized status: %s", probe)
	}
}
