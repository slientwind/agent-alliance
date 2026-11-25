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
	"alliance/internal/mcphub"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestGeneralRunMCPToolsRespectTeamGrant(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("c2", 32))
	if err != nil {
		t.Fatal(err)
	}

	const resultText = "record fixture 27"
	service := mcp.NewServer(&mcp.Implementation{Name: "records", Version: "1"}, nil)
	var reads, writes atomic.Int32
	schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
	addTool := func(name string, calls *atomic.Int32) {
		service.AddTool(&mcp.Tool{Name: name, InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
			func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				var args struct{ Query string }
				if err := json.Unmarshal(request.Params.Arguments, &args); err != nil || args.Query != "fixture" {
					return nil, fmt.Errorf("unexpected tool arguments: %s", request.Params.Arguments)
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: resultText}}}, nil
			})
	}
	addTool("lookup", &reads)
	addTool("mutate", &writes)
	srv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return service },
		&mcp.StreamableHTTPOptions{JSONResponse: true}))
	defer srv.Close()
	endpoint, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}

	team, owner, provider, model, serverID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(ctx context.Context, sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Error(err)
		}
	}
	exec(ctx, `insert into teams(id,name) values($1,$1::uuid::text)`, team)
	exec(ctx, `insert into members(id,email,display_name,password_hash,role) values($1,$2,'mcp test','','member')`, owner, owner+"@example.test")
	exec(ctx, `insert into team_members(team_id,member_id) values($1,$2)`, team, owner)
	exec(ctx, `insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')`, provider)
	exec(ctx, `insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
		values($1,$2,'mcp-test-model',array['text','tool_call'],8192,512)`, model, provider)
	exec(ctx, `insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
		values($1,now(),0,0,0,0)`, model)
	exec(ctx, `insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)`, model)
	defer db.Exec(context.Background(), `delete from model_tier_routes where model_id=$1`, model)
	exec(ctx, `insert into outbound_host_allowlist(host,port,trusted_networks) values($1,$2,array['127.0.0.1/32'::cidr])`, endpoint.Hostname(), port)
	exec(ctx, `insert into mcp_servers(id,name,endpoint,transport,tools,allow_private_http,enabled)
		values($1,'records',$2,'streamable_http','{"lookup":"read_only","mutate":"write"}',true,true)`, serverID, srv.URL)
	exec(ctx, `insert into mcp_team_grants(server_id,team_id,enabled) values($1,$2,true)`, serverID, team)
	runtime := &mcphub.Runtime{DB: db, Client: mcphub.Client{Vault: vault, Egress: egress.Policy{DB: db}}}

	for _, mode := range []string{"granted", "revoked before call", "not granted"} {
		t.Run(mode, func(t *testing.T) {
			project := uuid.NewString()
			exec(ctx, `insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'mcp fixture')`, project, owner, team)
			exec(ctx, `insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)`, project, owner)
			var revision int
			if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/projects/"+project+"/general-runs",
				strings.NewReader(fmt.Sprintf(`{"expected":%d,"goal":"Look up the record"}`, revision)))
			request.SetPathValue("id", project)
			request.Header.Set("Idempotency-Key", "mcp-fixture-"+project)
			if err := (Server{DB: db}).start(w, request, identity.Member{ID: owner}); err != nil {
				t.Fatal(err)
			}
			var reply struct{ ID string }
			if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != http.StatusAccepted || reply.ID == "" {
				t.Fatalf("start got %d %s (%v)", w.Code, w.Body, err)
			}
			var modelCalls atomic.Int32
			a := Activities{DB: db, Vault: vault, Objects: knowledge.Objects{Root: t.TempDir()}, MCP: runtime,
				Gateway: testGateway(func(callCtx context.Context, _ llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
					var alias string
					for _, tool := range in.Tools {
						switch {
						case strings.Contains(tool.Function.Name, "_mutate"):
							return llmgateway.RoutedResult{}, fmt.Errorf("write tool exposed as automatic: %q", tool.Function.Name)
						case strings.Contains(tool.Function.Name, "_lookup"):
							alias = tool.Function.Name
						}
					}
					out := resources.Reply{FinishReason: "stop", Text: "Fixture handled."}
					switch modelCalls.Add(1) {
					case 1:
						if (alias != "") != (mode != "not granted") {
							return llmgateway.RoutedResult{}, fmt.Errorf("unexpected granted tool %q", alias)
						}
						if mode == "revoked before call" {
							exec(callCtx, `update mcp_team_grants set enabled=false,revision=revision+1 where (server_id,team_id)=($1,$2)`, serverID, team)
						}
						if alias != "" {
							out = resources.Reply{FinishReason: "tool_calls", ToolCalls: []resources.ToolCall{
								{ID: "mcp-read", Type: "function", Function: resources.ToolFunction{Name: alias, Arguments: `{"query":"fixture"}`}},
							}}
						}
					case 2:
						last := in.Messages[len(in.Messages)-1]
						if last.ToolCallID != "mcp-read" || strings.Contains(last.Content, resultText) != (mode == "granted") ||
							(alias != "") != (mode == "granted") {
							return llmgateway.RoutedResult{}, fmt.Errorf("unexpected observation for %s: %v %s", mode, last, alias)
						}
					default:
						return llmgateway.RoutedResult{}, fmt.Errorf("unexpected model replay")
					}
					return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: out}}, nil
				})}
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.RegisterActivityWithOptions(a.AdvanceTurn, activity.RegisterOptions{Name: AdvanceTurnActivity})
			env.RegisterActivityWithOptions(a.ExecuteTool, activity.RegisterOptions{Name: ExecuteToolActivity})
			env.ExecuteWorkflow(GeneralAgentWorkflow, reply.ID)
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			var state string
			if err := db.QueryRow(ctx, "select status from runs where id=$1", reply.ID).Scan(&state); err != nil || state != "completed" {
				t.Fatalf("run ended as %q: %v", state, err)
			}
			if reads.Load() != 1 || writes.Load() != 0 {
				t.Fatalf("unexpected outbound dispatches: read=%d write=%d", reads.Load(), writes.Load())
			}
		})
	}
}
