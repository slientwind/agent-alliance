package resources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExternalAdmissionsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := NewVault(strings.Repeat("ce", 32))
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer admission-fixture-key" {
			t.Error("missing fixture authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"admission fixture answer"},"finish_reason":"stop"}]}`)
	}))
	defer provider.Close()
	original := providerClient
	providerClient = func() (*http.Client, error) { return provider.Client(), nil }
	defer func() { providerClient = original }()
	address, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, other := uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text),($2,$2::uuid::text)", team, other)
	exec("insert into enterprise(name,default_team) values('Admission fixture',$1) on conflict do nothing", team)
	exec(`insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])
		on conflict do nothing`, address.Hostname(), port)
	member := func(team, role string) (string, string) {
		t.Helper()
		id, token := uuid.NewString(), web.Token()
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Admission fixture','',$3)", id, id+"@example.test", role)
		exec("insert into team_members(team_id,member_id) values($1,$2)", team, id)
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,$3)", web.Hash(token), id, time.Now().Add(time.Hour))
		return id, token
	}
	owner, ownerToken := member(team, "member")
	tester, testerToken := member(team, "member")
	teammate, teammateToken := member(team, "member")
	_, adminToken := member(other, "admin")
	employee, employeeToken := member(other, "member")
	c := Catalog{DB: db, Vault: vault}
	routes := http.NewServeMux()
	c.Routes(routes, identity.Server{DB: db})
	call := func(token, method, path, body string, status int) string {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d (%s)", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.String()
	}
	var created struct{ ID string }
	err = json.Unmarshal([]byte(call(ownerToken, "POST", "/api/agents/adapters", fmt.Sprintf(`{"team":%q,"endpoint":%q,
		"name":"Admission fixture","description":"Short description","key":"admission-fixture-key",
		"adapter":{"protocol":"openai_compat","template":"openai","model":"fixture"}}`, team, provider.URL), 201)), &created)
	if err != nil || created.ID == "" {
		t.Fatalf("draft creation failed: %v", err)
	}
	id, path := created.ID, "/api/agents/"+created.ID
	assertPhase := func(want string, actors ...string) {
		t.Helper()
		var phase string
		if err := db.QueryRow(ctx, "select external_admission_phase($1,1)", id).Scan(&phase); err != nil || phase != want {
			t.Fatalf("phase = %q, want %q (%v)", phase, want, err)
		}
		for _, actor := range []string{owner, tester, employee, teammate} {
			var wantAccess, allowed bool
			for _, authorized := range actors {
				wantAccess = wantAccess || actor == authorized
			}
			err := db.QueryRow(ctx, "select project_agent_admissible(null,$1,1,$2)", id, actor).Scan(&allowed)
			if err != nil || allowed != wantAccess {
				t.Fatalf("%s permission = %t, want %t (%v)", actor, allowed, wantAccess, err)
			}
		}
	}
	assertPhase("draft")
	testers := fmt.Sprintf(`{"revision":1,"expected":1,"members":[%q]}`, tester)
	call(ownerToken, "PUT", path+"/external-testers", testers, 200)
	call(ownerToken, "PUT", path+"/external-testers", testers, 409)
	test := `{"revision":1,"input":"Smoke test","admissionTest":true,"authorizeOneCall":true}`
	call(teammateToken, "POST", path+"/validate", test, 422)
	if got := call(ownerToken, "POST", path+"/validate", test, 200); !strings.Contains(got, `"status":"passed"`) {
		t.Fatal(got)
	}
	assertPhase("beta", owner, tester)
	if got := call(testerToken, "GET", "/api/agents/admissions", "", 200); !strings.Contains(got, id) || strings.Contains(got, `"testers"`) {
		t.Fatal(got)
	}
	if got := call(employeeToken, "GET", "/api/agents/admissions", "", 200); strings.Contains(got, id) {
		t.Fatal(got)
	}
	exec("delete from team_members where (team_id,member_id)=($1,$2)", team, tester)
	assertPhase("beta", owner)
	build, err := builder.Read(ctx, db, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := builder.EvidenceFor(ctx, db, id, 1, build.Digest)
	if err != nil {
		t.Fatal(err)
	}
	selection := fmt.Sprintf(`{"revision":1,"buildDigest":%q,"evidenceDigest":%q`, build.Digest, proof.Digest)
	call(ownerToken, "POST", path+"/release-requests", `{"revision":1}`, 409)
	var release struct{ ID string }
	err = json.Unmarshal([]byte(call(ownerToken, "POST", path+"/release-requests", selection+`}`, 200)), &release)
	if err != nil || release.ID == "" {
		t.Fatalf("release request failed: %v", err)
	}
	assertPhase("pending_verify", owner)
	queue := call(adminToken, "GET", "/api/admin/external-agents/releases", "", 200)
	if !strings.Contains(queue, release.ID) || !strings.Contains(queue, build.Digest) || strings.Contains(queue, "admission-fixture-key") {
		t.Fatal(queue)
	}
	decision := path + "/release-requests/" + release.ID
	call(adminToken, "POST", decision, `{"revision":1,"action":"publish"}`, 409)
	call(adminToken, "POST", decision, selection+`,"action":"publish"}`, 204)
	assertPhase("verified", owner, tester, employee, teammate)
	model, _, err := c.ResolveRun(ctx, db, "", employee, id, 1)
	if err != nil || !model.External || model.RemoteOperations == nil || !model.RemoteOperations.CanStart() {
		t.Fatalf("verified revision could not be resolved by another team: %v", err)
	}
	if got := call(employeeToken, "GET", "/api/agents/admissions", "", 200); !strings.Contains(got, id) || !strings.Contains(got, `"phase":"verified"`) {
		t.Fatal(got)
	}
}
