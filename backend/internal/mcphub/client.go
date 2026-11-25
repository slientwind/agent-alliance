package mcphub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/egress"
	"alliance/internal/harness"
	"alliance/internal/resources"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	StreamableHTTP = "streamable_http"
	SSE            = "sse"
	responseLimit  = 1 << 20
)

var (
	ErrUnavailable = errors.New("MCP 服务暂不可用")
	ErrApproval    = errors.New("该 MCP 工具需要审核授权")
	toolName       = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	aliasChars     = regexp.MustCompile(`[^A-Za-z0-9_-]`)
)

type ServerConfig struct {
	ID               string
	Endpoint         string
	Transport        string
	EncryptedBearer  []byte
	AllowedTools     map[string]string
	AllowPrivateHTTP bool
	authorized       func(context.Context) bool
}

type Client struct {
	Vault  resources.Vault
	Egress egress.Policy
	HTTP   *http.Client
}

func bearerScope(id string) string { return "mcp-server/" + id + "/bearer" }

func parseEndpoint(raw string, allowPrivateHTTP bool) (*url.URL, error) {
	endpoint, err := url.Parse(raw)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery ||
		endpoint.Fragment != "" || (endpoint.Scheme != "https" && !(allowPrivateHTTP && endpoint.Scheme == "http")) {
		return nil, egress.ErrBlocked
	}
	return endpoint, nil
}

func endpointPort(endpoint *url.URL) string {
	if value := endpoint.Port(); value != "" {
		return value
	}
	if endpoint.Scheme == "http" {
		return "80"
	}
	return "443"
}

type authorizedTransport struct {
	endpoint *url.URL
	base     *http.Client
	policy   egress.Policy
	bearer   string
	allowed  func(context.Context) bool
}

