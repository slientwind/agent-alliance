package agentloop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"alliance/internal/bizconnectors"
	"alliance/internal/database"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type testBusinessHTTP func(*http.Request) (*http.Response, error)

func (f testBusinessHTTP) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestBusinessViewThroughGeneralLoop(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("bc", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, actor, project, run, connector, view := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','Semantic owner','','member')", actor)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, actor)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Return rates',1)", project, actor, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, actor)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'查询 SKU 退货率',$2)", project, actor)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
		values($1,$2,1,$3,'running',$4,$1::uuid::text,$5,4)`, run, project, actor, team, web.Hash(run))
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	cipher, err := vault.Seal(`{"username":"readonly"}`, "business-connector/"+connector)
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,credential_cipher,settings,maintainer)
		select $1,space_id,'warehouse','readonly_sql','clickhouse','Shipping data','https://warehouse.example.test','service',
		 $2,'{"database":"shipping","allowedRelations":["daily_orders"]}',$3 from projects where id=$4`, connector, cipher, actor, project)
	exec(`insert into semantic_data_views(id,connector_id,space_id,view_name,business_desc,relation_name,allowed_dimensions,
		 verified_metrics_json,time_field,defaults,verified_by)
		select $1,$2,space_id,'SKU 净退货率','已核定店铺退货口径','daily_orders',
		'{"sku":{"source":"item","type":"string","label":"SKU"},"sold":{"source":"shipped_qty","label":"发货量","type":"integer"},
		  "returned":{"source":"return_qty","label":"净退货量","type":"integer"},"date":{"source":"dt","label":"日期","type":"date"},
		  "status":{"source":"state","label":"订单状态","type":"string"}}',
		'{"net_rate":{"label":"净退货率","formula":"sum(returned) / sum(sold) * 100"}}','date','{"status":"settled"}',$3
		from projects where id=$4`, view, connector, actor, project)

	requests := 0
	biz := &bizconnectors.Server{DB: db, Vault: vault, HTTP: func(context.Context, string) (*http.Client, error) {
		return &http.Client{Transport: testBusinessHTTP(func(r *http.Request) (*http.Response, error) {
			requests++
			sql, _ := io.ReadAll(r.Body)
			params := r.URL.Query()
			if !strings.Contains(string(sql), "SUM(`return_qty`)") || !strings.Contains(string(sql), "group by `item`") ||
				params.Get("param_arg1") != "settled" || params.Get("param_arg2") != "2026-10-01" || params.Get("param_arg3") != "2026-11-01" {
				t.Fatalf("warehouse query missed reviewed predicates or exact aggregates: %s %v", sql, params)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"sku":"SKU-5","_measure_0":3,"_measure_1":8}]}`))}, nil
		})}, nil
	}}
	a := Activities{DB: db, Vault: vault, Business: biz}
	turns := 0
	a.Gateway = testGateway(func(ctx context.Context, id llmgateway.CallIdentity, request llmgateway.Request) (llmgateway.RoutedResult, error) {
		turns++
		if turns == 1 {
			if !slices.ContainsFunc(request.Tools, func(tool resources.ToolDefinition) bool { return tool.Function.Name == businessViewQuery }) {
				t.Fatal("business view tool was not registered in the normal loop")
			}
			reply := resources.Reply{FinishReason: "tool_calls"}
			call := resources.ToolCall{ID: "query", Type: "function"}
			call.Function.Name = businessViewQuery
			call.Function.Arguments = fmt.Sprintf(`{"viewId":%q,"dimensions":["sku"],"metrics":["net_rate"],"from":"2026-10-01","until":"2026-11-01"}`, view)
			reply.ToolCalls = []resources.ToolCall{call}
			return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: reply}}, nil
		}
		prepared, err := id.Prepare(ctx, llmgateway.Candidate{ModelID: "fixture", Endpoint: "https://model.example.test/v1"}, request)
		if err != nil {
			return llmgateway.RoutedResult{}, err
		}
		if err := prepared.Authorize(ctx); err != nil {
			return llmgateway.RoutedResult{}, err
		}
		message := prepared.Request.Messages[len(prepared.Request.Messages)-1]
		if message.Role != "tool" || !strings.Contains(message.Content, "37.5") || !strings.Contains(message.Content, "SKU 净退货率") {
			t.Fatalf("approved formula and provenance absent from tool response: %s", message.Content)
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: resources.Reply{Text: "SKU-5 净退货率 37.5%"}}}, nil
	})
	if first, err := a.AdvanceTurn(ctx, TurnRef{RunID: run, Turn: 1}); err != nil || first.Tools != 1 {
		t.Fatalf("first turn: %+v %v", first, err)
	}
	if next, err := a.ExecuteTool(ctx, ToolRef{RunID: run, Turn: 1, Position: 1}); err != nil || next.Wait || requests != 1 {
		t.Fatalf("tool result: %+v %v, requests=%d", next, err, requests)
	}
	if final, err := a.AdvanceTurn(ctx, TurnRef{RunID: run, Turn: 2}); err != nil || !final.Done || turns != 2 {
		t.Fatalf("final turn: %+v %v, turns=%d", final, err, turns)
	}
	var visible bool
	var provenance string
	const visibility = `select distribution_readable(a.id,a.current_version,$2),business_run_provenance(a.run_id,$2)::text
	 from general_agent_runs g join artifacts a on a.id=g.final_reply where g.run_id=$1`
	if err := db.QueryRow(ctx, visibility, run, actor).Scan(&visible, &provenance); err != nil || !visible || !strings.Contains(provenance, "sum(returned) / sum(sold) * 100") {
		t.Fatalf("expected visible persisted provenance: %t %s %v", visible, provenance, err)
	}
	exec("update semantic_data_views set enabled=false,revision=revision+1 where id=$1", view)
	if err := db.QueryRow(ctx, visibility, run, actor).Scan(&visible, &provenance); err != nil || visible || provenance != "[]" {
		t.Fatalf("revoked view permitted historical answer: %t %s %v", visible, provenance, err)
	}
}
