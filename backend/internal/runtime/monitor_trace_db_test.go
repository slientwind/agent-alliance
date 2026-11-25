package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMonitorTraceChecksProjectAndDiagnosticAccess(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	catalog := resources.Catalog{DB: db, Vault: vault}
	seed, _ := seedLookupTurn(t, ctx, Activities{Catalog: catalog}, "https://model.example.test")
	var project, owner string
	if err := db.QueryRow(ctx, `select p.id,p.owner from runs r join projects p on p.id=r.project_id where r.id=$1`, seed.Run).
		Scan(&project, &owner); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(mux, identity.Server{DB: db})
	other := uuid.NewString()
	lookupTestSQL(t, ctx, db, `insert into members(id,email,display_name,password_hash,role) values($1,$2,'Trace observer','','admin')`, other, other+"@example.test")
	ownerToken, observerToken := web.Token(), web.Token()
	lookupTestSQL(t, ctx, db, `insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '20 minutes'),($3,$4,now()+interval '20 minutes')`,
		web.Hash(ownerToken), owner, web.Hash(observerToken), other)
	lookupTestSQL(t, ctx, db, `insert into external_agent_receipts(request_key,run_id,turn,attempt,operation,cost_disclosure,usage,trace_url)
		values($1,$2,1,1,'submit','reported','{"totalTokens":123}','https://diagnostics.example.test/private')`, uuid.NewString(), seed.Run)

	check := func(token, suffix string, status int, field string) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, "GET", "/api/admin/monitor/trace/"+seed.Run+suffix, nil)
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		mux.ServeHTTP(w, r)
		if w.Code != status || !strings.Contains(w.Body.String(), field) {
			t.Fatalf("got %d with %s, want %d and %q", w.Code, w.Body.String(), status, field)
		}
		return w.Body.String()
	}
	check(ownerToken, "", http.StatusOK, `"gatewaySpans":[]`)
	check(ownerToken, "", http.StatusOK, `"totalTokens":123`)
	check(observerToken, "", http.StatusNotFound, "")
	lookupTestSQL(t, ctx, db, `insert into project_members(project_id,member_id) values($1,$2)`, project, other)
	check(observerToken, "", http.StatusForbidden, "")
	lookupTestSQL(t, ctx, db, `insert into trace_grants(run_id,member_id,scope,expires_at) values($1,$2,'structure',now()+interval '5 minutes')`, seed.Run, other)
	visible := check(observerToken, "", http.StatusOK, `"structure":true`)
	if strings.Contains(visible, "totalTokens") || strings.Contains(visible, "diagnostics.example.test") {
		t.Fatalf("structure grant exposed external usage or trace content: %s", visible)
	}
	check(observerToken, "?mode=content&step=1", http.StatusForbidden, "")
	lookupTestSQL(t, ctx, db, `update runs set trace_expires_at=clock_timestamp()-interval '1 second' where id=$1`, seed.Run)
	check(ownerToken, "", http.StatusGone, "")
}
