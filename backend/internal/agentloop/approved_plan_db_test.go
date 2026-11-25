package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/resources"
	"alliance/internal/runtime"
	"alliance/internal/web"
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestApprovedPlanRunsOnGeneralWorkerWithPinnedLegacyRevision(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
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
	vault, err := resources.NewVault(strings.Repeat("bf", 32))
	if err != nil {
		t.Fatal(err)
	}
	var calls, toolCalls atomic.Int32
	model := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input struct{ Model string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Model != "pinned-model" || r.Header.Get("Authorization") != "Bearer pinned-fixture" {
			t.Errorf("legacy publication did not preserve its model and credential: %s %v", input.Model, err)
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop",
			"message": map[string]string{"content": "Pinned result"}}}, "usage": map[string]int{"total_tokens": 5}})
	}))
	defer model.Close()
	ca := filepath.Join(t.TempDir(), "model-ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: model.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	workbook := sampleWorkbook(t)
	connector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		toolCalls.Add(1)
		var input struct{ Operation, Text string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Operation != "xlsx" ||
			!strings.Contains(input.Text, "Pinned result") || r.Header.Get("Authorization") != "Bearer tool-fixture" {
			t.Errorf("tool input did not inherit the pinned parent result: %+v %v", input, err)
		}
		json.NewEncoder(w).Encode(resources.Reply{Text: "Workbook ready", File: &resources.File{
			Name: "approved.xlsx", Mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: workbook}})
	}))
	defer connector.Close()
	t.Setenv("CONNECTOR_URL", connector.URL)
	t.Setenv("CONNECTOR_TOKEN", "tool-fixture")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	person, team, modelID, agent, tool, project, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	key, err := vault.Seal("pinned-fixture", "model/"+modelID+"/1")
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := vault.Seal("newer-fixture", "model/"+modelID+"/2")
	if err != nil {
		t.Fatal(err)
	}
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", person, person+"@example.test")
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, person)
	exec("insert into model_resources(id,name,team_id,created_by) values($1,'Fixture',$2,$3)", modelID, team, person)
	exec("insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny) values($1,1,$2,'pinned-model',$3,0.1),($1,2,$2,'newer-model',$4,0.5)", modelID, model.URL, key, newKey)
	exec("insert into agents(id,owner,team_id) values($1,$2,$3)", agent, person, team)
	exec("insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by) values($1,1,'Fixture','Reply',$2,1,$3)", agent, modelID, person)
	exec("insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", agent, person)
	exec("update agents set published_revision=1 where id=$1", agent)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,stage,plan_version) values($1,$2,$3,$2,'Fixture','running',1)", project, person, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, person)
	exec("insert into project_budgets(project_id) values($1) on conflict do nothing", project)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Approved plan',$2)", project, person)
	exec("insert into agents(id,owner,team_id) values($1,$2,$3)", tool, person, team)
	exec(`insert into agent_revisions(agent_id,revision,name,instructions,created_by,tool)
	 values($1,1,'Workbook','Generate workbook',$2,'{"operation":"xlsx","config":{"title":"Fixture"}}')`, tool, person)
	exec("insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", tool, person)
	exec("update agents set published_revision=1 where id=$1", tool)
	exec("insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,1,1,$2,1),($1,1,2,$3,1)", project, agent, tool)
	exec("insert into node_edges(project_id,plan_version,ordinal,depends_on) values($1,1,2,1)", project)
	exec("insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,$4,$1::uuid::text,$5,2)",
		run, project, person, team, web.Hash("approved plan"))
	legacy := runtime.Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	general := Activities{DB: db, Vault: vault, Planned: &legacy}
	if address := os.Getenv("ALLIANCE_LOOKUP_TEST_TEMPORAL"); address != "" {
		t.Setenv("TEMPORAL_QUEUE", "alliance-test-approved-"+run)
		tc, err := client.Dial(client.Options{HostPort: address})
		if err != nil {
			t.Fatal(err)
		}
		defer tc.Close()
		w := worker.New(tc, runtime.GeneralQueue(), worker.Options{})
		w.RegisterWorkflowWithOptions(GeneralAgentWorkflow, workflow.RegisterOptions{Name: WorkflowName})
		w.RegisterActivityWithOptions(general.AdvanceTurn, activity.RegisterOptions{Name: AdvanceTurnActivity})
		w.RegisterActivityWithOptions(general.ExecuteTool, activity.RegisterOptions{Name: ExecuteToolActivity})
		if err = w.Start(); err != nil {
			t.Fatal(err)
		}
		defer w.Stop()
		exec("insert into outbox(run_id) values($1)", run)
		dispatch, stop := context.WithCancel(ctx)
		dispatchDone := make(chan struct{})
		go func() {
			defer close(dispatchDone)
			legacy.Dispatch(dispatch, tc)
		}()
		defer func() { stop(); <-dispatchDone }()
		history := tc.GetWorkflowHistory(ctx, "run/"+run, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		var started, advanced, executed bool
		for history.HasNext() {
			event, err := history.Next()
			if _, missing := err.(*serviceerror.NotFound); missing {
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(100 * time.Millisecond):
				}
				history = tc.GetWorkflowHistory(ctx, "run/"+run, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if entry := event.GetWorkflowExecutionStartedEventAttributes(); entry != nil {
				started = entry.GetWorkflowType().GetName() == WorkflowName && entry.GetTaskQueue().GetName() == runtime.GeneralQueue()
				break
			}
		}
		if err := tc.GetWorkflow(ctx, "run/"+run, "").Get(ctx, nil); err != nil {
			t.Fatal(err)
		}
		history = tc.GetWorkflowHistory(ctx, "run/"+run, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for history.HasNext() {
			event, err := history.Next()
			if err != nil {
				t.Fatal(err)
			}
			if entry := event.GetActivityTaskScheduledEventAttributes(); entry != nil {
				switch entry.GetActivityType().GetName() {
				case AdvanceTurnActivity:
					advanced = true
				case ExecuteToolActivity:
					executed = true
				default:
					t.Fatal("fresh history used a historical activity name")
				}
			}
		}
		if !started || !advanced || !executed {
			t.Fatalf("unexpected history: general=%t advance=%t execute=%t", started, advanced, executed)
		}
	} else {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.RegisterActivityWithOptions(general.AdvanceTurn, activity.RegisterOptions{Name: AdvanceTurnActivity})
		env.RegisterActivityWithOptions(general.ExecuteTool, activity.RegisterOptions{Name: ExecuteToolActivity})
		env.ExecuteWorkflow(GeneralAgentWorkflow, runtime.PlannedRunInput(run))
		if err := env.GetWorkflowError(); err != nil {
			t.Fatal(err)
		}
	}
	var status, body string
	var charges int
	err = db.QueryRow(ctx, `select r.status,v.body,(select count(*) from spend where run_id=r.id)
	 from runs r join artifacts a on a.run_id=r.id join artifact_versions v on (v.artifact_id,v.version)=(a.id,1)
	 where r.id=$1 and a.ordinal=1`, run).Scan(&status, &body, &charges)
	if err != nil || status != "completed" || body != "Pinned result" || charges != 2 || calls.Load() != 1 || toolCalls.Load() != 1 {
		t.Fatalf("approved plan receipt: %s %q charges=%d provider calls=%d tool calls=%d: %v", status, body, charges, calls.Load(), toolCalls.Load(), err)
	}
}
