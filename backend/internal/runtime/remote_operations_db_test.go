package runtime

import (
	"alliance/internal/channels"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	var supplements atomic.Int32
	supplementIDs := make(chan string, 16)
	var wrongTask atomic.Bool
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rpc struct {
			ID, Method string
			Params     struct {
				ID      string
				Message struct {
					MessageID string
					TaskID    string
					ContextID string
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
		waitingTask := func(id, questionMessage string) map[string]any {
			waiting := task(id, "input-required")
			waiting["status"] = map[string]any{"state": "input-required", "message": map[string]any{
				"messageId": questionMessage,
				"parts":     []map[string]string{{"kind": "text", "text": "请补充供应商的报价有效期"}},
			}}
			return waiting
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
			switch {
			case rpc.Params.Message.TaskID != "":
				if !strings.HasPrefix(rpc.Params.Message.TaskID, "input-") || rpc.Params.Message.ContextID != "fixture" {
					http.Error(w, "wrong original task", http.StatusBadRequest)
					return
				}
				supplements.Add(1)
				supplementIDs <- rpc.Params.Message.MessageID
				if input == "fixture lost response" {
					http.Error(w, "lost response", http.StatusServiceUnavailable)
					return
				}
				if input == "fixture ask again" {
					result = waitingTask(rpc.Params.Message.TaskID, "revised-question")
					break
				}
				completed := task(rpc.Params.Message.TaskID, "completed")
				completed["artifacts"] = []map[string]any{{"parts": text}}
				result = completed
			case strings.HasPrefix(input, "needs-input"):
				result = waitingTask("input-"+rpc.Params.Message.MessageID, "initial-question")
			case strings.HasPrefix(input, "background"):
				result = task("remote-"+rpc.Params.Message.MessageID, "working")
			default:
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
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	feishu := channels.Feishu{DB: db, Vault: vault, App: "fixture-app", Secret: "fixture-secret",
		Encrypt: "fixture-encryption", Verify: "fixture-verification", Tenant: "fixture-remote-inputs",
		Bot: "ou_fixture_bot", Public: "https://workspace.example"}
	feishu.Routes(routes, auth)
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

	decode(call("POST", "/api/agents/cards", map[string]any{"team": team, "card": card}, 201), &agent)
	path = "/api/agents/" + agent.ID
	trial := startTrial(1, "needs-input qualification", "remote")
	if evidence := call("GET", path+"/evidence", nil, 200); !strings.Contains(string(evidence), "请补充供应商的报价有效期") {
		t.Fatalf("trial question missing: %s", evidence)
	}
	trialInput := map[string]any{"action": "supplement", "inputRevision": 1, "input": "有效期至月底", "authorizeOneCall": true}
	supplementsBefore := supplements.Load()
	call("POST", path+"/evidence/"+trial+"/receipt", trialInput, 200)
	call("POST", path+"/evidence/"+trial+"/receipt", trialInput, 200)
	if supplements.Load() != supplementsBefore+1 {
		t.Fatal("trial supplement was dispatched twice")
	}
	if proof := operations(1); !proof.Evidence.Passed || !proof.Evidence.RemoteOperations.CanReturnSupplementText() {
		t.Fatalf("original-task continuation was not verifiable: %+v", proof)
	}
	followTrial(startTrial(1, "background polling proof", "remote"), "check", "passed")
	publish(1, 204)

	type inputReceipt struct{ ID, Status string }
	enqueueAtQuestion := func(step Step, key string, control, question int, input string, authorized bool, want int) inputReceipt {
		t.Helper()
		body, err := json.Marshal(map[string]any{"expected": control, "inputRevision": question, "input": input, "authorizeOneCall": authorized})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("POST", "/api/runs/"+step.Run+"/remote/1/inputs", strings.NewReader(string(body)))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("enqueue: %d want %d, %s", response.Code, want, response.Body)
		}
		var receipt inputReceipt
		if want == 202 {
			decode(response.Body.Bytes(), &receipt)
		}
		return receipt
	}
	enqueue := func(step Step, key string, control int, input string, authorized bool, want int) inputReceipt {
		t.Helper()
		return enqueueAtQuestion(step, key, control, 1, input, authorized, want)
	}
	sendInput := func() bool {
		t.Helper()
		sentInput, err := worker.sendRemoteInput(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return sentInput
	}
	supplementRun, supplementProject := run(1, "needs-input actual run")
	dispatch(supplementRun, true)
	projectSnapshot := call("GET", "/api/projects/"+supplementProject, nil, 200)
	if !strings.Contains(string(projectSnapshot), "请补充供应商的报价有效期") {
		t.Fatal("production question missing")
	}
	expected, key := controlRevision(supplementRun), web.Token()
	enqueue(supplementRun, key, expected, "有效期至月底", false, 422)
	initial := enqueue(supplementRun, key, expected, "有效期至月底", true, 202)
	replay := enqueue(supplementRun, key, expected, "有效期至月底", true, 202)
	enqueue(supplementRun, key, expected, "不要使用旧报价", true, 409)
	enqueue(supplementRun, web.Token(), controlRevision(supplementRun), "有效期至下周", true, 409)
	if initial.ID == "" || initial != replay || supplements.Load() != supplementsBefore+1 {
		t.Fatalf("queued input was not idempotent or reached provider before worker: %+v %+v", initial, replay)
	}
	var storedState string
	var sealed []byte
	var costCount int
	if err := db.QueryRow(ctx, "select state,encrypted_body from remote_task_inputs where id=$1", initial.ID).
		Scan(&storedState, &sealed); err != nil || storedState != "queued" || len(sealed) == 0 || strings.Contains(string(sealed), "有效期") {
		t.Fatalf("queued content did not stay protected: %s, %v", storedState, err)
	}
	if !sendInput() || sendInput() {
		t.Fatal("worker did not consume exactly one supplement")
	}
	<-supplementIDs // The trial message has its own operation identity.
	if actualID := <-supplementIDs; actualID != initial.ID {
		t.Fatalf("provider received unexpected operation identity %s", actualID)
	}
	if after := enqueue(supplementRun, key, expected, "有效期至月底", true, 202); after.Status != "received" {
		t.Fatalf("replay lost the original receipt: %+v", after)
	}
	dispatch(supplementRun, false)
	if err := db.QueryRow(ctx, "select count(*) from spend where run_id=$1 and kind='remote-input'", supplementRun.Run).Scan(&costCount); err != nil || costCount != 1 {
		t.Fatalf("supplement accounting: %d, %v", costCount, err)
	}

	reprompt, _ := run(1, "needs-input reprompt")
	dispatch(reprompt, true)
	enqueue(reprompt, web.Token(), controlRevision(reprompt), "fixture ask again", true, 202)
	if !sendInput() {
		t.Fatal("first input was not sent")
	}
	var questionRevision int
	if err := db.QueryRow(ctx, "select input_revision from remote_jobs where run_id=$1", reprompt.Run).
		Scan(&questionRevision); err != nil || questionRevision != 2 {
		t.Fatalf("new question with unchanged text was not distinguished: %d %v", questionRevision, err)
	}
	enqueue(reprompt, web.Token(), controlRevision(reprompt), "旧卡补件", true, 409)
	enqueueAtQuestion(reprompt, web.Token(), controlRevision(reprompt), 2, "再次核对后补充", true, 202)
	if !sendInput() || sendInput() {
		t.Fatal("second question did not accept exactly one new supplement")
	}
	dispatch(reprompt, false)

	lost, _ := run(1, "needs-input lost response")
	dispatch(lost, true)
	uncertain := enqueue(lost, web.Token(), controlRevision(lost), "fixture lost response", true, 202)
	if !sendInput() || sendInput() {
		t.Fatal("missing receipt was re-sent")
	}
	if err := db.QueryRow(ctx, "select state,encrypted_body from remote_task_inputs where id=$1", uncertain.ID).
		Scan(&storedState, &sealed); err != nil || storedState != "uncertain" || sealed != nil {
		t.Fatalf("lost input response: %s, %v", storedState, err)
	}
	call("POST", "/api/runs/"+lost.Run+"/remote/1/check", map[string]any{"expected": controlRevision(lost), "reason": "核对原任务"}, 202)
	sendsBefore, queriesBefore = sent.Load(), queried.Load()
	if checked, err := worker.checkRemote(ctx); !checked || err != nil {
		t.Fatalf("original query failed: %t %v", checked, err)
	}
	dispatch(lost, false)
	if sent.Load() != sendsBefore || queried.Load() != queriesBefore+1 {
		t.Fatal("reconciliation must query the original task without re-sending")
	}

	interrupted, _ := run(1, "needs-input interrupted worker")
	dispatch(interrupted, true)
	submittedInput := enqueue(interrupted, web.Token(), controlRevision(interrupted), "等待另一执行进程", true, 202)
	// Simulate a worker exit after the committed claim, before a receipt can be saved.
	if _, err := db.Exec(ctx, `with claimed as (
		update remote_task_inputs set state='submitted',encrypted_body=null,claim_until=now()-interval '1 second'
		where id=$1 returning run_id,ordinal,id,claim_until
	) update remote_jobs j set lease=c.id,lease_until=c.claim_until
	from claimed c where (j.run_id,j.ordinal)=(c.run_id,c.ordinal)`, submittedInput.ID); err != nil {
		t.Fatal(err)
	}
	sendsBefore = sent.Load()
	if !sendInput() || sendInput() || sent.Load() != sendsBefore {
		t.Fatal("worker recovery replayed a claimed supplement")
	}
	if err := db.QueryRow(ctx, "select state,encrypted_body from remote_task_inputs where id=$1", submittedInput.ID).
		Scan(&storedState, &sealed); err != nil || storedState != "uncertain" || sealed != nil {
		t.Fatalf("expired input claim: %s, %v", storedState, err)
	}
	if phase, detail := remoteStatus(interrupted); phase != "manual" || detail != "remote_input_unconfirmed" {
		t.Fatalf("expired input was not left for reconciliation: %s %s", phase, detail)
	}

	cancelledInput, _ := run(1, "needs-input cancelled worker")
	dispatch(cancelledInput, true)
	cancelledReceipt := enqueue(cancelledInput, web.Token(), controlRevision(cancelledInput), "撤销前保存的补件", true, 202)
	sendsBefore = sent.Load()
	cancelRun(cancelledInput)
	if sendInput() || sent.Load() != sendsBefore {
		t.Fatal("cancellation permitted dispatch of queued input")
	}
	if err := db.QueryRow(ctx, "select state,encrypted_body from remote_task_inputs where id=$1", cancelledReceipt.ID).
		Scan(&storedState, &sealed); err != nil || storedState != "rejected" || sealed != nil {
		t.Fatalf("cancelled input left pending data: %s, %v", storedState, err)
	}

	const openID = "ou_fixture_remote_operator"
	lookupTestSQL(t, ctx, db, "insert into channel_links(tenant,open_id,member_id) values($1,$2,$3)", feishu.Tenant, openID, actor)
	chat := "oc_original_conversation"
	sendFeishu := func(chatID, text string) (note, kind, arg string) {
		t.Helper()
		event := "event-" + uuid.NewString()
		request := signedRemoteInputFeishuEvent(t, feishu, openID, chatID, text, event)
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("Feishu event: %d %s", response.Code, response.Body)
		}
		if err := db.QueryRow(ctx, "select note,reply_kind,argument from channel_replies where event_id=$1", event).
			Scan(&note, &kind, &arg); err != nil {
			t.Fatal(err)
		}
		return note, kind, arg
	}
	proposeFeishu := func(text string) string {
		t.Helper()
		note, kind, arg := sendFeishu(chat, "远端补件 #1 "+text)
		if kind != "remote_input_preview" || arg == "" || strings.Contains(note, text) {
			t.Fatalf("invalid Feishu preview: %q %q %q", note, kind, arg)
		}
		return arg
	}
	webFirst, projectForWeb := run(1, "needs-input web confirms first")
	dispatch(webFirst, true)
	sendFeishu(chat, "切换 "+projectForWeb)
	webFirstPreview := proposeFeishu("准备在飞书发送")
	verifyFeishuRemoteInputPreview(t, ctx, feishu, webFirstPreview, projectForWeb)
	enqueue(webFirst, web.Token(), controlRevision(webFirst), "在网页确认本次补件", true, 202)
	if note, _, _ := sendFeishu(chat, "确认远端补件 "+webFirstPreview); !strings.Contains(note, "补件状态") {
		t.Fatalf("Feishu failed to reject the stale web-confirmed question: %q", note)
	}
	if !sendInput() || sendInput() {
		t.Fatal("web and Feishu produced duplicate work")
	}
	dispatch(webFirst, false)

	feishuFirst, projectForFeishu := run(1, "needs-input feishu confirms first")
	dispatch(feishuFirst, true)
	sendFeishu(chat, "切换 "+projectForFeishu)
	stalePreview := proposeFeishu("旧会话补件")
	sendFeishu(chat, "切换 "+projectForWeb)
	sendFeishu(chat, "切换 "+projectForFeishu)
	if note, _, _ := sendFeishu(chat, "确认远端补件 "+stalePreview); !strings.Contains(note, "预览已失效") {
		t.Fatalf("leaving and returning to the project restored a stale confirmation: %q", note)
	}
	sensitive := "已经和合作方确认，本月底为有效期"
	preview := proposeFeishu(sensitive)
	if note, _, _ := sendFeishu("oc_other_chat", "确认远端补件 "+preview); !strings.Contains(note, "预览已失效") {
		t.Fatalf("confirmation escaped its private chat: %q", note)
	}
	if err := db.QueryRow(ctx, "select encrypted_body from channel_remote_input_proposals where id=$1", preview).Scan(&sealed); err != nil || len(sealed) == 0 || strings.Contains(string(sealed), sensitive) {
		t.Fatalf("Feishu preview stored readable request text: %v", err)
	}
	beforeConfirm := controlRevision(feishuFirst)
	sendsBefore = sent.Load()
	for range 2 {
		if note, _, _ := sendFeishu(chat, "确认远端补件 "+preview); !strings.Contains(note, "已受理") {
			t.Fatalf("Feishu did not return the same accepted state: %q", note)
		}
	}
	enqueue(feishuFirst, web.Token(), beforeConfirm, "试图重发", true, 409)
	if sent.Load() != sendsBefore || controlRevision(feishuFirst) != beforeConfirm+1 {
		t.Fatal("duplicate confirmation dispatched early or changed the control revision")
	}
	if err := db.QueryRow(ctx, "select encrypted_body from channel_remote_input_proposals where id=$1", preview).
		Scan(&sealed); err != nil || sealed != nil {
		t.Fatalf("confirmation retained the preview body: %v", err)
	}
	if !sendInput() || sendInput() || sent.Load() != sendsBefore+1 {
		t.Fatal("channel input did not dispatch exactly once")
	}
	dispatch(feishuFirst, false)
	if note, _, _ := sendFeishu(chat, "确认远端补件 "+preview); !strings.Contains(note, "回执已记录") {
		t.Fatalf("repeat confirmation lost the receipt: %q", note)
	}

	competing, projectCompeting := run(1, "needs-input simultaneous channels")
	dispatch(competing, true)
	sendFeishu(chat, "切换 "+projectCompeting)
	competingPreview := proposeFeishu("飞书与网页同时确认")
	body, err := json.Marshal(map[string]any{"expected": controlRevision(competing), "inputRevision": 1,
		"input": "确认网页版补件", "authorizeOneCall": true})
	if err != nil {
		t.Fatal(err)
	}
	webRequest := httptest.NewRequest("POST", "/api/runs/"+competing.Run+"/remote/1/inputs", strings.NewReader(string(body)))
	webRequest.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
	webRequest.Header.Set("Idempotency-Key", web.Token())
	feishuRequest := signedRemoteInputFeishuEvent(t, feishu, openID, chat, "确认远端补件 "+competingPreview, "event-"+uuid.NewString())
	codes := make(chan int, 2)
	for _, request := range []*http.Request{webRequest, feishuRequest} {
		go func() {
			recorder := httptest.NewRecorder()
			routes.ServeHTTP(recorder, request)
			codes <- recorder.Code
		}()
	}
	codeA, codeB := <-codes, <-codes
	if codeA != 200 {
		codeA, codeB = codeB, codeA
	}
	if codeA != 200 || (codeB != 202 && codeB != 409) {
		t.Fatalf("competing confirmations failed: %d %d", codeA, codeB)
	}
	var queued int
	if err := db.QueryRow(ctx, "select count(*) from remote_task_inputs where run_id=$1", competing.Run).
		Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("competing confirmations produced %d commands: %v", queued, err)
	}
	if !sendInput() || sendInput() {
		t.Fatal("competing confirmations did not consume exactly one command")
	}
	dispatch(competing, false)

	unlinked, projectUnlinked := run(1, "needs-input Feishu unlinked during queue")
	dispatch(unlinked, true)
	sendFeishu(chat, "切换 "+projectUnlinked)
	unlinkedPreview := proposeFeishu("解绑前已确认")
	sendFeishu(chat, "确认远端补件 "+unlinkedPreview)
	call("DELETE", "/api/channels/feishu/bind", nil, 204)
	sendsBefore = sent.Load()
	if !sendInput() || sendInput() || sent.Load() != sendsBefore {
		t.Fatal("removed Feishu binding permitted dispatch")
	}
	if err := db.QueryRow(ctx, "select state,encrypted_body from remote_task_inputs where run_id=$1", unlinked.Run).
		Scan(&storedState, &sealed); err != nil || storedState != "rejected" || sealed != nil {
		t.Fatalf("removed Feishu binding left protected input pending: %s, %v", storedState, err)
	}

	revoke, _ := run(1, "needs-input before proof withdrawal")
	dispatch(revoke, true)
	waitingInput := enqueue(revoke, web.Token(), controlRevision(revoke), "之前已授权的补件", true, 202)
	call("POST", path+"/evidence/"+trial+"/withdraw", map[string]string{"reason": "连接协议发生变动"}, 204)
	sendsBefore = sent.Load()
	if !sendInput() || sent.Load() != sendsBefore {
		t.Fatal("revoked supplement proof permitted a new dispatch")
	}
	if err := db.QueryRow(ctx, "select state,encrypted_body from remote_task_inputs where id=$1", waitingInput.ID).
		Scan(&storedState, &sealed); err != nil || storedState != "rejected" || sealed != nil {
		t.Fatalf("withdrawn authorization left pending data: %s, %v", storedState, err)
	}
}

type feishuFixtureTransport func(*http.Request) (*http.Response, error)

func (transport feishuFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func verifyFeishuRemoteInputPreview(t *testing.T, ctx context.Context, f channels.Feishu, preview, project string) {
	t.Helper()
	seen := make(chan string, 8)
	original := http.DefaultTransport
	http.DefaultTransport = feishuFixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "open.feishu.cn" {
			return original.RoundTrip(r)
		}
		response := `{"code":0,"tenant_access_token":"fixture-token"}`
		if strings.HasSuffix(r.URL.Path, "/reply") {
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(payload["content"]), &content); err != nil {
				return nil, err
			}
			select {
			case seen <- content["text"]:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(response)), Request: r}, nil
	})
	notifyCtx, stop := context.WithTimeout(ctx, 8*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Notify(notifyCtx)
	}()
	defer func() {
		stop()
		<-done
		http.DefaultTransport = original
	}()
	for {
		select {
		case text := <-seen:
			if !strings.Contains(text, "确认远端补件 "+preview) {
				continue
			}
			if !strings.Contains(text, "第三方材料核对") || !strings.Contains(text, "费用待核算") ||
				!strings.Contains(text, "/#"+project) || strings.Contains(text, "准备在飞书发送") ||
				strings.Contains(text, "请补充供应商的报价有效期") {
				t.Fatalf("unexpected preview delivered to Feishu: %q", text)
			}
			return
		case <-notifyCtx.Done():
			t.Fatal("Feishu notifier did not deliver the scoped confirmation preview")
		}
	}
}

func signedRemoteInputFeishuEvent(t *testing.T, f channels.Feishu, actor, chat, text, event string) *http.Request {
	t.Helper()
	marshal := func(value any) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	now := time.Now()
	payload := marshal(map[string]any{
		"header": map[string]string{"app_id": f.App, "tenant_key": f.Tenant, "event_id": event, "token": f.Verify, "event_type": "im.message.receive_v1"},
		"event": map[string]any{
			"sender": map[string]any{"sender_type": "user", "sender_id": map[string]string{"open_id": actor}},
			"message": map[string]string{"message_id": "message-" + event, "chat_id": chat, "chat_type": "p2p", "message_type": "text",
				"content": string(marshal(map[string]string{"text": text})), "create_time": strconv.FormatInt(now.UnixMilli(), 10)},
		},
	})
	key := sha256.Sum256([]byte(f.Encrypt))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	n := aes.BlockSize
	encrypted := make([]byte, n+len(payload)+(n-len(payload)%n))
	if _, err := rand.Read(encrypted[:n]); err != nil {
		t.Fatal(err)
	}
	copy(encrypted[n:], payload)
	for at := n + len(payload); at < len(encrypted); at++ {
		encrypted[at] = byte(len(encrypted) - n - len(payload))
	}
	cipher.NewCBCEncrypter(block, encrypted[:n]).CryptBlocks(encrypted[n:], encrypted[n:])
	raw := marshal(map[string]string{"encrypt": base64.StdEncoding.EncodeToString(encrypted)})
	ts, nonce := strconv.FormatInt(now.Unix(), 10), uuid.NewString()
	signature := sha256.Sum256(append([]byte(ts+nonce+f.Encrypt), raw...))
	req := httptest.NewRequest("POST", "/api/channels/feishu/events", strings.NewReader(string(raw)))
	req.Header.Set("X-Lark-Request-Timestamp", ts)
	req.Header.Set("X-Lark-Request-Nonce", nonce)
	req.Header.Set("X-Lark-Signature", hex.EncodeToString(signature[:]))
	return req
}
