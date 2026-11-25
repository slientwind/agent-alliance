package knowledgesync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"alliance/internal/egress"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/net/html"
)

const textLimit = 256 << 10
const responseLimit = 1 << 20

var (
	documentID     = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)
	numericID      = regexp.MustCompile(`^[0-9]{1,32}$`)
	signedDingPath = regexp.MustCompile(`^/server/[A-Za-z0-9_-]{1,160}$`)
	dingAttachment = regexp.MustCompile(`https://[A-Za-z0-9.-]*oss[A-Za-z0-9.-]*\.aliyuncs\.com/[^\s<>()]+\?[^\s<>()]+`)
	errUnavailable = errors.New("remote document is unavailable")
	errDenied      = errors.New("remote document is no longer accessible")
	errInvalid     = errors.New("remote document response is invalid or too large")
	errEgress      = errors.New("remote host is not reachable under the outbound policy")
)

type settings struct {
	Document string `json:"document"`
	Account  string `json:"account"`
	Origin   string `json:"origin"`
}

func (s settings) valid(provider string) bool {
	if !documentID.MatchString(s.Document) || len(s.Origin) > 300 || len(s.Account) > 200 {
		return false
	}
	switch provider {
	case "feishu":
		return s.Origin == "" && documentID.MatchString(s.Account)
	case "dingtalk":
		return s.Origin == "" && s.Account == ""
	case "confluence":
		u, err := url.Parse(s.Origin)
		address, mailErr := mail.ParseAddress(s.Account)
		return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Path == "" && u.RawQuery == "" &&
			!u.ForceQuery && mailErr == nil && address.Address == s.Account && numericID.MatchString(s.Document)
	}
	return false
}

func credentialValid(provider, secret string) bool {
	if len(secret) == 0 || len(secret) > 4096 || strings.ContainsFunc(secret, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return false
	}
	if provider != "dingtalk" {
		return true
	}
	u, err := url.Parse(secret)
	if err != nil || u.Scheme != "https" || u.Host != "mcp-gw.dingtalk.com" || u.User != nil || u.Fragment != "" || !signedDingPath.MatchString(u.EscapedPath()) {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(query) == 1 && len(query["key"]) == 1 && query.Get("key") != ""
}

type providerRequest struct {
	provider string
	settings settings
	secret   string
}

type providerClient struct {
	egress egress.Policy
	// Tests can supply an in-process transport without external connections.
	httpClient func(context.Context, string) (*http.Client, error)
}

func (p providerClient) client(ctx context.Context, target string) (*http.Client, error) {
	if p.httpClient != nil {
		return p.httpClient(ctx, target)
	}
	client, err := p.egress.Client(ctx, target, false, &http.Client{Timeout: 15 * time.Second})
	if err != nil {
		return nil, errEgress
	}
	return client, nil
}

func (p providerClient) fetch(ctx context.Context, in providerRequest) (string, error) {
	var out string
	var err error
	switch in.provider {
	case "feishu":
		out, err = p.feishu(ctx, in.settings, in.secret)
	case "dingtalk":
		out, err = p.dingTalk(ctx, in.settings.Document, in.secret)
	case "confluence":
		out, err = p.confluence(ctx, in.settings, in.secret)
	default:
		err = errInvalid
	}
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if len(out) > textLimit || !utf8.ValidString(out) || strings.ContainsRune(out, 0) {
		return "", errInvalid
	}
	return out, nil
}

func (p providerClient) json(ctx context.Context, request *http.Request, dest any) error {
	client, err := p.client(ctx, request.URL.String())
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return errUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 401, 403, 404, 410:
		return errDenied
	case 200:
	case 400:
		return errInvalid
	default:
		return errUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit || json.Unmarshal(raw, dest) != nil {
		return errInvalid
	}
	return nil
}

func (p providerClient) feishu(ctx context.Context, s settings, secret string) (string, error) {
	const base = "https://open.feishu.cn/open-apis/"
	body, err := json.Marshal(map[string]string{"app_id": s.Account, "app_secret": secret})
	if err != nil {
		return "", errInvalid
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"auth/v3/tenant_access_token/internal", bytes.NewReader(body))
	if err != nil {
		return "", errInvalid
	}
	request.Header.Set("Content-Type", "application/json")
	var token struct {
		Code   *int
		Access string `json:"tenant_access_token"`
	}
	if err := p.json(ctx, request, &token); err != nil {
		return "", err
	}
	if token.Code == nil {
		return "", errInvalid
	}
	if *token.Code != 0 || token.Access == "" || strings.ContainsFunc(token.Access, unicode.IsSpace) {
		return "", errDenied
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"docx/v1/documents/"+s.Document+"/raw_content", nil)
	if err != nil {
		return "", errInvalid
	}
	request.Header.Set("Authorization", "Bearer "+token.Access)
	var doc struct {
		Code *int
		Data struct {
			Content *string
		}
	}
	if err := p.json(ctx, request, &doc); err != nil {
		return "", err
	}
	if doc.Code == nil {
		return "", errInvalid
	}
	switch *doc.Code {
	case 0:
	case 1770032:
		return "", errDenied
	case 1770033:
		return "", errInvalid
	default:
		return "", errUnavailable
	}
	if doc.Data.Content == nil {
		return "", errInvalid
	}
	return *doc.Data.Content, nil
}

func (p providerClient) confluence(ctx context.Context, s settings, secret string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Origin+"/wiki/api/v2/pages/"+s.Document+"?body-format=storage", nil)
	if err != nil {
		return "", errInvalid
	}
	request.SetBasicAuth(s.Account, secret)
	var doc struct {
		ID, Status string
		Body       struct {
			Storage struct{ Value *string }
		}
	}
	if err := p.json(ctx, request, &doc); err != nil {
		return "", err
	}
	if doc.ID != s.Document || doc.Body.Storage.Value == nil {
		return "", errInvalid
	}
	if !strings.EqualFold(doc.Status, "current") {
		return "", errDenied
	}
	return htmlText(*doc.Body.Storage.Value), nil
}

