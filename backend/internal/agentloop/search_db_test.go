package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/websearch"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConversationSendsOnlyTheConsentedSearchQuery(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("19", 32))
	if err != nil {
		t.Fatal(err)
	}
	const publicQuery = "公示采购规则"
	const internalGoal = "将结果与内部项目代号项一核对"
	const resultURL = "https://public.example.test/rules"
	var searchCalls atomic.Int32
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := map[string]any{}
		err := json.NewDecoder(r.Body).Decode(&data)
		if err != nil || data["query"] != publicQuery || r.Header.Get("Authorization") != "Bearer search-fixture" {
			t.Errorf("wrong outbound search payload: %v %v", data, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		searchCalls.Add(1)
		fmt.Fprintf(w, `{"results":[{"title":"公示规则","url":%q,"content":"公开摘要"}]}`, resultURL)
	}))
	defer fixture.Close()
	endpoint := fixture.URL + "/search"
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.Seal("search-fixture", "integration/web-search/tavily/"+endpoint)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])`, parsed.Hostname(), port)
	exec(`update web_search_integration set provider='tavily',endpoint=$1,encrypted_key=$2,enabled=true,revision=revision+1`, endpoint, key)
	team, owner, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`insert into teams(id,name) values($1,$1::uuid::text)`, team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'search actor','','member')`, owner, owner+"@example.test")
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, owner)
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'search-test-model',array['text','tool_call'],8192,512)`, model, provider)
	exec(`insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,now(),0,0,0,0)`, model)
	exec(`insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)`, model)
	defer db.Exec(context.Background(), `delete from model_tier_routes where model_id=$1`, model)

	for _, tt := range []struct {
		name      string
		optedIn   bool
		args      string
		revoke    bool
		expectHit bool
	}{
		{name: "no consent", args: "{}"},
		{name: "model argument injection", optedIn: true, args: `{"query":"internal project name"}`},
		{name: "explicit consent", optedIn: true, args: "{}", expectHit: true},
		{name: "configuration withdrawn", optedIn: true, args: "{}", revoke: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			project := uuid.NewString()
			exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'search fixture')`, project, owner, team)
			exec(`insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)`, project, owner)
			var revision int
			if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			input := startInput{Goal: internalGoal, Expected: revision}
			if tt.optedIn {
				input.SearchQuery = publicQuery
			}
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequestWithContext(ctx, "POST", "/", strings.NewReader(string(raw)))
			request.SetPathValue("id", project)
			request.Header.Set("Idempotency-Key", project)
			w := httptest.NewRecorder()
			if err := (Server{DB: db, Vault: vault}).start(w, request, identity.Member{ID: owner}); err != nil {
				t.Fatal(err)
			}
			var reply struct{ ID string }
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || reply.ID == "" {
				t.Fatalf("invalid start response %s: %v", w.Body, err)
			}
			var turn int
			a := Activities{DB: db, Vault: vault, Objects: knowledge.Objects{Root: t.TempDir()},
				Search: &websearch.Runtime{DB: db, Client: websearch.Client{Vault: vault, HTTP: fixture.Client(), Egress: egress.Policy{DB: db}}},
				Gateway: testGateway(func(_ context.Context, _ llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
					turn++
					listed := false
					for _, tool := range in.Tools {
						listed = listed || tool.Function.Name == "web_search"
					}
					out := resources.Reply{FinishReason: "stop", Text: "本次查询结束。"}
					if turn == 1 {
						if listed != tt.optedIn || !strings.Contains(in.Messages[len(in.Messages)-1].Content, internalGoal) {
							t.Errorf("search availability or project context differs: %t", listed)
						}
						out = resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{
							{ID: "search", Type: "function", Function: resources.ToolFunction{Name: "web_search", Arguments: tt.args}},
						}}
					} else {
						last := in.Messages[len(in.Messages)-1]
						if listed || last.ToolCallID != "search" || strings.Contains(last.Content, resultURL) != tt.expectHit {
							t.Errorf("unexpected search observation: %+v", last)
						}
					}
					return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: out}}, nil
				})}
			ref := ToolRef{RunID: reply.ID, Turn: 1, Position: 1}
			before := searchCalls.Load()
			if result, err := a.AdvanceTurn(ctx, TurnRef{RunID: reply.ID, Turn: 1}); err != nil || result.Tools != 1 {
				t.Fatalf("first turn: %+v %v", result, err)
			}
			if tt.revoke {
				exec(`update web_search_integration set enabled=false,revision=revision+1`)
			}
			for range 2 {
				if result, err := a.ExecuteTool(ctx, ref); err != nil || result.Done || result.Wait {
					t.Fatalf("search action: %+v %v", result, err)
				}
			}
			if tt.expectHit {
				before++
			}
			if searchCalls.Load() != before {
				t.Fatalf("executed search %d times, wanted %d", searchCalls.Load(), before)
			}
			if result, err := a.AdvanceTurn(ctx, TurnRef{RunID: reply.ID, Turn: 2}); err != nil || !result.Done {
				t.Fatalf("second turn: %+v %v", result, err)
			}
		})
	}
}
