package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/web"
)

const AdapterOpenAICompat = "openai_compat"
const AdapterWebhook = "async_webhook"

type AdapterSpec struct {
	Protocol string       `json:"protocol"`
	Template string       `json:"template"`
	Model    string       `json:"model,omitempty"`
	Stream   bool         `json:"stream"`
	Webhook  *WebhookSpec `json:"webhook,omitempty"`
}

type adapterTemplate struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Route     string `json:"route"`
	Model     string `json:"model,omitempty"`
	Streaming bool   `json:"requiresStreaming"`
	Protocol  string `json:"protocol,omitempty"`
}

var chatAdapterTemplates = []adapterTemplate{
	{ID: "openai", Name: "OpenAI Compatible", Route: "/v1/chat/completions", Model: "模型名称"},
	{ID: "dify", Name: "Dify", Route: "/v1/chat-messages"},
	{ID: "coze", Name: "Coze", Route: "/v3/chat", Model: "Bot ID", Streaming: true},
	{ID: "fastgpt", Name: "FastGPT", Route: "/api/v1/chat/completions", Model: "App ID"},
	{ID: "langgraph", Name: "LangGraph", Route: "/runs/stream", Model: "Assistant ID", Streaming: true},
	{ID: "webhook", Name: "异步 Webhook", Protocol: AdapterWebhook},
	{ID: "manus", Name: "Manus", Route: "/v2/task.create", Protocol: AdapterWebhook},
}

func (c Catalog) adapterTemplates(w http.ResponseWriter, _ *http.Request, _ identity.Member) error {
	return web.JSON(w, http.StatusOK, chatAdapterTemplates)
}

func (spec AdapterSpec) valid() bool {
	if spec.Protocol == AdapterWebhook {
		return !spec.Stream && spec.Model == "" && (spec.Template == "manus" && spec.Webhook == nil ||
			spec.Template == "webhook" && spec.Webhook != nil && spec.Webhook.valid())
	}
	if spec.Protocol != AdapterOpenAICompat || spec.Webhook != nil || len(spec.Model) > 256 || strings.ContainsRune(spec.Model, 0) {
		return false
	}
	for _, template := range chatAdapterTemplates {
		if template.ID == spec.Template && template.Protocol == "" {
			return (!template.Streaming || spec.Stream) && (template.Model == "" || spec.Model != "")
		}
	}
	return false
}

func (c Catalog) saveAdapter(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		externalDraft
		Name, Description string
		Adapter           AdapterSpec
	}
	in.Policy = defaultRemotePolicy()
	if err := web.ReadLimited(w, r, &in, 32<<10); err != nil {
		return err
	}
	in.Name, in.Description = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description)
	in.Adapter.Model = strings.TrimSpace(in.Adapter.Model)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if in.Adapter.Webhook != nil {
		in.Adapter.Webhook.defaults()
	}
	if !in.Adapter.valid() || !in.Policy.valid() || in.Expected < 0 || in.Name == "" || utf8.RuneCountInString(in.Name) > 120 ||
		len(in.Description) > 4096 || strings.ContainsRune(in.Name+in.Description, 0) || len(in.Key) > 4096 ||
		strings.ContainsFunc(in.Key, func(c rune) bool { return c <= ' ' || c >= 127 }) {
		return web.Fail(422, "请核对 Agent 名称、连接方式和服务配置")
	}
	if !c.PermittedAgentEndpoint(r.Context(), in.Endpoint) {
		return web.Fail(422, "请使用企业已授权的 HTTPS 服务地址")
	}
	card := AgentCard{Name: in.Name, Description: in.Description, Version: "1", Protocol: in.Adapter.Protocol,
		URL: in.Endpoint, Transport: "HTTP", Adapter: &in.Adapter}
	data, err := json.Marshal(card)
	if err != nil {
		return err
	}
	in.Card = data
	return c.saveExternal(w, r, u, in.externalDraft, card, in.Endpoint)
}

var errExternalReply = errors.New("外部 Agent 未返回完整答复")
var errRemoteTaskMismatch = errors.New("远端回执与原任务不符")

func (m Model) generateExternal(ctx context.Context, messages []Message, emit func(string)) (Reply, error) {
	sub, cancel := context.WithTimeout(ctx, time.Duration(m.Policy.ExecutionSeconds)*time.Second)
	defer cancel()
	if m.Adapter != nil {
		switch m.Adapter.Protocol {
		case AdapterOpenAICompat:
			return m.chatAdapter(sub, messages, emit)
		case AdapterWebhook:
			return m.webhookStart(sub, messages)
		default:
			return Reply{}, errExternalReply
		}
	}
	var parts []part
	for _, msg := range messages {
		if msg.Role == "user" {
			parts = append(parts, part{Kind: "text", Text: msg.Content})
		}
	}
	id := m.Operation
	if id == "" {
		id = web.Token()
	}
	return m.remote(sub, "message/send", nil, map[string]any{
		"message":       map[string]any{"kind": "message", "messageId": id, "role": "user", "parts": parts},
		"configuration": map[string]any{"blocking": true, "acceptedOutputModes": []string{"text/plain"}},
	})
}