func htmlText(value string) string {
	input := html.NewTokenizer(strings.NewReader(value))
	var out strings.Builder
	skip := ""
	for {
		switch input.Next() {
		case html.ErrorToken:
			return out.String()
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			token := input.Token()
			name := token.Data
			if input.Err() != nil {
				continue
			}
			if skip == name && token.Type == html.EndTagToken {
				skip = ""
			} else if skip == "" && (name == "script" || name == "style") && token.Type == html.StartTagToken {
				skip = name
			}
			switch name {
			case "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "br":
				out.WriteByte('\n')
			case "td", "th":
				out.WriteByte(' ')
			}
		case html.TextToken:
			if skip == "" {
				out.WriteString(input.Token().Data)
			}
		}
	}
}

func (p providerClient) dingTalk(ctx context.Context, document, endpoint string) (string, error) {
	client, err := p.client(ctx, endpoint)
	if err != nil {
		return "", err
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	guard := &dingResponseGuard{base: base}
	bounded := *client
	bounded.Transport = guard
	sdk := mcp.NewClient(&mcp.Implementation{Name: "agent-alliance-knowledge-sync", Version: "1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, Logger: slog.New(slog.DiscardHandler)})
	transport := &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &bounded,
		DisableStandaloneSSE: true, MaxEventSize: responseLimit, MaxRetries: -1}
	session, err := sdk.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return "", guard.failure()
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_document_content",
		Arguments: map[string]string{"nodeId": document, "format": "markdown"}})
	if err != nil || result == nil {
		return "", guard.failure()
	}
	if result.IsError {
		return "", errDenied
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err == nil {
		if value, ok := dingContent(raw); ok {
			return dingAttachment.ReplaceAllString(value, "[附件链接]"), nil
		}
	}
	if len(result.Content) != 1 {
		return "", errInvalid
	}
	part, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		return "", errInvalid
	}
	value, ok := dingContent([]byte(part.Text))
	if !ok {
		return "", errInvalid
	}
	return dingAttachment.ReplaceAllString(value, "[附件链接]"), nil
}

type dingResponseGuard struct {
	base   http.RoundTripper
	denied atomic.Bool
}

func (g *dingResponseGuard) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := g.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 401, 403, 404, 410:
		g.denied.Store(true)
	}
	if response.ContentLength > responseLimit {
		response.Body.Close()
		return nil, errInvalid
	}
	response.Body = struct {
		io.Reader
		io.Closer
	}{io.LimitReader(response.Body, responseLimit+1), response.Body}
	return response, nil
}

func (g *dingResponseGuard) failure() error {
	if g.denied.Load() {
		return errDenied
	}
	return errUnavailable
}

func dingContent(raw []byte) (string, bool) {
	if len(raw) > responseLimit {
		return "", false
	}
	type document struct {
		Content  *string
		Markdown *string
	}
	var envelope struct {
		document
		Data    document
		Result  document
		Success *bool
	}
	if json.Unmarshal(raw, &envelope) != nil || (envelope.Success != nil && !*envelope.Success) {
		return "", false
	}
	for _, doc := range []document{envelope.document, envelope.Data, envelope.Result} {
		for _, text := range []*string{doc.Markdown, doc.Content} {
			if text != nil {
				return *text, true
			}
		}
	}
	return "", false
}
