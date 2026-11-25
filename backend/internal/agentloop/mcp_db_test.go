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
	"alliance/internal/web"
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
	var paidReads atomic.Int32
	addTool("paid_lookup", &paidReads)
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
		values($1,'records',$2,'streamable_http','{"lookup":"read_only","mutate":"write","paid_lookup":"paid_read"}',true,true)`, serverID, srv.URL)
	exec(ctx, `insert into mcp_team_grants(server_id,team_id,enabled) values($1,$2,true)`, serverID, team)
	runtime := &mcphub.Runtime{DB: db, Client: mcphub.Client{Vault: vault, Egress: egress.Policy{DB: db}}}

	for _, mode := range []string{"granted", "revoked before call", "not granted", "accept paid", "skip paid", "revoke pending"} {
		t.Run(mode, func(t *testing.T) {
			paid := strings.HasSuffix(mode, "paid") || mode == "revoke pending"
			if paid {
				exec(ctx, `update mcp_team_grants set enabled=true,revision=revision+1 where (server_id,team_id)=($1,$2)`, serverID, team)
			}
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
						case paid && strings.HasPrefix(tool.Function.Name, "mcp_paid_lookup_"):
							alias = tool.Function.Name
						case !paid && strings.HasPrefix(tool.Function.Name, "mcp_lookup_"):
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
						observed := mode == "granted" || mode == "accept paid"
						granted := mode == "granted" || mode == "accept paid" || mode == "skip paid"
						if last.ToolCallID != "mcp-read" || strings.Contains(last.Content, resultText) != observed ||
							(alias != "") != granted {
							return llmgateway.RoutedResult{}, fmt.Errorf("unexpected observation for %s: %v %s", mode, last, alias)
						}
					default:
						return llmgateway.RoutedResult{}, fmt.Errorf("unexpected model replay")
					}
					return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: out}}, nil
				})}
			if paid {
				turn, err := a.AdvanceTurn(ctx, TurnRef{RunID: reply.ID, Turn: 1})
				if err != nil || turn.Tools != 1 {
					t.Fatalf("first turn: %+v %v", turn, err)
				}
				ref := ToolRef{RunID: reply.ID, Turn: 1, Position: 1}
				before := paidReads.Load()
				pending, err := a.ExecuteTool(ctx, ref)
				if err != nil || !pending.Wait || !pending.Recheck || paidReads.Load() != before {
					t.Fatalf("paid tool ran before confirmation: %+v %v", pending, err)
				}
				s := Server{DB: db}
				list := func(user string) []struct {
					ID, Level, Recipient, Tool string
					Revision                   int
				} {
					t.Helper()
					r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil)
					r.SetPathValue("id", project)
					w := httptest.NewRecorder()
					if err := s.toolApprovals(w, r, identity.Member{ID: user}); err != nil {
						t.Fatal(err)
					}
					var items []struct {
						ID, Level, Recipient, Tool string
						Revision                   int
					}
					if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
						t.Fatal(err)
					}
					return items
				}
				items := list(owner)
				if len(items) != 1 || items[0].Level != "L1" || items[0].Tool != "paid_lookup" || items[0].Recipient != endpoint.Host ||
					len(list(uuid.NewString())) != 0 {
					t.Fatalf("unexpected approval list: %+v", items)
				}
				decide := func(user string, revision int, accept bool) error {
					r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/",
						strings.NewReader(fmt.Sprintf(`{"expected":%d,"accept":%t}`, revision, accept)))
					r.SetPathValue("id", project)
					r.SetPathValue("approval", items[0].ID)
					return s.decideToolApproval(httptest.NewRecorder(), r, identity.Member{ID: user})
				}
				if err := decide(uuid.NewString(), items[0].Revision, true); err != (web.Problem{Status: 404, Message: "这项确认已失效或不可处理"}) {
					t.Fatalf("unrelated user could decide the request: %v", err)
				}
				if mode == "revoke pending" {
					exec(ctx, `update mcp_team_grants set enabled=false,revision=revision+1 where (server_id,team_id)=($1,$2)`, serverID, team)
					if len(list(owner)) != 0 || decide(owner, items[0].Revision, true) == nil {
						t.Fatal("revocation left an actionable confirmation")
					}
				} else {
					accept := mode == "accept paid"
					if err := decide(owner, items[0].Revision+1, accept); err == nil {
						t.Fatal("stale revision decided the request")
					}
					for range 2 {
						if err := decide(owner, items[0].Revision, accept); err != nil {
							t.Fatal(err)
						}
					}
					exec(ctx, `update general_agent_dispatch set sent_at=now() where run_id=$1`, reply.ID)
					var wakes int
					if err := a.sendToolDecision(ctx, func(_ context.Context, run, _, signal string, payload any) error {
						wakes++
						if run != "run/"+reply.ID || signal != "control" || payload != items[0].ID {
							t.Errorf("incorrect durable wake target: %s %s %v", run, signal, payload)
						}
						return nil
					}); err != nil || wakes != 1 {
						t.Fatalf("decision failed to wake workflow: %d %v", wakes, err)
					}
					if accept {
						before++
					}
				}
				for range 2 {
					result, err := a.ExecuteTool(ctx, ref)
					if err != nil || result.Wait || paidReads.Load() != before {
						t.Fatalf("paid execution or retry: %+v reads=%d want=%d %v", result, paidReads.Load(), before, err)
					}
				}
				if result, err := a.AdvanceTurn(ctx, TurnRef{RunID: reply.ID, Turn: 2}); err != nil || !result.Done {
					t.Fatalf("second turn: %+v %v", result, err)
				}
			} else {
				var suite testsuite.WorkflowTestSuite
				env := suite.NewTestWorkflowEnvironment()
				env.RegisterActivityWithOptions(a.AdvanceTurn, activity.RegisterOptions{Name: AdvanceTurnActivity})
				env.RegisterActivityWithOptions(a.ExecuteTool, activity.RegisterOptions{Name: ExecuteToolActivity})
				env.ExecuteWorkflow(GeneralAgentWorkflow, reply.ID)
				if err := env.GetWorkflowError(); err != nil {
					t.Fatal(err)
				}
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