func (m Model) chatAdapter(ctx context.Context, messages []Message, emit func(string)) (Reply, error) {
	spec := m.Adapter
	if !spec.valid() || !Endpoint(m.Endpoint) {
		return Reply{}, errExternalReply
	}
	brief, err := externalBrief(messages)
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.Endpoint, nil)
	if err != nil {
		return Reply{}, errExternalReply
	}
	m.ExternalTrace.inject(req)
	wire := spec.request(brief, req.Header.Get("X-Alliance-User-Id"), req.Header.Get("X-Alliance-Run-Id"))
	raw, err := json.Marshal(wire)
	if err != nil {
		return Reply{}, errExternalReply
	}
	req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
	req.Header.Set("Content-Type", "application/json")
	if spec.Stream {
		req.Header.Set("Accept", "text/event-stream, application/json")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if m.Key != "" {
		if spec.Template == "langgraph" {
			req.Header.Set("X-Api-Key", m.Key)
		} else {
			req.Header.Set("Authorization", "Bearer "+m.Key)
		}
	}
	response, err := m.externalResponse(req)
	if err != nil {
		return Reply{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Reply{ExternalMetadata: externalMetadata(response.Header, nil)}, fmt.Errorf("外部服务 HTTP %d", response.StatusCode)
	}
	reply, err := spec.decode(response.Header.Get("Content-Type"), response.Body, emit)
	usage := reply.wireUsage
	if spec.Template == "fastgpt" {
		// The compatibility response's top-level usage contains placeholder counts.
		usage = nil
	}
	reply.ExternalMetadata = externalMetadata(response.Header, usage)
	return reply, err
}

func externalBrief(messages []Message) (string, error) {
	var parts []string
	for _, message := range messages {
		if message.Role == "user" && strings.TrimSpace(message.Content) != "" {
			parts = append(parts, message.Content)
		}
	}
	brief := strings.Join(parts, "\n\n")
	if brief == "" || len(brief) > 256<<10 {
		return "", errExternalReply
	}
	return brief, nil
}

func (m Model) externalResponse(req *http.Request) (*http.Response, error) {
	base, err := providerClient()
	if err != nil {
		return nil, errExternalReply
	}
	client, err := m.outbound.Client(req.Context(), req.URL.String(), false, base)
	if err != nil {
		return nil, errors.New("外部主机未获准或无法核对连接地址")
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("外部提交结果待核对")
	}
	return response, nil
}

func (spec AdapterSpec) request(brief, actor, run string) map[string]any {
	if actor == "" {
		actor = run
	}
	messages := []Message{{Role: "user", Content: brief}}
	switch spec.Template {
	case "dify":
		mode := "blocking"
		if spec.Stream {
			mode = "streaming"
		}
		return map[string]any{"inputs": map[string]any{}, "query": brief, "response_mode": mode, "user": actor, "auto_generate_name": false}
	case "coze":
		return map[string]any{"bot_id": spec.Model, "user_id": actor, "stream": true, "auto_save_history": false,
			"additional_messages": []map[string]string{{"role": "user", "content": brief, "content_type": "text"}}}
	case "fastgpt":
		return map[string]any{"appId": spec.Model, "messages": messages, "stream": spec.Stream, "detail": false}
	case "langgraph":
		return map[string]any{"assistant_id": spec.Model, "input": map[string]any{"messages": messages}, "stream_mode": "values"}
	default:
		return map[string]any{"model": spec.Model, "messages": messages, "stream": spec.Stream}
	}
}

func (spec AdapterSpec) decode(contentType string, body io.Reader, emit func(string)) (Reply, error) {
	kind, _, _ := mime.ParseMediaType(contentType)
	switch spec.Template {
	case "openai", "fastgpt":
		reply, err := DecodeStreamingChatReply(contentType, body, emit, false)
		if err != nil || !validExternalText(reply.Text) {
			return Reply{}, errExternalReply
		}
		return reply, nil
	case "dify":
		if kind == "text/event-stream" {
			return decodeDify(body, emit)
		}
		var result struct {
			Answer   string
			Metadata json.RawMessage
		}
		if err := decodeAdapterJSON(body, &result); err != nil || !validExternalText(result.Answer) {
			return Reply{}, errExternalReply
		}
		return Reply{Text: result.Answer, FinishReason: "stop", wireUsage: parseExternalUsage(readJSONPath(result.Metadata, "$.usage"))}, nil
	case "coze":
		if kind != "text/event-stream" {
			return Reply{}, errExternalReply
		}
		return decodeCoze(body, emit)
	case "langgraph":
		if kind != "text/event-stream" {
			return Reply{}, errExternalReply
		}
		return decodeLangGraph(body, emit)
	default:
		return Reply{}, errExternalReply
	}
}

func validExternalText(value string) bool {
	return len(value) <= TextStreamLimit && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && strings.TrimSpace(value) != ""
}

func decodeAdapterJSON(body io.Reader, to any) error {
	data, err := io.ReadAll(io.LimitReader(body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 || json.Unmarshal(data, to) != nil {
		return errExternalReply
	}
	return nil
}
