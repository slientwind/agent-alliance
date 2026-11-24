package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRemoteOperationsPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	var sent, queried, canceled atomic.Int32
	var wrongTask atomic.Bool
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID, Method string
			Params     struct {
				ID      string
				Message struct {
					MessageID string
					Parts     []struct{ Text string }
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		text := []map[string]string{{"kind": "text", "text": "MATCH"}}
		task := func(id, state string) map[string]any {
			return map[string]any{"kind": "task", "id": id, "contextId": "fixture", "status": map[string]string{"state": state}}
		}
		var result any
		switch rpc.Method {
		case "message/send":
			sent.Add(1)
			if len(rpc.Params.Message.Parts) == 0 {
				http.Error(w, "fixture needs a prompt", http.StatusBadRequest)
				return
			}
			input := rpc.Params.Message.Parts[0].Text
			if strings.HasPrefix(input, "background") {
				result = task("remote-"+rpc.Params.Message.MessageID, "working")
			} else {
				if strings.HasPrefix(input, "business-fail") {
					text[0]["text"] = "NO EXPECTED RESULT"
				}
				result = map[string]any{"kind": "message", "parts": text}
			}
		case "tasks/get":
			queried.Add(1)
			id := rpc.Params.ID
			if wrongTask.Load() {
				id += "-wrong"
			}
			completed := task(id, "completed")
			completed["artifacts"] = []map[string]any{{"parts": text}}
			result = completed
		case "tasks/cancel":
			canceled.Add(1)
			result = task(rpc.Params.ID, "canceled")
		default:
			http.Error(w, "unexpected method", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	endpoint, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_ALLOWED_HOSTS", endpoint.Host)
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	auth := identity.Server{DB: db}
	routes := http.NewServeMux()
	worker.Catalog.Routes(routes, auth)
	projects.Server{DB: db}.Routes(routes, auth)
	seed, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var actor, team string
	if err := db.QueryRow(ctx, "select actor,charge_team from runs where id=$1", seed.Run).Scan(&actor, &team); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "insert into enterprise(name,default_team) values('Fixture',$1) on conflict do nothing", team)
	lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
	call := func(method, path string, body any, want int) []byte {
		t.Helper()
		return skillTestRequest(t, routes, token, method, path, body, want)
	}
	decode := func(raw []byte, into any) {
		t.Helper()
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
	card := map[string]any{"name": "第三方材料核对", "version": "1", "protocolVersion": "0.3.0",
		"url": provider.URL, "preferredTransport": "JSONRPC", "capabilities": map[string]bool{"streaming": true},
		"skills": []map[string]string{{"id": "quote", "name": "核对报价"}}}
	type revision struct {
		ID       string
		Revision int
	}
	var agent revision
	decode(call("POST", "/api/agents/cards", map[string]any{"team": team, "card": card}, 201), &agent)
	path := "/api/agents/" + agent.ID
	type trialReceipt struct{ ID, Status string }
	startTrial := func(rev int, prompt, want string) string {
		t.Helper()
		var v trialReceipt
		decode(call("POST", path+"/validate", map[string]any{
			"revision": rev, "input": prompt, "expected": "MATCH", "authorizeOneCall": true,
		}, 200), &v)
		if v.Status != want || v.ID == "" {
			t.Fatalf("trial %s: %+v, want %s", prompt, v, want)
		}
		return v.ID
	}
	followTrial := func(id, action, want string) {
		t.Helper()
		var v trialReceipt
		decode(call("POST", path+"/evidence/"+id+"/receipt", map[string]string{"action": action}, 200), &v)
		if v.Status != want {
			t.Fatalf("%s trial: %+v, want %s", action, v, want)
		}
	}
	operations := func(rev int) (view struct {
		Evidence struct {
			Passed           bool
			RemoteOperations resources.RemoteOperations
		}
		PublishedRemoteOperations *resources.RemoteOperations
	}) {
		t.Helper()
		url := path + "/builds/1"
		if rev == 2 {
			url = path + "/builds/2"
		}
		decode(call("GET", url, nil, 200), &view)
		return view
	}
	publish := func(rev, want int) {
		t.Helper()
		call("POST", path+"/publish", map[string]int{"revision": rev}, want)
	}
	startTrial(1, "business-fail", "failed")
	publish(1, 409)
	startTrial(1, "direct", "passed")
	first := operations(1)
	if !first.Evidence.Passed || !first.Evidence.RemoteOperations.DirectText ||
		first.Evidence.RemoteOperations.Query || first.Evidence.RemoteOperations.Cancel {
		t.Fatalf("Card claims affected qualification: %+v", first)
	}
	publish(1, 204)

	run := func(rev int, brief string) (Step, string) {
		t.Helper()
		project, id := uuid.NewString(), uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into projects(id, owner, final_reviewer,team_id,title,stage,plan_version) values($1,$2,$2,$3,'远端核对','running',1)", project, actor, team)
		lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, actor)
		lookupTestSQL(t, ctx, db, "insert into plans(project_id,version,brief,confirmed_by) values($1,1,$2,$3)", project, brief, actor)
		lookupTestSQL(t, ctx, db, "insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,1,1,$2,$3)", project, agent.ID, rev)
		lookupTestSQL(t, ctx, db, "insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'running',$4,$5,'fixture',1)", id, project, actor, team, id)
		return Step{id, 1}, project
	}
	dispatch := func(step Step, wait bool) {
		t.Helper()
		result, err := worker.ExecuteStep(ctx, step)
		if err != nil || result.Wait != wait {
			t.Fatalf("execute %+v: %+v, %v", step, result, err)
		}
	}
	controlRevision := func(step Step) int {
		t.Helper()
		var rev int
		if err := db.QueryRow(ctx, "select control_revision from runs where id=$1", step.Run).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		return rev
	}
	cancelRun := func(step Step) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"expected": controlRevision(step), "action": "cancel", "reason": "停止试验"})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/runs/"+step.Run+"/control", strings.NewReader(string(body)))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", web.Token())
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, req)
		if response.Code != 202 {
			t.Fatalf("cancel: %d %s", response.Code, response.Body)
		}
	}
	remoteStatus := func(step Step) (phase, detail string) {
		t.Helper()
		if err := db.QueryRow(ctx, "select phase,detail from remote_jobs where run_id=$1 and ordinal=$2", step.Run, step.Ordinal).Scan(&phase, &detail); err != nil {
			t.Fatal(err)
		}
		return phase, detail
	}
	unexpected, _ := run(1, "background despite direct")
	dispatch(unexpected, true)
	phase, detail := remoteStatus(unexpected)
	if phase != "manual" || detail != "remote_query_unverified" {
		t.Fatalf("unverified task was scheduled for external query: %s %s", phase, detail)
	}
	if raw := call("POST", "/api/runs/"+unexpected.Run+"/remote/1/check", map[string]any{"expected": controlRevision(unexpected), "reason": "核对"}, 409); !strings.Contains(string(raw), "远端查询未通过验证") {
		t.Fatalf("unqualified manual query rejected for wrong reason: %s", raw)
	}
	cancelRun(unexpected)
	if _, err := worker.checkRemote(ctx); err != nil || queried.Load() != 0 || canceled.Load() != 0 {
		t.Fatalf("unverified query/cancel reached provider: %d %d %v", queried.Load(), canceled.Load(), err)
	}
	newProbe := startTrial(1, "background later probe", "remote")
	followTrial(newProbe, "cancel", "failed")
	if current := operations(1); current.PublishedRemoteOperations == nil || current.PublishedRemoteOperations.Cancel {
		t.Fatalf("published scope widened after another trial: %+v", current)
	}

	decode(call("POST", "/api/agents/cards", map[string]any{"id": agent.ID, "expected": 1, "team": team, "card": card}, 201), &agent)
	async := startTrial(2, "background validated", "remote")
	wrongTask.Store(true)
	followTrial(async, "check", "remote")
	if view := operations(2); view.Evidence.Passed || view.Evidence.RemoteOperations.Query {
		t.Fatalf("unmatched task became evidence: %+v", view)
	}
	wrongTask.Store(false)
	followTrial(async, "check", "passed")
	cancelProof := startTrial(2, "background cancellable", "remote")
	followTrial(cancelProof, "cancel", "failed")
	second := operations(2)
	if !second.Evidence.Passed || !second.Evidence.RemoteOperations.Query || !second.Evidence.RemoteOperations.AsyncText ||
		!second.Evidence.RemoteOperations.Cancel || second.Evidence.RemoteOperations.DirectText {
		t.Fatalf("wrong qualified operation set: %+v", second)
	}
	publish(2, 204)

	production, prodProject := run(2, "background actual run")
	sendsBefore, queriesBefore := sent.Load(), queried.Load()
	dispatch(production, true)
	if checked, err := worker.checkRemote(ctx); !checked || err != nil {
		t.Fatalf("approved query was not executed: %t %v", checked, err)
	}
	dispatch(production, false)
	var deliveredBody string
	if err := db.QueryRow(ctx, "select v.body from artifacts a join artifact_versions v on(v.artifact_id,v.version)=(a.id,a.current_version) where a.project_id=$1", prodProject).Scan(&deliveredBody); err != nil ||
		deliveredBody != "MATCH" || sent.Load() != sendsBefore+1 || queried.Load() != queriesBefore+1 {
		t.Fatalf("remote receipt did not produce one candidate: %q sends=%d queries=%d %v", deliveredBody, sent.Load(), queried.Load(), err)
	}

	cancelExample, _ := run(2, "background cancel")
	dispatch(cancelExample, true)
	cancelRun(cancelExample)
	cancelsBefore := canceled.Load()
	if checked, err := worker.checkRemote(ctx); !checked || err != nil || canceled.Load() != cancelsBefore+1 {
		t.Fatalf("approved cancellation did not reach original task: %t %v", checked, err)
	}
	call("POST", path+"/evidence/"+cancelProof+"/withdraw", map[string]string{"reason": "取消能力已撤回"}, 204)
	revoked, _ := run(2, "background after cancellation withdrawal")
	dispatch(revoked, true)
	cancelRun(revoked)
	cancelsBefore, queriesBefore = canceled.Load(), queried.Load()
	if checked, err := worker.checkRemote(ctx); !checked || err != nil || canceled.Load() != cancelsBefore || queried.Load() != queriesBefore+1 {
		t.Fatalf("withdrawal did not preserve query-only reconciliation: %t %v", checked, err)
	}
	call("POST", path+"/evidence/"+async+"/withdraw", map[string]string{"reason": "结果获取需复验"}, 204)
	blocked, _ := run(2, "background after output withdrawal")
	sendsBefore = sent.Load()
	dispatch(blocked, true)
	if sent.Load() != sendsBefore {
		t.Fatal("withdrawn result proof permitted a new invocation")
	}

	decode(call("POST", "/api/agents/cards", map[string]any{"team": team, "card": card}, 201), &agent)
	path = "/api/agents/" + agent.ID
	// Simulate a revision deployed before operation qualification existed.
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", agent.ID)
	startTrial(1, "direct legacy evidence", "passed")
	publish(1, 409)
	if legacy := operations(1); legacy.PublishedRemoteOperations != nil {
		t.Fatalf("old revision was silently requalified: %+v", legacy)
	}
	legacyRun, _ := run(1, "background legacy")
	dispatch(legacyRun, true)
	if checked, err := worker.checkRemote(ctx); !checked || err != nil {
		t.Fatalf("legacy remote work failed to continue: %t %v", checked, err)
	}
	dispatch(legacyRun, false)
}
