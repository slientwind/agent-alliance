package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/sandbox"
	"alliance/internal/web"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestGeneralRunCorrectsConnectorFailureAndStoresArtifacts(t *testing.T) {
	t.Run("trusted tool", func(t *testing.T) { checkGeneralRunArtifacts(t, false) })
	t.Run("official template", func(t *testing.T) { checkGeneralRunArtifacts(t, true) })
}

func checkGeneralRunArtifacts(t *testing.T, branded bool) {
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
	var rendererCalls atomic.Int32
	workbook := sampleWorkbook(t)
	var renderer *sandbox.BrandRenderer
	var templateID string
	wantConnectorCalls := int32(2)
	if branded {
		wantConnectorCalls = 1
		templateID = uuid.NewString()
		var hash string
		if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			var err error
			hash, _, err = objects.Put(ctx, tx, bytes.NewReader(workbook), 8192)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into corporate_templates(id,space_id,doc_type,name,object_hash,is_default,author)
			select $1,space_id,'xlsx','Official workbook',$2,true,$3 from projects where id=$4`, templateID, hash, owner, project)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		socketDir, err := os.MkdirTemp("/tmp", "aa-brand-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
		socket := filepath.Join(socketDir, "render.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		root := t.TempDir()
		fixture := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rendererCalls.Add(1)
			var in struct {
				Digest, Format string
				Fields         map[string]string
			}
			info, statErr := os.Stat(filepath.Join(root, hash))
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || r.URL.Path != "/render" ||
				in.Format != "xlsx" || in.Digest != hash || in.Fields["title"] != "Invoice" || in.Fields["content"] != "Total: 27" ||
				statErr != nil || info.Mode().Perm() != 0444 {
				t.Errorf("invalid renderer request or staged template: %+v %v %v", in, err, statErr)
				http.Error(w, "invalid request", 400)
				return
			}
			w.Write(workbook)
		})}
		go func() { _ = fixture.Serve(listener) }()
		t.Cleanup(func() { _ = fixture.Close() })
		renderer = &sandbox.BrandRenderer{Pool: sandbox.Pool{DB: db}, Objects: objects, Root: root, Socket: socket}
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connectorCalls.Add(1)
		var in struct{ Operation, Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || (in.Operation != "aggregate" && in.Text != "Total: 27") || r.Header.Get("Authorization") != "Bearer loop-test-token" {
			t.Errorf("connector request was not scoped: %+v, %v", in, err)
		}
		if in.Operation == "aggregate" {
			http.Error(w, "fixture-internal-credential=not-for-model", 422)
			return
		}
		if in.Operation != "xlsx" {
			t.Errorf("unexpected fallback operation: %+v", in)
		}
		json.NewEncoder(w).Encode(resources.Reply{Text: "Total: 27", File: &resources.File{
			Name: "deliverable.xlsx", Mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: workbook,
		}})
	}))
	defer mock.Close()
	t.Setenv("CONNECTOR_URL", mock.URL)
	t.Setenv("CONNECTOR_TOKEN", "loop-test-token")
	a := Activities{DB: db, Vault: vault, Objects: objects, BrandRenderer: renderer, Gateway: testGateway(func(_ context.Context, actor llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
		if actor.RunID != run || actor.MemberID != owner || actor.TeamID != team || len(in.Tools) < 2 {
			return llmgateway.RoutedResult{}, fmt.Errorf("unexpected model identity or tools")
		}
		var reply resources.Reply
		switch modelCalls.Add(1) {
		case 1:
			reply = resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{
				{ID: "read", Type: "function", Function: resources.ToolFunction{Name: "read_sources", Arguments: `{"query":"amount"}`}},
				{ID: "aggregate", Type: "function", Function: resources.ToolFunction{Name: "aggregate_csv", Arguments: `{"group":"invoice","amount":"amount"}`}},
			}}
		case 2:
			if len(in.Messages) != 5 || !strings.Contains(in.Messages[3].Content, "invoice amount: 27") || in.Messages[3].ToolCallID != "read" ||
				in.Messages[4].ToolCallID != "aggregate" || !strings.Contains(in.Messages[4].Content, `"code":"result_unconfirmed"`) ||
				strings.Contains(in.Messages[4].Content, "not-for-model") {
				t.Errorf("tool observations were not assembled: %+v", in.Messages)
				return llmgateway.RoutedResult{}, fmt.Errorf("tool observations were not assembled")
			}
			reply = resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{
				{ID: "make", Type: "function", Function: resources.ToolFunction{Name: "create_xlsx", Arguments: `{"title":"Invoice","content":"Total: 27"}`}},
			}}
		case 3:
			if len(in.Messages) != 7 || in.Messages[6].ToolCallID != "make" {
				return llmgateway.RoutedResult{}, fmt.Errorf("fallback tool receipt did not reach the workflow")
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
		Scan(&state, &kind, &candidateCount, &fileCount); err != nil || state != "completed" || kind != "lightweight" || candidateCount != 2 || fileCount != 1 || connectorCalls.Load() != wantConnectorCalls || modelCalls.Load() != 3 {
		t.Fatalf("run=%s kind=%s artifacts=%d files=%d toolCalls=%d modelCalls=%d err=%v",
			state, kind, candidateCount, fileCount, connectorCalls.Load(), modelCalls.Load(), err)
	}
	if replay, err := a.ExecuteTool(ctx, ToolRef{RunID: run, Turn: 2, Position: 1}); err != nil || !replay.Done || connectorCalls.Load() != wantConnectorCalls {
		t.Fatalf("replayed a completed tool: %+v, %v", replay, err)
	}
	if branded {
		var revision int
		if err := db.QueryRow(ctx, `select r.template_revision from brand_render_receipts r
		join sandbox_artifacts a on a.artifact_id=r.artifact_id where a.run_id=$1 and r.template_id=$2`, run, templateID).
			Scan(&revision); err != nil || revision != 1 || rendererCalls.Load() != 1 {
			t.Fatalf("invalid brand receipt or renderer retry: %d, %d, %v", revision, rendererCalls.Load(), err)
		}
	}
	ownerToken := web.Token()
	exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')", web.Hash(ownerToken), owner)
	mux := http.NewServeMux()
	projects.Server{DB: db, Vault: vault}.Routes(mux, identity.Server{DB: db})
	tracePath := "/api/admin/monitor/trace/" + run
	trace := func(path string, status int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: ownerToken})
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("trace %s returned %d: %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	trace(tracePath, 403)
	var timeline struct {
		Trace struct {
			ActionSpans []struct {
				Name       string
				Start, End time.Time
			}
		}
	}
	data := trace("/api/projects/"+project+"/runs/"+run+"/trace", 200)
	if err := json.Unmarshal(data, &timeline); err != nil || len(timeline.Trace.ActionSpans) != 3 {
		t.Fatalf("missing tool trace durations: %s %v", data, err)
	}
	for i, want := range []string{"read_sources", "aggregate_csv", "create_xlsx"} {
		span := timeline.Trace.ActionSpans[i]
		if span.Name != want || span.Start.IsZero() || span.End.Before(span.Start) {
			t.Fatalf("invalid tool span: %+v", span)
		}
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
