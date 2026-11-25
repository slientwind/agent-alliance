package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestDelegateGraphRejectsCyclesAndDeepChains(t *testing.T) {
	id := uuid.NewString()
	node := delegateSpec{Agent: id, Revision: 1}
	node.Children = []delegateSpec{{Agent: id, Revision: 1}}
	if _, err := flattenDelegates([]delegateSpec{node}); err == nil {
		t.Fatal("accepted a repeated ancestor")
	}
	node.Children = nil
	node.Fallback = &externalRef{ID: id, Revision: 1}
	if _, err := flattenDelegates([]delegateSpec{node}); err == nil {
		t.Fatal("accepted the same primary and alternate")
	}
	node.Fallback = nil
	for range 4 {
		node = delegateSpec{Agent: uuid.NewString(), Revision: 1, Children: []delegateSpec{node}}
	}
	if _, err := flattenDelegates([]delegateSpec{node}); err == nil {
		t.Fatal("accepted a deep delegation chain")
	}
}

func TestNestedDelegationDiscardsRevokedReceiptAndUsesApprovedFallback(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("ec", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, other, project := uuid.NewString(), uuid.NewString(), uuid.NewString()
	team, remote, leaf := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, member := range []string{owner, other} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", member, member+"@example.test")
	}
	for _, payer := range []string{team, remote, leaf} {
		exec("insert into teams(id,name) values($1,$1::uuid::text)", payer)
	}
	exec("insert into team_members(team_id,member_id) values($1,$2),($3,$4),($5,$4)", team, owner, remote, other, leaf)
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'Fixture')", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, owner)
	agent := func(team, author, label string) (string, string) {
		t.Helper()
		id, model, grant := uuid.NewString(), uuid.NewString(), uuid.NewString()
		encrypted, err := vault.Seal("unused-fixture", "model/"+model+"/1")
		if err != nil {
			t.Fatal(err)
		}
		exec("insert into model_resources(id,name,team_id,created_by) values($1,$2,$3,$4)", model, label, team, author)
		exec(`insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key)
		 values($1,1,'https://delegate-fixture.example.test','text-fixture',$2)`, model, encrypted)
		exec("insert into agents(id,owner,team_id) values($1,$2,$3)", id, author, team)
		exec(`insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
		 values($1,1,$2,$2,$3,1,$4)`, id, label, model, author)
		exec("insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", id, author)
		exec("update agents set published_revision=1 where id=$1", id)
		if author != owner {
			exec(`insert into project_agent_grants(id,project_id,agent_id,revision,description,requested_by,expires_at,call_limit,resource_approver,budget_approver)
			 values($1,$2,$3,1,'Authorized text assistance',$4,now()+interval '1 hour',1,$5,$5)`, grant, project, id, owner, other)
		}
		return id, grant
	}
	root, _ := agent(team, owner, "Primary")
	backup, _ := agent(team, owner, "Backup")
	middle, middleGrant := agent(remote, other, "Middle")
	final, leafGrant := agent(leaf, other, "Leaf")
	provider, candidate := uuid.NewString(), uuid.NewString()
	exec("insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://delegate-fixture.example.test/v1')", provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'fixture',array['text','tool_call'],8192,1800)`, candidate, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, candidate)
	exec("insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)", candidate)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", candidate)
	payload := StartInput{Expected: 1, Goal: "A workplace request", Delegates: []delegateSpec{
		{Agent: root, Revision: 1, Fallback: &externalRef{ID: backup, Revision: 1}, Children: []delegateSpec{
			{Agent: middle, Revision: 1, Children: []delegateSpec{{Agent: final, Revision: 1}}},
		}},
	}}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(string(raw)))
	req.SetPathValue("id", project)
	req.Header.Set("Idempotency-Key", "delegation-fixture")
	rec := httptest.NewRecorder()
	if err := (Server{DB: db, Vault: vault}).start(rec, req, identity.Member{ID: owner}); err != nil {
		t.Fatal(err)
	}
	var response struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.ID == "" {
		t.Fatalf("could not start delegation: %s %v", rec.Body, err)
	}
	var mu sync.Mutex
	order := []string{}
	a := Activities{DB: db, Vault: vault, Gateway: testGateway(func(callCtx context.Context, actor llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
		mu.Lock()
		defer mu.Unlock()
		reply := resources.Reply{FinishReason: "stop"}
		var next string
		switch {
		case !strings.Contains(actor.OperationKey, "/delegate/") && actor.Turn == 1:
			order = append(order, "root")
			next = "1"
		case strings.HasSuffix(actor.OperationKey, "/delegate/1/false"):
			order = append(order, "primary")
			next = "1.1"
		case strings.HasSuffix(actor.OperationKey, "/delegate/1.1/false"):
			order = append(order, "middle")
			next = "1.1.1"
		case strings.HasSuffix(actor.OperationKey, "/delegate/1.1.1/false"):
			order = append(order, "leaf")
			exec("update project_agent_grants set revoked_at=now() where id=$1", middleGrant)
			reply.Text = "stale subagent result"
		case strings.HasSuffix(actor.OperationKey, "/delegate/1/true"):
			order = append(order, "fallback")
			reply.Text = "approved fallback result"
		case actor.Turn == 2:
			order = append(order, "finish")
			var accepted bool
			for _, message := range request.Messages {
				accepted = accepted || strings.Contains(message.Content, "approved fallback result")
				if strings.Contains(message.Content, "stale subagent result") {
					t.Error("revoked result reached the root")
				}
			}
			if !accepted {
				t.Error("fallback observation absent")
			}
			reply.Text = "Final answer based on fallback"
		default:
			return llmgateway.RoutedResult{}, fmt.Errorf("unexpected model request")
		}
		if actor.Authorize != nil {
			// The leaf's simulated revocation happens after dispatch.
			if !strings.HasSuffix(actor.OperationKey, "/delegate/1.1.1/false") {
				if err := actor.Authorize(callCtx); err != nil {
					return llmgateway.RoutedResult{}, err
				}
			}
			if len(request.Messages) != 2 || strings.Contains(request.Messages[1].Content, payload.Goal) {
				t.Error("child received the root conversation instead of only the delegated message")
			}
		}
		if next != "" {
			reply.FinishReason = "tool_calls"
			reply.ToolCalls = []resources.ToolCall{{ID: "delegate-" + next, Type: "function", Function: resources.ToolFunction{
				Name: delegateTool, Arguments: fmt.Sprintf(`{"target":%q,"message":"Only this subtask"}`, next),
			}}}
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: reply}}, nil
	})}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(a.AdvanceTurn, activity.RegisterOptions{Name: AdvanceTurnActivity})
	env.RegisterActivityWithOptions(a.ExecuteTool, activity.RegisterOptions{Name: ExecuteToolActivity})
	env.ExecuteWorkflow(GeneralAgentWorkflow, response.ID)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(order, []string{"root", "primary", "middle", "leaf", "fallback", "finish"}) {
		t.Fatalf("unexpected delegation graph execution: %v", order)
	}
	var state string
	var dropped, adopted, spent int
	err = db.QueryRow(ctx, `select r.status,count(*) filter(where a.state='discarded'),count(*) filter(where a.adopted),project_agent_grant_usage($2)
	 from runs r join general_delegate_attempts a on a.run_id=r.id where r.id=$1 group by r.status`, response.ID, leafGrant).
		Scan(&state, &dropped, &adopted, &spent)
	if err != nil || state != "completed" || dropped != 1 || adopted != 1 || spent != 1 {
		t.Fatalf("wrong receipts: run=%s discarded=%d accepted=%d grant uses=%d error=%v", state, dropped, adopted, spent, err)
	}
}