func (t authorizedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	target := request.URL
	if target.User != nil || target.Fragment != "" || target.Scheme != t.endpoint.Scheme ||
		!strings.EqualFold(target.Hostname(), t.endpoint.Hostname()) || endpointPort(target) != endpointPort(t.endpoint) {
		return nil, egress.ErrBlocked
	}
	if t.allowed != nil && !t.allowed(request.Context()) {
		return nil, egress.ErrBlocked
	}
	client, err := t.policy.Client(request.Context(), target.String(), t.endpoint.Scheme == "http", t.base)
	if err != nil {
		return nil, ErrUnavailable
	}
	out := request.Clone(request.Context())
	out.Header.Del("Authorization")
	if t.bearer != "" {
		out.Header.Set("Authorization", "Bearer "+t.bearer)
	}
	response, err := client.Transport.RoundTrip(out)
	if err != nil {
		return nil, ErrUnavailable
	}
	if response.ContentLength > responseLimit {
		response.Body.Close()
		return nil, ErrUnavailable
	}
	response.Body = limitedBody{Reader: io.LimitReader(response.Body, responseLimit+1), Closer: response.Body}
	return response, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func validBearer(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	for _, c := range value {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

type Session struct {
	raw    *mcp.ClientSession
	server ServerConfig
	cancel context.CancelFunc
}

func (c Client) Open(ctx context.Context, config ServerConfig) (*Session, error) {
	id, err := uuid.Parse(config.ID)
	if err != nil || id.String() != config.ID || id == uuid.Nil {
		return nil, ErrUnavailable
	}
	endpoint, err := parseEndpoint(config.Endpoint, config.AllowPrivateHTTP)
	if err != nil || !c.Egress.Allowed(ctx, config.Endpoint, config.AllowPrivateHTTP) {
		return nil, ErrUnavailable
	}
	bearer := ""
	if len(config.EncryptedBearer) != 0 {
		if endpoint.Scheme != "https" {
			return nil, ErrUnavailable
		}
		bearer, err = c.Vault.Open(config.EncryptedBearer, bearerScope(config.ID))
		if err != nil || !validBearer(bearer) {
			return nil, ErrUnavailable
		}
	}
	base := c.HTTP
	if base == nil {
		base, err = resources.ProviderHTTPClient()
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	client := &http.Client{
		Transport:     authorizedTransport{endpoint: endpoint, base: base, policy: c.Egress, bearer: bearer, allowed: config.authorized},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var transport mcp.Transport
	version := "2025-11-25"
	switch config.Transport {
	case StreamableHTTP:
		transport = &mcp.StreamableClientTransport{Endpoint: config.Endpoint, HTTPClient: client,
			DisableStandaloneSSE: true, MaxEventSize: responseLimit, MaxRetries: -1}
	case SSE:
		transport = &mcp.SSEClientTransport{Endpoint: config.Endpoint, HTTPClient: client, MaxEventSize: responseLimit}
		version = "2024-11-05"
	default:
		return nil, ErrUnavailable
	}
	sdk := mcp.NewClient(&mcp.Implementation{Name: "agent-alliance", Version: "1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, Logger: slog.New(slog.DiscardHandler)})
	connect, stop := context.WithCancel(ctx)
	handshake := time.AfterFunc(12*time.Second, stop)
	session, err := sdk.Connect(connect, transport, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if !handshake.Stop() || err != nil {
		stop()
		if session != nil {
			_ = session.Close()
		}
		return nil, ErrUnavailable
	}
	config.AllowedTools = maps.Clone(config.AllowedTools)
	return &Session{raw: session, server: config, cancel: stop}, nil
}

func (s *Session) Close() {
	s.cancel()
	_ = s.raw.Close()
}

type Tool struct {
	Name       string                   `json:"name"`
	Definition resources.ToolDefinition `json:"definition"`
	Risk       harness.ToolRisk         `json:"risk"`
	Level      harness.AutonomyLevel    `json:"level"`
	Allowed    bool                     `json:"allowed"`
	ReadOnly   bool                     `json:"readOnlyHint"`
}

func toolAlias(server, name string) string {
	sum := sha256.Sum256([]byte(server + "/" + name))
	label := aliasChars.ReplaceAllString(name, "_")
	return "mcp_" + label[:min(len(label), 39)] + "_" + hex.EncodeToString(sum[:8])
}

func (s *Session) Tools(ctx context.Context) ([]Tool, error) {
	return s.discover(ctx, false)
}

func (s *Session) discover(ctx context.Context, includeUnconfigured bool) ([]Tool, error) {
	ctx, stop := context.WithTimeout(ctx, 20*time.Second)
	defer stop()
	tools := make([]Tool, 0)
	cursor := ""
	seen := map[string]bool{}
	for range 10 {
		reply, err := s.raw.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil || reply == nil || len(reply.Tools) > 200 {
			return nil, ErrUnavailable
		}
		for _, tool := range reply.Tools {
			if tool == nil || !toolName.MatchString(tool.Name) || seen[tool.Name] {
				return nil, ErrUnavailable
			}
			seen[tool.Name] = true
			if mode, ok := s.server.AllowedTools[tool.Name]; ok || includeUnconfigured {
				registered, err := s.definition(tool, mode)
				if err != nil {
					return nil, err
				}
				registered.Allowed = mode == "write" || (registered.ReadOnly && (mode == "read_only" || mode == "paid_read"))
				if registered.Allowed || includeUnconfigured {
					tools = append(tools, registered)
				}
			}
		}
		if reply.NextCursor == "" {
			return tools, nil
		}
		if len(reply.NextCursor) > 512 || reply.NextCursor == cursor {
			return nil, ErrUnavailable
		}
		cursor = reply.NextCursor
	}
	return nil, ErrUnavailable
}

func (s *Session) definition(tool *mcp.Tool, mode string) (Tool, error) {
	schema, err := json.Marshal(tool.InputSchema)
	var root struct{ Type string }
	if err != nil || len(schema) > 32<<10 || json.Unmarshal(schema, &root) != nil || root.Type != "object" {
		return Tool{}, ErrUnavailable
	}
	description := strings.TrimSpace(tool.Description)
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > 2000 {
		return Tool{}, ErrUnavailable
	}
	result := Tool{
		Name: tool.Name, Risk: harness.SystemWrite, Level: harness.L2,
		ReadOnly: tool.Annotations != nil && tool.Annotations.ReadOnlyHint,
		Definition: resources.ToolDefinition{Type: "function", Function: resources.ToolSchema{
			Name: toolAlias(s.server.ID, tool.Name), Description: description, Parameters: schema,
		}},
	}
	if result.ReadOnly {
		switch mode {
		case "read_only":
			result.Risk, result.Level = harness.ReadOnly, harness.L0
		case "paid_read":
			result.Risk, result.Level = harness.ReadOnly, harness.L1
		}
	}
	return result, nil
}

// Call re-discovers the named tool so a server-side capability change cannot
// silently reuse an approval for an older read-only schema.
func (s *Session) Call(ctx context.Context, alias string, args json.RawMessage, approve func(context.Context, Tool, json.RawMessage) error) (json.RawMessage, error) {
	task, stop := context.WithTimeout(ctx, 45*time.Second)
	defer stop()
	var object map[string]json.RawMessage
	if len(args) > 32<<10 || json.Unmarshal(args, &object) != nil || object == nil {
		return nil, errors.New("MCP 工具参数格式无效")
	}
	tools, err := s.Tools(task)
	if err != nil {
		return nil, err
	}
	for _, tool := range tools {
		if tool.Definition.Function.Name != alias {
			continue
		}
		if approve == nil && tool.Level != harness.L0 {
			return nil, ErrApproval
		}
		if approve != nil {
			if err := approve(task, tool, bytes.Clone(args)); err != nil {
				return nil, err
			}
		}
		result, err := s.raw.CallTool(task, &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		if err != nil {
			return nil, ErrUnavailable
		}
		body, err := json.Marshal(result)
		if err != nil || len(body) > responseLimit {
			return nil, ErrUnavailable
		}
		return body, nil
	}
	return nil, errors.New("MCP 工具不可用或未授权")
}
