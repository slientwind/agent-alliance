package agentloop

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
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

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDirectExternalRejectsExtraContext(t *testing.T) {
	ref := externalRef{ID: "199a6a44-6811-4744-b993-4b81f82f65a0", Revision: 4}
	input := StartInput{Direct: true, Goal: "one explicit message", External: []externalRef{ref}}
	if err := validateExternalSelection(input); err != nil {
		t.Fatal(err)
	}
	input.Sources = []harness.SourceRevision{{ID: ref.ID, Revision: 1}}
	if err := validateExternalSelection(input); err == nil {
		t.Fatal("direct message accepted an attached source")
	}
	input.Sources = nil
	input.SearchQuery = "private"
	if err := validateExternalSelection(input); err == nil {
		t.Fatal("direct message accepted a search query")
	}
}

func TestExternalAgentDirectAndDelegatedPostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("2c", 32))
	if err != nil {
		t.Fatal(err)
	}
	const directInput, wireInput = "only direct message 13800000000", "only direct message [MOBILE_01]"
	const delegatedInput, internalGoal = "approved external request", "private project context"
	const secretTerm = "roadmap-confidential-fixture"
	var sends, polls atomic.Int32
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-external-key" || r.Header.Get("X-Alliance-Run-Id") == "" {
			t.Error("external request is missing its bound credential or trace")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/submit":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 {
				t.Errorf("bad outbound payload: %v %v", body, err)
			}
			switch input := body["input"]; input {
			case "smoke", wireInput, delegatedInput:
				id := fmt.Sprintf("job-%d", sends.Add(1))
				fmt.Fprintf(w, `{"task_id":%q,"status":"working"}`, id)
			default:
				t.Errorf("unauthorized outbound message %q", input)
				w.WriteHeader(http.StatusBadRequest)
			}
		default:
			if !strings.HasPrefix(r.URL.Path, "/poll/job-") {
				t.Errorf("poll omitted the original job: %s", r.URL.Path)
			}
			polls.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/poll/")
			output := "external fixture reply"
			if id == "job-2" {
				output += " [MOBILE_01]"
			}
			fmt.Fprintf(w, `{"task_id":%q,"status":"completed","output":%q,"usage":{"total_tokens":6}}`, id, output)
		}
	}))
	defer fixture.Close()
	ca := filepath.Join(t.TempDir(), "fixture.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: fixture.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	address, err := url.Parse(fixture.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(address.Port())
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, owner := uuid.NewString(), uuid.NewString()
	exec(`insert into teams(id,name) values($1,$1::uuid::text)`, team)
	exec("insert into enterprise(name,default_team) values('test',$1) on conflict do nothing", team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'external tester','','member')`, owner, owner+"@example.test")
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, owner)
	exec(`insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])`, address.Hostname(), port)
	token := web.Token()
	exec(`insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '3 minutes')`, web.Hash(token), owner)
	s := Server{DB: db, Vault: vault}
	auth := identity.Server{DB: db}
	routes := http.NewServeMux()
	resources.Catalog{DB: db, Vault: vault, ExternalTrialContent: func(trial, actor, endpoint string) resources.ExternalContentPolicy {
		return policy.ExternalDLP{DB: db, Vault: vault, Validation: trial, Actor: actor, Exchange: "trial", Endpoint: endpoint}
	}}.Routes(routes, auth)
	s.Routes(routes, auth)
	call := func(method, path, body string, status int) string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.Header.Set("Idempotency-Key", path+"/"+method+"/"+fmt.Sprintf("%x", web.Hash(body))[:12])
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		routes.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body)
		}
		return w.Body.String()
	}
	var connection struct{ ID string }
	err = json.Unmarshal([]byte(call("POST", "/api/agents/adapters", fmt.Sprintf(`{"team":%q,"endpoint":%q,"key":"local-external-key",
	 "name":"Fixture asynchronous Agent","description":"Gets an asynchronous fixture","adapter":{"protocol":"async_webhook","template":"webhook",
	 "webhook":{"submit":{"path":"/submit"},"poll":{"path":"/poll/{task}"}}}}`, team, fixture.URL), 201)), &connection)
	if err != nil {
		t.Fatal(err)
	}
	var trial struct{ ID, Status string }
	err = json.Unmarshal([]byte(call("POST", "/api/agents/"+connection.ID+"/validate",
		`{"revision":1,"input":"smoke","admissionTest":true,"authorizeOneCall":true}`, 200)), &trial)
	if err != nil || trial.Status != "remote" {
		t.Fatalf("expected async connection test: %+v %v", trial, err)
	}
	if body := call("POST", "/api/agents/"+connection.ID+"/evidence/"+trial.ID+"/receipt", `{"action":"check"}`, 200); !strings.Contains(body, `"passed"`) {
		t.Fatal(body)
	}
	start := func(goal string, direct bool) (string, string) {
		t.Helper()
		project := uuid.NewString()
		exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'External fixture')`, project, owner, team)
		exec(`insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)`, project, owner)
		var run struct{ ID string }
		err := json.Unmarshal([]byte(call("POST", "/api/projects/"+project+"/general-runs", fmt.Sprintf(`{"expected":1,"goal":%q,
		 "externalAgents":[{"id":%q,"revision":1}],"directExternal":%t}`, goal, connection.ID, direct), 202)), &run)
		if err != nil || run.ID == "" {
			t.Fatalf("run creation: %+v %v", run, err)
		}
		return project, run.ID
	}
	_, run := start(directInput, true)
	a := Activities{DB: db, Vault: vault}
	advance := func(run string, turn int, want TurnResult) {
		t.Helper()
		if got, err := a.AdvanceTurn(ctx, TurnRef{RunID: run, Turn: turn}); err != nil || got != want {
			t.Fatalf("turn %d: got %+v want %+v (%v)", turn, got, want, err)
		}
	}
	execute := func(run string, want ToolResult) {
		t.Helper()
		if got, err := a.ExecuteTool(ctx, ToolRef{RunID: run, Turn: 1, Position: 1}); err != nil || got != want {
			t.Fatalf("tool action: got %+v want %+v (%v)", got, want, err)
		}
	}
	advance(run, 1, TurnResult{Tools: 1})
	execute(run, ToolResult{Wait: true, PollSeconds: 5})
	execute(run, ToolResult{Done: true})
	execute(run, ToolResult{Done: true})
	if sends.Load() != 2 || polls.Load() != 2 {
		t.Fatalf("direct replay produced extra calls: %d submits, %d polls", sends.Load(), polls.Load())
	}
	var cost int
	if err := db.QueryRow(ctx, `select count(*) from external_agent_receipts where run_id=$1 and operation='poll' and cost_disclosure='reported'`, run).
		Scan(&cost); err != nil || cost != 1 {
		t.Fatalf("missing reported usage: %d %v", cost, err)
	}
	var body, outcome, audit string
	var cipher []byte
	if err := db.QueryRow(ctx, `select v.body from artifacts a join artifact_versions v on v.artifact_id=a.id
		where a.run_id=$1`, run).Scan(&body); err != nil || body != "external fixture reply 13800000000" {
		t.Fatalf("reply was not restored within the original run: %q %v", body, err)
	}
	if err := db.QueryRow(ctx, `select outcome,counts::text from dlp_egress_audit_logs where run_id=$1`, run).
		Scan(&outcome, &audit); err != nil || outcome != "masked" || !strings.Contains(audit, "mobile") || strings.Contains(audit, "13800000000") {
		t.Fatalf("unexpected DLP audit: %s %s %v", outcome, audit, err)
	}
	if err := db.QueryRow(ctx, "select encrypted_tokens from dlp_egress_exchanges where run_id=$1", run).Scan(&cipher); err != nil ||
		strings.Contains(string(cipher), "13800000000") {
		t.Fatal("reversible mapping is not encrypted")
	}
	keywordID := uuid.NewString()
	keywordCipher, err := vault.Seal(secretTerm, "enterprise/dlp/rule/"+keywordID)
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into dlp_rules(id,kind,name,sensitivity,encrypted_match) values
	 ($1,'custom_term','fixture internal code','secret',$2)`, keywordID, keywordCipher)
	_, deniedRun := start("do not export "+secretTerm, true)
	advance(deniedRun, 1, TurnResult{Tools: 1})
	execute(deniedRun, ToolResult{Done: true})
	if sends.Load() != 2 || polls.Load() != 2 {
		t.Fatal("a restricted request reached the provider")
	}
	var remaining int
	if err := db.QueryRow(ctx, `select outcome,counts::text,(select count(*) from spend where run_id=$1)
		from dlp_egress_audit_logs where run_id=$1`, deniedRun).Scan(&outcome, &audit, &remaining); err != nil ||
		outcome != "blocked" || remaining != 0 || strings.Contains(audit, secretTerm) {
		t.Fatalf("blocked send leaked or retained cost: %s %s %d %v", outcome, audit, remaining, err)
	}
	blockedTrial := call("POST", "/api/agents/"+connection.ID+"/validate",
		fmt.Sprintf(`{"revision":1,"input":%q,"admissionTest":true,"authorizeOneCall":true}`, secretTerm), 200)
	var blockedRecord struct{ ID, Status string }
	if err := json.Unmarshal([]byte(blockedTrial), &blockedRecord); err != nil || blockedRecord.Status != "failed" || sends.Load() != 2 {
		t.Fatalf("validation allow-list leaked restricted test input: %s %v", blockedTrial, err)
	}
	if err := db.QueryRow(ctx, "select outcome from dlp_egress_audit_logs where validation_id=$1", blockedRecord.ID).Scan(&outcome); err != nil || outcome != "blocked" {
		t.Fatalf("validation was not audited: %s %v", outcome, err)
	}
	provider, model := uuid.NewString(), uuid.NewString()
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'external-fixture-model',array['text','tool_call'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, model)
	exec(`insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)`, model)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", model)
	a.Gateway = testGateway(func(_ context.Context, identity llmgateway.CallIdentity, req llmgateway.Request) (llmgateway.RoutedResult, error) {
		name := externalToolName(connection.ID)
		found := false
		for _, tool := range req.Tools {
			found = found || tool.Function.Name == name
		}
		if !found {
			t.Error("assistant did not receive its authorized external target")
		}
		reply := resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{{
			ID: "fixture-tool", Type: "function", Function: resources.ToolFunction{Name: name, Arguments: fmt.Sprintf(`{"message":%q}`, delegatedInput)},
		}}}
		if identity.Turn == 2 {
			if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "external fixture reply") {
				t.Error("assistant did not receive the external observation")
			}
			reply = resources.Reply{FinishReason: "stop", Text: "Combined reply"}
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: reply}}, nil
	})
	for _, revoke := range []bool{false, true} {
		project, run := start(internalGoal, false)
		advance(run, 1, TurnResult{Tools: 1})
		execute(run, ToolResult{Wait: true, Recheck: true})
		approvalPath := "/api/projects/" + project + "/tool-approvals"
		var approvals []toolApproval
		err := json.Unmarshal([]byte(call("GET", approvalPath, "", 200)), &approvals)
		if err != nil || len(approvals) != 1 || !strings.Contains(string(approvals[0].Arguments), delegatedInput) {
			t.Fatalf("missing delegation confirmation: %+v %v", approvals, err)
		}
		previous := sends.Load()
		call("POST", approvalPath+"/"+approvals[0].ID, `{"expected":1,"accept":true}`, 204)
		if revoke {
			exec("update agents set disabled=true where id=$1", connection.ID)
			execute(run, ToolResult{})
		} else {
			execute(run, ToolResult{Wait: true, PollSeconds: 5})
			execute(run, ToolResult{})
			advance(run, 2, TurnResult{Done: true})
			previous++
		}
		if sends.Load() != previous {
			t.Fatal("delegation dispatched without its current approval or more than once")
		}
	}
}
