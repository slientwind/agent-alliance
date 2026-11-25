package eventtriggers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"alliance/internal/agentloop"
	"alliance/internal/database"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

type eventModel func(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)

func (m eventModel) Call(ctx context.Context, actor llmgateway.CallIdentity, input llmgateway.Request) (llmgateway.RoutedResult, error) {
	return m(ctx, actor, input)
}

func TestDueBusinessEventQueuesOneScopedTemporalRunAndNotices(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("81", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, reviewer, team, trigger, batch, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(),
		uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into enterprise(name,default_team) values('Event fixture',$1) on conflict do nothing", team)
	exec(`insert into members(id,email,display_name,password_hash,role) values
	 ($1,$1::uuid::text||'@example.test','Trigger owner','','member'),($2,$2::uuid::text||'@example.test','Reviewer','','member')`, owner, reviewer)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, owner, reviewer)
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'event-test-model',array['text','tool_call'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, model)
	exec("insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)", model)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", model)
	exec(`insert into event_triggers(id,space_id,name,webhook_token_hash,hmac_cipher,filter_cel,dedup_cel,cooldown_seconds,maintainer,notify_targets)
	 select $1,id,'Low stock',gen_random_bytes(32),'fixture','true','event.id',60,$3,$4 from spaces where team_id=$2`,
		trigger, team, owner, []Target{{Channel: "feishu", Member: reviewer}, {Channel: "dingtalk", Member: reviewer}})
	sample, err := vault.Seal(`{"quantity":2,"email":"fixture.stock@example.test"}`, sampleScope(batch))
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into event_trigger_batches(id,trigger_id,trigger_revision,ready_at,hits,sample_cipher)
	 values($1,$2,1,clock_timestamp()-interval '2 seconds',12,$3)`, batch, trigger, sample)
	s := Server{DB: db, Vault: vault}
	if busy, err := s.StartReady(ctx); err != nil || !busy {
		t.Fatalf("due event dispatch: %t %v", busy, err)
	}
	if busy, err := s.StartReady(ctx); err != nil || busy {
		t.Fatalf("duplicate event dispatch: %t %v", busy, err)
	}
	var run, assignee, goal string
	var correctSpace, canWrite bool
	if err := db.QueryRow(ctx, `select r.id,p.final_reviewer,p.space_id=t.space_id,event_run_write_allowed(r.id),plan.brief
	 from event_trigger_batches b join event_triggers t on t.id=b.trigger_id join runs r on r.id=b.run_id
	 join general_agent_dispatch d on d.run_id=r.id join projects p on p.id=r.project_id
	 join plans plan on (plan.project_id,plan.version)=(p.id,r.plan_version) where b.id=$1`, batch).
		Scan(&run, &assignee, &correctSpace, &canWrite, &goal); err != nil || !correctSpace || canWrite || assignee != reviewer ||
		!strings.Contains(goal, "累计去重事件 12") || strings.Contains(goal, "fixture.stock@example.test") {
		t.Fatalf("incorrect scoped dispatch: %s %s %t %t %q %v", run, assignee, correctSpace, canWrite, goal, err)
	}
	activities := agentloop.Activities{DB: db, Vault: vault, Gateway: eventModel(func(_ context.Context, _ llmgateway.CallIdentity, req llmgateway.Request) (llmgateway.RoutedResult, error) {
		if !strings.Contains(req.Messages[len(req.Messages)-1].Content, "SOP") {
			return llmgateway.RoutedResult{}, fmt.Errorf("event briefing did not reach the model")
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: resources.Reply{Text: "检查库存阈值并核对入库记录。"}}}, nil
	})}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(activities.AdvanceTurn, activity.RegisterOptions{Name: agentloop.AdvanceTurnActivity})
	env.RegisterActivityWithOptions(activities.ExecuteTool, activity.RegisterOptions{Name: agentloop.ExecuteToolActivity})
	env.ExecuteWorkflow(agentloop.GeneralAgentWorkflow, run)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReady(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var notices int
	if err := db.QueryRow(ctx, `select status,(select count(*) from event_trigger_notices where batch_id=$1 and member_id=$2)
	 from event_trigger_batches where id=$1`, batch, reviewer).Scan(&state, &notices); err != nil || state != "completed" || notices != 4 {
		t.Fatalf("workflow result or notification queue failed: %s %d %v", state, notices, err)
	}
}
