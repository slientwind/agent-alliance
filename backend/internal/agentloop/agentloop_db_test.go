package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type testGateway func(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)

func (g testGateway) Call(ctx context.Context, actor llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
	return g(ctx, actor, in)
}

func TestGeneralRunDispatchesTrustedToolsAndStoresArtifacts(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("ba", 32))
	if err != nil {
		t.Fatal(err)
	}
	objects := knowledge.Objects{Root: t.TempDir()}
	team, owner, project, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into teams(id,name) values($1,$2)`, team, "general-test-"+team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'loop test','','member')`, owner, owner+"@example.test")
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, owner)
	exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'loop test')`, project, owner, team)
	exec(`insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)`, project, owner)
	const initialGoal = "Find amount and write an XLSX"
	exec("insert into messages(project_id,author,kind,body) values($1,$2,'user',$3)", project, owner, initialGoal)
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'loop-test-model',array['text','tool_call'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, model)
	exec(`insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)`, model)
	defer db.Exec(context.Background(), `delete from model_tier_routes where model_id=$1`, model)
	tiers := httptest.NewRecorder()
	if err := (Server{DB: db}).tiers(tiers, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/general-tiers", nil), identity.Member{ID: owner}); err != nil ||
		tiers.Code != 200 || !strings.Contains(tiers.Body.String(), `"default"`) || strings.Contains(tiers.Body.String(), "fixture.example") {
		t.Fatalf("unexpected available tiers: %s %v", tiers.Body, err)
	}
	var source string
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var err error
		source, err = objects.NewTextSource(ctx, tx, project, owner, "worksheet", "invoice amount: 27; approved")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var revision int
	if err := db.QueryRow(ctx, `select revision from projects where id=$1`, project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	requestBody := fmt.Sprintf(`{"expected":%d,"goal":"Find amount and write an XLSX","sources":[{"id":"%s","revision":1}]}`, revision, source)
	start := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/projects/"+project+"/general-runs", strings.NewReader(requestBody))
		r.SetPathValue("id", project)
		r.Header.Set("Idempotency-Key", "loop-test")
		if err := (Server{DB: db}).start(w, r, identity.Member{ID: owner}); err != nil {
			t.Fatal(err)
		}
		var reply struct{ ID string }
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 202 || reply.ID == "" {
			t.Fatalf("run start returned %d: %s", w.Code, w.Body.String())
		}
		return reply.ID
	}
	run := start()
	if run != start() {
		t.Fatal("idempotent retry created another run")
	}
	var prompts int
	if err := db.QueryRow(ctx, "select count(*) from messages where project_id=$1 and kind='user'", project).Scan(&prompts); err != nil || prompts != 1 {
		t.Fatalf("initial prompt was inserted again: %d %v", prompts, err)
	}
	var connectorCalls, modelCalls atomic.Int32
	workbook := sampleWorkbook(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectorCalls.Add(1)
		var in struct{ Operation, Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Operation != "xlsx" || in.Text != "Total: 27" || r.Header.Get("Authorization") != "Bearer loop-test-token" {
			t.Errorf("connector request was not scoped: %+v, %v", in, err)
		}
		json.NewEncoder(w).Encode(resources.Reply{Text: "Total: 27", File: &resources.File{
			Name: "deliverable.xlsx", Mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: workbook,
		}})
	}))
	defer mock.Close()
	t.Setenv("CONNECTOR_URL", mock.URL)
	t.Setenv("CONNECTOR_TOKEN", "loop-test-token")
	a := Activities{DB: db, Vault: vault, Objects: objects, Gateway: testGateway(func(_ context.Context, actor llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
		if actor.RunID != run || actor.MemberID != owner || actor.TeamID != team || len(in.Tools) < 2 {
			return llmgateway.RoutedResult{}, fmt.Errorf("unexpected model identity or tools")
		}
		var reply resources.Reply
		switch modelCalls.Add(1) {
		case 1:
			reply = resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{
				{ID: "read", Type: "function", Function: resources.ToolFunction{Name: "read_sources", Arguments: `{"query":"amount"}`}},
				{ID: "make", Type: "function", Function: resources.ToolFunction{Name: "create_xlsx", Arguments: `{"title":"Invoice","content":"Total: 27"}`}},
			}}
		case 2:
			if len(in.Messages) != 5 || !strings.Contains(in.Messages[3].Content, "invoice amount: 27") || in.Messages[3].ToolCallID != "read" || in.Messages[4].ToolCallID != "make" {
				return llmgateway.RoutedResult{}, fmt.Errorf("tool observations were not assembled")
			}
			reply = resources.Reply{FinishReason: "stop", Text: "The approved amount is 27."}
		default:
			return llmgateway.RoutedResult{}, fmt.Errorf("unexpected model replay")
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: reply}}, nil
	})}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(a.AdvanceTurn, activity.RegisterOptions{Name: AdvanceTurnActivity})
	env.RegisterActivityWithOptions(a.ExecuteTool, activity.RegisterOptions{Name: ExecuteToolActivity})
	env.ExecuteWorkflow(GeneralAgentWorkflow, run)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var state, kind string
	var candidateCount, fileCount int
	if err := db.QueryRow(ctx, `select r.status,r.quality_track,(select count(*) from artifacts a where a.run_id=r.id),
	 (select count(*) from artifact_files f join artifacts a on a.id=f.artifact_id where a.run_id=r.id) from runs r where r.id=$1`, run).
		Scan(&state, &kind, &candidateCount, &fileCount); err != nil || state != "completed" || kind != "lightweight" || candidateCount != 2 || fileCount != 1 || connectorCalls.Load() != 1 || modelCalls.Load() != 2 {
		t.Fatalf("run=%s kind=%s artifacts=%d files=%d toolCalls=%d modelCalls=%d err=%v",
			state, kind, candidateCount, fileCount, connectorCalls.Load(), modelCalls.Load(), err)
	}
	if replay, err := a.ExecuteTool(ctx, ToolRef{RunID: run, Turn: 1, Position: 2}); err != nil || !replay.Done || connectorCalls.Load() != 1 {
		t.Fatalf("replayed a completed tool: %+v, %v", replay, err)
	}
	if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/projects/"+project+"/general-runs",
		strings.NewReader(fmt.Sprintf(`{"expected":%d,"goal":"Check the previous amount again","sources":[{"id":"%s","revision":1}]}`, revision, source)))
	r.SetPathValue("id", project)
	r.Header.Set("Idempotency-Key", "loop-followup")
	if err := (Server{DB: db}).start(w, r, identity.Member{ID: owner}); err != nil {
		t.Fatal(err)
	}
	var created struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	followup := created.ID
	checkPastAnswer := func(want bool) {
		t.Helper()
		if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			d, err := loadRun(ctx, tx, followup)
			if err != nil {
				return err
			}
			messages, _, _, err := a.contextForTurn(ctx, tx, TurnRef{RunID: followup, Turn: 1}, d)
			if err != nil {
				return err
			}
			has := false
			for _, message := range messages {
				has = has || message.Content == "The approved amount is 27."
			}
			if has != want || messages[len(messages)-1].Content != "Check the previous amount again" {
				return fmt.Errorf("unexpected authorized followup history: previous answer present=%t", has)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	checkPastAnswer(true)
	var replyArtifact string
	if err := db.QueryRow(ctx, `select a.id from general_agent_runs g join artifacts a on a.id=g.final_reply where g.run_id=$1 and a.run_id=g.run_id`,
		run).Scan(&replyArtifact); err != nil || replyArtifact == "" {
		t.Fatalf("general run is missing its reply artifact: %s %v", replyArtifact, err)
	}
	exec("update runs set status='failed' where id=$1", followup)
	exec("update projects set stage='brief' where id=$1", project)
	if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	retry := func(expected int, key string) (string, error) {
		t.Helper()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"expected":%d}`, expected)))
		req.SetPathValue("id", project)
		req.SetPathValue("run", run)
		req.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		if err := (Server{DB: db, Vault: vault}).retryRun(response, req, identity.Member{ID: owner}); err != nil {
			return "", err
		}
		var body struct{ ID string }
		err := json.Unmarshal(response.Body.Bytes(), &body)
		return body.ID, err
	}
	retried, err := retry(revision, "fixture-retry")
	if err != nil || retried == "" || retried == run {
		t.Fatalf("could not restart the selected reply: %s %v", retried, err)
	}
	if again, err := retry(revision, "fixture-retry"); err != nil || again != retried {
		t.Fatalf("retry did not replay the same run: %s %v", again, err)
	}
	var replayedGoal, replayedSource string
	var signalCount int
	if err := db.QueryRow(ctx, `select plan.brief,s.source_id,(select count(*) from run_quality_signals
	 where (run_id,actor,kind)=($2,$3,'reply_regenerated')) from runs r
	 join plans plan on (plan.project_id,plan.version)=(r.project_id,r.plan_version)
	 join plan_sources s on (s.project_id,s.plan_version)=(r.project_id,r.plan_version) where r.id=$1`,
		retried, run, owner).Scan(&replayedGoal, &replayedSource, &signalCount); err != nil ||
		replayedGoal != initialGoal || replayedSource != source || signalCount != 1 {
		t.Fatalf("retry did not retain its prompt, source, and single signal: %q %q %d %v", replayedGoal, replayedSource, signalCount, err)
	}
	exec("update sources set revoked_at=now() where id=$1", source)
	checkPastAnswer(false)
	exec("update runs set status='cancelled' where id=$1", retried)
	exec("update projects set stage='brief' where id=$1", project)
	if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := retry(revision, "revoked-retry"); err == nil {
		t.Fatal("retry silently ignored a withdrawn source")
	}
}

func sampleWorkbook(t *testing.T) []byte {
	t.Helper()
	var file bytes.Buffer
	w := zip.NewWriter(&file)
	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml"} {
		part, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, "<test/>"); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return file.Bytes()
}
