package evalsuite

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type fakeEvaluationModel func(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)

func (m fakeEvaluationModel) Call(ctx context.Context, id llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
	return m(ctx, id, request)
}

func TestGoldenCaseAssertionChecksAuthorizedCitationsAndNumericTolerance(t *testing.T) {
	const id = "K-0000000000000001"
	checks := Assertions{Facts: []string{"按出库计算"}, Citations: []string{id}, Numbers: []Number{{Key: "合计", Value: 27, Tolerance: .1}}}
	passing := `{"answer":"按出库计算。","citations":["` + id + `"],"numbers":{"合计":27.1}}`
	if failures := evaluateAssertions(passing, checks, []string{id}); len(failures) != 0 {
		t.Fatalf("approved assertion failed: %v", failures)
	}
	if failures := evaluateAssertions(strings.Replace(passing, "27.1", "27.12", 1), checks, nil); len(failures) != 3 {
		t.Fatalf("wrong figure or unsupported citation accepted: %v", failures)
	}
}

func TestCaptureTeamTaskAndEvaluateThroughTemporal(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
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
	vault, err := resources.NewVault(strings.Repeat("79", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, owner, project, run, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','Fixture Worker','','member')", owner)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, owner)
	var space string
	if err := db.QueryRow(ctx, "select id from spaces where team_id=$1", team).Scan(&space); err != nil {
		t.Fatal(err)
	}
	exec("update space_members set role='space_admin' where (space_id,member_id)=($1,$2)", space, owner)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Shipping review',1)", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, owner)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'请核对商品出库总量，联系人 fixture.private@example.test',$2)", project, owner)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,'completed',$4,$1::uuid::text,$5,4)`, run, project, owner, team, web.Hash(run))
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'eval-candidate',array['text'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, model)
	s := Server{DB: db, Vault: vault}
	request := func(body, suite string) (*httptest.ResponseRecorder, *http.Request) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)).WithContext(ctx)
		r.SetPathValue("space", space)
		r.SetPathValue("suite", suite)
		return httptest.NewRecorder(), r
	}
	user := identity.Member{ID: owner}
	w, r := request(`{"name":"出库口径对账"}`, "")
	if err := s.create(w, r, user); err != nil {
		t.Fatal(err)
	}
	var created struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || !validID(created.ID) {
		t.Fatalf("suite creation: %s %v", w.Body, err)
	}
	suite := created.ID
	for i := range 2 {
		artifact := uuid.NewString()
		exec("insert into artifacts(id,project_id,run_id,name,ordinal) values($1,$2,$3,'Reviewed quantity',$4)", artifact, project, run, i+1)
		exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,$2,'采用')", artifact, "数量: 42件\n业务已经核对。")
		raw, _ := json.Marshal(captureSpec{Artifact: artifact, Version: 1})
		w, r = request(string(raw), suite)
		if err := s.capture(w, r, user); err != nil {
			t.Fatal(err)
		}
	}
	var prompt string
	var checks Assertions
	if err := db.QueryRow(ctx, "select input_prompt,assertions from eval_cases where suite_id=$1 limit 1", suite).Scan(&prompt, &checks); err != nil ||
		strings.Contains(prompt, "example.test") || len(checks.Numbers) != 1 || checks.Numbers[0].Value != 42 {
		t.Fatalf("unexpected captured fixture: %q %+v %v", prompt, checks, err)
	}
	w, r = request(`{"kind":"model","candidateModelId":"`+model+`"}`, suite)
	if err := s.start(w, r, user); err != nil {
		t.Fatal(err)
	}
	created.ID = ""
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a := Activities{Server: s, Gateway: fakeEvaluationModel(func(ctx context.Context, id llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
		calls.Add(1)
		if len(in.Tools) != 0 || strings.Contains(in.Messages[0].Content, "42") || id.RunID != "" {
			t.Error("evaluation revealed the answer key or enabled tools")
		}
		if err := id.Authorize(ctx); err != nil {
			return llmgateway.RoutedResult{}, err
		}
		prepared, err := id.Prepare(ctx, llmgateway.Candidate{}, in)
		if err != nil {
			return llmgateway.RoutedResult{}, err
		}
		if err := prepared.Authorize(ctx); err != nil {
			return llmgateway.RoutedResult{}, err
		}
		value := "42"
		if id.EvaluationModel == model {
			value = "37"
		}
		return llmgateway.RoutedResult{ModelID: id.EvaluationModel, Result: llmgateway.Result{Reply: resources.Reply{
			Text: `{"answer":"已统计出库数量。","citations":[],"numbers":{"数量":` + value + `}}`,
		}}}, nil
	})}
	var temporalSuite testsuite.WorkflowTestSuite
	env := temporalSuite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(a.Begin, activity.RegisterOptions{Name: BeginName})
	env.RegisterActivityWithOptions(a.Evaluate, activity.RegisterOptions{Name: EvaluateName})
	env.RegisterActivityWithOptions(a.Finish, activity.RegisterOptions{Name: FinishName})
	env.ExecuteWorkflow(EvalWorkflow, created.ID)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var report, state string
	if err := db.QueryRow(ctx, "select state,diff_report_json::text from eval_runs where id=$1", created.ID).Scan(&state, &report); err != nil ||
		state != "completed" || !strings.Contains(report, `"candidatePassRate": 0.0000`) ||
		!strings.Contains(report, `"baselinePassRate": 1.0000`) || !strings.Contains(report, `number:1`) || calls.Load() != 4 {
		t.Fatalf("unexpected evaluation: %d %s %s %v", calls.Load(), state, report, err)
	}
}
