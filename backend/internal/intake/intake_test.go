package intake

import (
	"alliance/internal/agentloop"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixtureGateway func(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)

func (g fixtureGateway) Call(ctx context.Context, actor llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
	return g(ctx, actor, request)
}

func TestIntakeRejectsFabricatedIDsAndUncoveredMessages(t *testing.T) {
	events := []intakeEvent{{Text: "ORD-123 请退款"}, {Text: "没有明确的对象编号"}}
	for _, value := range []string{
		`{"items":[{"event":1,"objectType":"order","objectKey":"ORD-12","intent":"refund","evidence":"ORD-123 请退款"}],"unresolved":[{"event":2,"reason":"缺编号"}]}`,
		`{"items":[{"event":1,"objectType":"order","objectKey":"ORD-123","intent":"refund","evidence":"ORD-123 请退款"}]}`,
		`{"items":[{"event":1,"objectType":"order","objectKey":"ORD-123","intent":"refund","evidence":"ORD-123 已收款"}],"unresolved":[{"event":2,"reason":"缺编号"}]}`,
	} {
		if _, err := parseExtraction(value, events); err == nil {
			t.Fatalf("accepted invalid result %s", value)
		}
	}
}

func TestIntakeDeduplicatesMessagesAndContinuesOnlySelectedCase(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires scratch PostgreSQL database with pgvector")
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
	vault, err := resources.NewVault(strings.Repeat("e4", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, viewer, team, project, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, person := range []string{owner, viewer} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", person, person+"@example.test")
	}
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, owner, viewer)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Fixture',2)", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,false)", project, owner, viewer)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,2,'Existing plan',$2)", project, owner)
	exec("insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://intake-fixture.example.test/v1')", provider)
	exec("insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens) values($1,$2,'fixture',array['text','tool_call'],16384,4096)", model, provider)
	exec("insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny) values($1,now(),0,0,0,0)", model)
	exec("insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)", model)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", model)
	source := uuid.NewString()
	exec("insert into objects(hash,size_bytes) values(repeat('d',64),10) on conflict do nothing")
	exec("insert into sources(id,project_id,name,created_by) values($1,$2,'Shipment fixture',$3)", source, project, owner)
	exec(`insert into source_versions(source_id,version,object_hash,media_type,author,filename,extracted)
  values($1,1,repeat('d',64),'text/plain',$2,'fixture.txt','SHIP-451 需要查物流')`, source, owner)
	calls := 0
	revoke := false
	server := Server{DB: db, Vault: vault, Gateway: fixtureGateway(func(_ context.Context, actor llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
		calls++
		if actor.TeamID != team || actor.MemberID != owner || len(request.Tools) != 0 || len(request.Messages) != 2 {
			t.Error("intake attempted to execute tools or changed the payer")
		}
		input := request.Messages[1].Content
		answer := `{"items":[{"event":1,"objectType":"order","objectKey":"ORD-123","intent":"refund","evidence":"ORD-123 需要退款"},
{"event":1,"objectType":"order","objectKey":"ORD-123","intent":"billing","evidence":"ORD-123 需要退款并补发票"},
{"event":2,"objectType":"shipment","objectKey":"SHIP-451","intent":"delivery","evidence":"SHIP-451 需要查物流"}],
"unresolved":[{"event":3,"reason":"缺少编号"}]}`
		if strings.Contains(input, "继续退款") {
			if strings.Contains(input, "SHIP-451") || strings.Contains(input, "补发票") {
				t.Error("unrelated case leaked into continuation")
			}
			answer = "请按 ORD-123 核对退款状态"
		} else if revoke {
			if strings.Contains(input, "ORD-123") {
				t.Error("unrelated case leaked into shipment")
			}
			exec("update sources set revoked_at=now() where id=$1", source)
			answer = "should be withheld"
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: resources.Reply{Text: answer}}}, nil
	})}
	action := func(handler identity.Action, actor string, status int, paths map[string]string, key, input string) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(input))
		r.SetPathValue("id", project)
		for k, v := range paths {
			r.SetPathValue(k, v)
		}
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		err := handler(w, r, identity.Member{ID: actor})
		var issue web.Problem
		if errors.As(err, &issue) && issue.Status == status {
			return issue.Message
		}
		if err != nil || w.Code != status {
			t.Fatalf("got %d want %d: %s %v", w.Code, status, w.Body, err)
		}
		return w.Body.String()
	}
	summary := func(data string) map[string]string {
		t.Helper()
		var values map[string]string
		if err := json.Unmarshal([]byte(data), &values); err != nil {
			t.Fatal(err)
		}
		return values
	}
	raw := `{"messages":[{"key":"one","text":"ORD-123 需要退款并补发票"},{"key":"shipment","source":"` + source + `","version":1},{"key":"unknown","text":"请尽快处理"}]}`
	batch := summary(action(server.createBatch, owner, 202, nil, "batch-one", raw))
	if replay := summary(action(server.createBatch, owner, 200, nil, "batch-one", raw)); replay["run"] != batch["run"] {
		t.Fatal("duplicate batch started another run")
	}
	worker := agentloop.Activities{DB: db, Vault: vault, Intake: server}
	advance := func(run string) {
		t.Helper()
		if result, err := worker.AdvanceTurn(ctx, agentloop.TurnRef{RunID: run, Turn: 1}); err != nil || !result.Done {
			t.Fatalf("intake did not complete: %+v, %v", result, err)
		}
	}
	advance(batch["run"])
	advance(batch["run"])
	paths := map[string]string{"batch": batch["id"]}
	action(server.confirm, viewer, 403, paths, "", `{"expected":2,"positions":[1,2,3]}`)
	committed := action(server.confirm, owner, 200, paths, "", `{"expected":2,"positions":[1,2,3]}`)
	if got := action(server.confirm, owner, 200, paths, "", `{"expected":2,"positions":[1,2,3]}`); got != committed {
		t.Fatal("confirmation replay lost its receipt")
	}
	retryBatch := summary(action(server.createBatch, owner, 202, nil, "batch-two", raw))
	advance(retryBatch["run"])
	second := action(server.confirm, owner, 200, map[string]string{"batch": retryBatch["id"]}, "", `{"expected":2,"positions":[1,2,3]}`)
	if second != committed {
		t.Fatal("repeated messages created additional cases")
	}
	action(server.createBatch, owner, 409, nil, "changed", strings.Replace(raw, "ORD-123 需要退款并补发票", "ORD-123 已退款", 1))
	var refund, shipment string
	var revision, shipRevision int
	if err := db.QueryRow(ctx, "select id,revision from intake_cases where project_id=$1 and intent='refund'", project).Scan(&refund, &revision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "select id,revision from intake_cases where project_id=$1 and intent='delivery'", project).Scan(&shipment, &shipRevision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || shipRevision != 2 || calls != 2 {
		t.Fatalf("replays changed case revisions or charges: %d %d %d", revision, shipRevision, calls)
	}
	run := summary(action(server.continueCase, owner, 202, map[string]string{"case": refund}, "continue-refund", `{"expected":2,"instruction":"继续退款"}`))
	advance(run["run"])
	detail := action(server.caseDetail, owner, 200, map[string]string{"case": refund}, "", "")
	if !strings.Contains(detail, "请按 ORD-123 核对退款状态") || strings.Contains(detail, "SHIP-451") {
		t.Fatalf("unexpected case history %s", detail)
	}
	revoke = true
	run = summary(action(server.continueCase, owner, 202, map[string]string{"case": shipment}, "continue-shipment", `{"expected":2,"instruction":"核对当前运单"}`))
	advance(run["run"])
	var failure string
	var confidential []byte
	if err := db.QueryRow(ctx, "select failure,result_cipher from general_intake_work where run_id=$1", run["run"]).Scan(&failure, &confidential); err != nil || !strings.Contains(failure, "访问权") || len(confidential) != 0 {
		t.Fatalf("stale source result was persisted: %s %v", failure, err)
	}
	var plan, version int
	if err := db.QueryRow(ctx, "select p.plan_version,c.revision from projects p join intake_cases c on c.project_id=p.id where p.id=$1 and c.id=$2",
		project, shipment).Scan(&plan, &version); err != nil || plan != 2 || version != 2 {
		t.Fatalf("continued case incorrectly changed project or neighbor: %d %d %v", plan, version, err)
	}
	if visible := action(server.cases, owner, 200, nil, "", ""); strings.Contains(visible, "SHIP-451") {
		t.Fatalf("revoked case remained in the list: %s", visible)
	}
}
