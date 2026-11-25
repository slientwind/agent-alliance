package mcphub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"alliance/internal/egress"
	"alliance/internal/harness"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type loopbackAllowlist struct{ disabled *atomic.Bool }

func (a loopbackAllowlist) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	return a
}

func TestLegacyEndpointCannotChangeCredentialOrigin(t *testing.T) {
	var foreignCalls atomic.Int32
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		foreignCalls.Add(1)
		w.WriteHeader(204)
	}))
	defer foreign.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", foreign.URL)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	vault, err := resources.NewVault(strings.Repeat("d1", 32))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	sealed, err := vault.Seal("scoped-fixture", bearerScope(id))
	if err != nil {
		t.Fatal(err)
	}
	client := Client{Vault: vault, Egress: egress.Policy{DB: loopbackAllowlist{}}, HTTP: origin.Client()}
	if connected, err := client.Open(t.Context(), ServerConfig{ID: id, Endpoint: origin.URL, Transport: SSE, EncryptedBearer: sealed}); err == nil {
		connected.Close()
		t.Fatal("legacy MCP endpoint escaped its configured origin")
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("foreign endpoint received a connection")
	}
}

func (a loopbackAllowlist) Scan(dest ...any) error {
	if a.disabled != nil && a.disabled.Load() {
		return pgx.ErrNoRows
	}
	*dest[0].(*[]string) = []string{"127.0.0.1/32"}
	return nil
}

func TestMCPTransportsDiscoverAndGateTools(t *testing.T) {
	for _, tt := range []struct {
		name      string
		transport string
		json      bool
	}{
		{name: "streamable JSON", transport: StreamableHTTP, json: true},
		{name: "streamable SSE", transport: StreamableHTTP},
		{name: "legacy SSE", transport: SSE},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
			var calls atomic.Int32
			handler := func(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				calls.Add(1)
				var in struct{ Query string }
				if json.Unmarshal(request.Params.Arguments, &in) != nil || in.Query != "采购" {
					return nil, errors.New("unexpected test argument")
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "测试结果"}}}, nil
			}
			schema := json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
			server.AddTool(&mcp.Tool{Name: "lookup.records", Description: "读取企业记录", InputSchema: schema,
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, handler)
			server.AddTool(&mcp.Tool{Name: "save", InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, handler)
			server.AddTool(&mcp.Tool{Name: "unknown", InputSchema: schema, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, handler)
			newServer := func(*http.Request) *mcp.Server { return server }
			var transportHandler http.Handler = mcp.NewStreamableHTTPHandler(newServer, &mcp.StreamableHTTPOptions{JSONResponse: tt.json})
			if tt.transport == SSE {
				transportHandler = mcp.NewSSEHandler(newServer, nil)
			}
			var missingBearer atomic.Bool
			endpoint := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer scoped-fixture" {
					missingBearer.Store(true)
					w.WriteHeader(401)
					return
				}
				transportHandler.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			vault, err := resources.NewVault(strings.Repeat("a1", 32))
			if err != nil {
				t.Fatal(err)
			}
			id := uuid.NewString()
			secret, err := vault.Seal("scoped-fixture", bearerScope(id))
			if err != nil {
				t.Fatal(err)
			}
			var blocked atomic.Bool
			client := Client{Vault: vault, HTTP: endpoint.Client(), Egress: egress.Policy{DB: loopbackAllowlist{disabled: &blocked}}}
			config := ServerConfig{ID: id, Endpoint: endpoint.URL, Transport: tt.transport, EncryptedBearer: secret,
				AllowedTools: map[string]string{"lookup.records": "read_only", "save": "write"}}
			session, err := client.Open(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			tools, err := session.Tools(t.Context())
			if err != nil || len(tools) != 2 {
				t.Fatalf("unexpected tools or error: %v %v", tools, err)
			}
			byName := map[string]Tool{}
			for _, tool := range tools {
				byName[tool.Name] = tool
			}
			read, write := byName["lookup.records"], byName["save"]
			if read.Level != harness.L0 || write.Level != harness.L2 || len(read.Definition.Function.Parameters) == 0 {
				t.Fatalf("unexpected risk mapping: %v %v", read.Level, write.Level)
			}
			all, err := session.discover(t.Context(), true)
			if err != nil || len(all) != 3 {
				t.Fatalf("admin could not discover unconfigured capabilities: %v %v", all, err)
			}
			for _, tool := range all {
				if tool.Name == "unknown" && (tool.Allowed || tool.Level != harness.L2) {
					t.Fatal("self-declared read-only status granted automatic execution")
				}
			}
			args := json.RawMessage(`{"query":"采购"}`)
			payload, err := session.Call(t.Context(), read.Definition.Function.Name, args, nil)
			if err != nil || !strings.Contains(string(payload), "测试结果") {
				t.Fatalf("authorized tool call failed: %s %v", payload, err)
			}
			if _, err = session.Call(t.Context(), write.Definition.Function.Name, args, nil); !errors.Is(err, ErrApproval) || calls.Load() != 1 {
				t.Fatalf("write ran without approval: %v", err)
			}
			if _, err = session.Call(t.Context(), write.Definition.Function.Name, args, func(_ context.Context, tool Tool, data json.RawMessage) error {
				if tool.Name != "save" || string(data) != string(args) {
					t.Fatal("approval did not receive the discovered tool and exact payload")
				}
				return nil
			}); err != nil || calls.Load() != 2 {
				t.Fatalf("reviewed tool call failed: %v", err)
			}
			blocked.Store(true)
			if _, err = session.Tools(t.Context()); err == nil || missingBearer.Load() {
				t.Fatalf("routing revoked after startup was accepted or credential was missing: %v", err)
			}
			blocked.Store(false)
		})
	}
}
