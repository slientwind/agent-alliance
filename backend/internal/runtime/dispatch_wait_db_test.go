package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStepDispatchWaitsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("ce", 32))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var duringRequest atomic.Bool
	var revokedAgent string
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if duringRequest.Load() {
			if _, err := db.Exec(r.Context(), "update agents set emergency=true where id=$1", revokedAgent); err != nil {
				http.Error(w, "fixture update failed", http.StatusInternalServerError)
				return
			}
		}
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"checked"}}],"usage":{"total_tokens":5}}`))
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)
	a := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	seed, _ := seedLookupTurn(t, ctx, a, provider.URL)
	var project, actor, team, model string
	err = db.QueryRow(ctx, `select r.project_id,r.actor,r.charge_team,v.resource_id
        from runs r join nodes n on(n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1)
        join agent_revisions v on(v.agent_id,v.revision)=(n.agent_id,n.agent_revision)
        where r.id=$1`, seed.Run).Scan(&project, &actor, &team, &model)
	if err != nil {
		t.Fatal(err)
	}
	limitedModel := uuid.NewString()
	limitedKey, err := vault.Seal("fixture", "model/"+limitedModel+"/1")
	if err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "insert into model_resources(id,name,team_id,created_by) values($1,'Limited',$2,$3)", limitedModel, team, actor)
	lookupTestSQL(t, ctx, db, `insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny)
        values($1,1,$2,'fixture',$3,0.2)`, limitedModel, provider.URL, limitedKey)
	lookupTestSQL(t, ctx, db, "insert into plans(project_id,version,brief,confirmed_by) values($1,2,'Check four steps',$2)", project, actor)
	var agents [4]string
	for i, resource := range []string{model, limitedModel, model, model} {
		agent := uuid.NewString()
		agents[i] = agent
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", agent, actor, team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
            values($1,1,'Check evidence','Check evidence',$2,1,$3)`, agent, resource, actor)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", agent, actor)
		lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent)
		lookupTestSQL(t, ctx, db, "insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,2,$2,$3,1)", project, i+1, agent)
	}
	lookupTestSQL(t, ctx, db, "insert into node_edges values($1,2,4,1),($1,2,4,2),($1,2,4,3)", project)
	lookupTestSQL(t, ctx, db, "update projects set plan_version=2 where id=$1", project)
	lookupTestSQL(t, ctx, db, "update agents set emergency=true where id=$1", agents[0])
	lookupTestSQL(t, ctx, db, `update resource_budgets set limit_cny=0 where id=
        (select budget_id from agent_budget_bindings where (agent_id,revision)=($1,1))`, agents[1])

	newRun := func() string {
		id := uuid.NewString()
		lookupTestSQL(t, ctx, db, `insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
            values($1,$2,2,$3,'queued',$4,$5,'fixture',4)`, id, project, actor, team, id)
		return id
	}
	run := newRun()
	if err := a.EnableStepDispatchWaits(ctx, run); err != nil {
		t.Fatal(err)
	}
	advance := func(run string, ordinal int, waiting bool) {
		t.Helper()
		result, err := a.ExecuteStep(ctx, Step{run, ordinal})
		if err != nil || result.Stop || result.Wait != waiting {
			t.Fatalf("step %d: result=%+v err=%v, want waiting %t", ordinal, result, err, waiting)
		}
	}
	advance(run, 1, true)
	advance(run, 2, true)
	if _, err := db.Exec(ctx, "insert into invocations(run_id,ordinal,status) values($1,3,'submitted')", run); err == nil || !strings.Contains(err.Error(), "requires an updated Worker") {
		t.Fatalf("old dispatch writer accepted: %v", err)
	}
	advance(run, 3, false)

	readWait := func(ordinal int, want string) int {
		t.Helper()
		var revision, invocations int
		var reason string
		err := db.QueryRow(ctx, `select w.wait_revision,w.reason,
          (select count(*) from invocations i where (i.run_id,i.ordinal)=(w.run_id,w.ordinal))
          from current_step_dispatch_waits w where (w.run_id,w.ordinal)=($1,$2)`, run, ordinal).
			Scan(&revision, &reason, &invocations)
		if err != nil || invocations != 0 || reason != want {
			t.Fatalf("wait %d: ticket=%d reason=%s attempts=%d err=%v", ordinal, revision, reason, invocations, err)
		}
		return revision
	}
	first, second := readWait(1, "resource_denied"), readWait(2, "budget_denied")
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
	routes := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(routes, identity.Server{DB: db})
	key := uuid.NewString()
	recheck := func(in projects.DispatchRecheckInput, key string, want int) {
		t.Helper()
		body, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/runs/"+run+"/step-dispatch/recheck", bytes.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		routes.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("recheck %+v: status=%d want=%d: %s", in, rec.Code, want, rec.Body)
		}
	}
	detail := skillTestRequest(t, routes, token, "GET", "/api/projects/"+project, nil, 200)
	var projected struct {
		Run struct {
			CanRecheckDispatch bool
			DispatchWaits      []struct{ Reason string }
		}
	}
	if err := json.Unmarshal(detail, &projected); err != nil || !projected.Run.CanRecheckDispatch ||
		len(projected.Run.DispatchWaits) != 2 || projected.Run.DispatchWaits[1].Reason != "budget_denied" {
		t.Fatalf("missing wait in project projection: %s", detail)
	}
	lookupTestSQL(t, ctx, db, "update runs set status='paused',control_revision=2 where id=$1", run)
	stale := projects.DispatchRecheckInput{Step: 1, Expected: 1, WaitRevision: first}
	recheck(stale, key, 409)
	lookupTestSQL(t, ctx, db, "update runs set status='running',control_revision=3 where id=$1", run)
	stale.Expected = 3
	recheck(stale, key, 202)
	advance(run, 1, true)
	replacement := readWait(1, "resource_denied")
	if replacement == first || readWait(2, "budget_denied") != second || calls.Load() != 1 {
		t.Fatal("recheck changed another branch or called with a blocked resource")
	}
	recheck(stale, key, 202)
	recheck(stale, uuid.NewString(), 409)
	lookupTestSQL(t, ctx, db, "update agents set emergency=false where id=$1", agents[0])
	recheck(projects.DispatchRecheckInput{Step: 1, Expected: 3, WaitRevision: replacement}, uuid.NewString(), 202)
	advance(run, 1, false)
	lookupTestSQL(t, ctx, db, `update resource_budgets set limit_cny=1 where id=
        (select budget_id from agent_budget_bindings where (agent_id,revision)=($1,1))`, agents[1])
	recheck(projects.DispatchRecheckInput{Step: 2, Expected: 3, WaitRevision: second}, uuid.NewString(), 202)
	advance(run, 2, false)
	advance(run, 4, false)
	var spend, candidates, wakes int
	err = db.QueryRow(ctx, `select (select count(*) from spend where run_id=$1),
        (select count(*) from artifacts where run_id=$1),
        (select count(*) from controls where run_id=$1 and action='dispatch.step.recheck'
          and revision=3 and ((wake_ordinal=1 and request_key=$2) or (wake_ordinal in (1,2) and request_key<>$2)))`, run, key).
		Scan(&spend, &candidates, &wakes)
	if err != nil || spend != 4 || candidates != 4 || wakes != 3 || calls.Load() != 4 {
		t.Fatalf("unexpected final ledger: spend=%d candidates=%d wakes=%d calls=%d err=%v", spend, candidates, wakes, calls.Load(), err)
	}

	legacy := newRun()
	lookupTestSQL(t, ctx, db, "update agents set emergency=true where id=$1", agents[0])
	advance(legacy, 1, true)
	var state string
	if err := db.QueryRow(ctx, "select status from runs where id=$1", legacy).Scan(&state); err != nil || state != "blocked" {
		t.Fatalf("legacy execution: state=%s err=%v", state, err)
	}
	lookupTestSQL(t, ctx, db, "update agents set emergency=false where id=$1", agents[0])
	inflight := newRun()
	if err := a.EnableStepDispatchWaits(ctx, inflight); err != nil {
		t.Fatal(err)
	}
	revokedAgent = agents[0]
	duringRequest.Store(true)
	advance(inflight, 1, true)
	var waitCount int
	err = db.QueryRow(ctx, `select status,(select count(*) from step_dispatch_waits where run_id=$1)
        from runs where id=$1`, inflight).Scan(&state, &waitCount)
	if err != nil || state != "blocked" || waitCount != 0 || calls.Load() != 5 {
		t.Fatalf("admitted attempt became a dispatch wait: state=%s waits=%d calls=%d err=%v", state, waitCount, calls.Load(), err)
	}
}
