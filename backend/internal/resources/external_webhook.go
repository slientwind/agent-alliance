package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type WebhookRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type WebhookSpec struct {
	Submit       WebhookRoute      `json:"submit"`
	Poll         WebhookRoute      `json:"poll"`
	Cancel       *WebhookRoute     `json:"cancel,omitempty"`
	InputField   string            `json:"inputField"`
	TaskField    string            `json:"taskField"`
	TaskPath     string            `json:"taskPath"`
	StatePath    string            `json:"statePath"`
	ResultPath   string            `json:"resultPath"`
	Auth         string            `json:"auth"`
	StateMapping map[string]string `json:"stateMapping,omitempty"`
}

var (
	routeSegment = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	remoteTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,255}$`)
	inputKey     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,59}$`)
)

func (s *WebhookSpec) defaults() {
	for name, fallback := range map[*string]string{
		&s.InputField: "input", &s.TaskField: "task_id", &s.TaskPath: "$.task_id", &s.StatePath: "$.status",
		&s.ResultPath: "$.output", &s.Auth: "bearer", &s.Submit.Method: http.MethodPost, &s.Poll.Method: http.MethodGet,
	} {
		if *name == "" {
			*name = fallback
		}
	}
	if s.Cancel != nil && s.Cancel.Method == "" {
		s.Cancel.Method = http.MethodPost
	}
}

func (spec AdapterSpec) sameWebhookRoute(saved []byte) bool {
	if spec.Webhook == nil {
		return len(saved) == 0
	}
	var old WebhookSpec
	return json.Unmarshal(saved, &old) == nil && spec.Webhook.Auth == old.Auth && spec.Webhook.Submit == old.Submit &&
		spec.Webhook.Poll == old.Poll && (spec.Webhook.Cancel == nil && old.Cancel == nil ||
		spec.Webhook.Cancel != nil && old.Cancel != nil && *spec.Webhook.Cancel == *old.Cancel)
}

func (s WebhookSpec) valid() bool {
	if !s.Submit.valid(false, http.MethodPost) || !s.Poll.valid(true, http.MethodGet, http.MethodPost) ||
		(s.Cancel != nil && !s.Cancel.valid(true, http.MethodPost, http.MethodDelete)) ||
		!inputKey.MatchString(s.InputField) || !inputKey.MatchString(s.TaskField) || len(s.StateMapping) > 24 {
		return false
	}
	switch s.Auth {
	case "bearer", "api_key", "x-api-key":
	default:
		return false
	}
	for _, expression := range []string{s.TaskPath, s.StatePath, s.ResultPath} {
		if _, ok := compileJSONPath(expression); !ok {
			return false
		}
	}
	for remote, state := range s.StateMapping {
		if strings.TrimSpace(remote) == "" || len(remote) > 100 || normalizeWebhookState(state) != state {
			return false
		}
	}
	return true
}

func (r WebhookRoute) valid(task bool, methods ...string) bool {
	allowed := false
	for _, method := range methods {
		allowed = allowed || r.Method == method
	}
	if !allowed || !strings.HasPrefix(r.Path, "/") || len(r.Path) > 512 {
		return false
	}
	count := 0
	for _, segment := range strings.Split(r.Path[1:], "/") {
		if segment == "{task}" && task {
			count++
			continue
		}
		if segment == "." || segment == ".." || !routeSegment.MatchString(segment) {
			return false
		}
	}
	return count <= 1
}

func normalizeWebhookState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "queued", "pending", "accepted", "submitted":
		return "submitted"
	case "running", "active", "in_progress", "processing", "working":
		return "working"
	case "success", "succeeded", "done", "complete", "completed":
		return "completed"
	case "failed", "failure", "error":
		return "failed"
	case "cancelled", "canceled", "stopped":
		return "canceled"
	case "rejected", "auth-required", "input-required":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "unknown"
	}
}

func (m Model) webhookStart(ctx context.Context, messages []Message) (Reply, error) {
	if !m.Adapter.valid() {
		return Reply{}, errExternalReply
	}
	brief, err := externalBrief(messages)
	if err != nil {
		return Reply{}, err
	}
	if m.Adapter.Template == "manus" {
		return m.manusStart(ctx, brief)
	}
	spec := m.Adapter.Webhook
	raw, _, err := m.webhookRequest(ctx, spec.Submit, spec.Auth, spec.TaskField, "",
		map[string]any{spec.InputField: brief})
	if err != nil {
		return Reply{}, err
	}
	return spec.reply(raw, "", false)
}

func (m Model) webhookFollow(ctx context.Context, receipt RemoteWait, cancel bool) (Reply, error) {
	if !m.Adapter.valid() || !remoteTaskID.MatchString(receipt.Task) {
		return Reply{}, errExternalReply
	}
	if m.Adapter.Template == "manus" {
		return m.manusFollow(ctx, receipt, cancel)
	}
	spec := m.Adapter.Webhook
	route := spec.Poll
	if cancel {
		if spec.Cancel == nil {
			return Reply{}, errors.New("该外部服务未配置取消请求")
		}
		route = *spec.Cancel
	}
	raw, status, err := m.webhookRequest(ctx, route, spec.Auth, spec.TaskField, receipt.Task, nil)
	if err != nil {
		return Reply{}, err
	}
	if cancel && status == http.StatusNoContent {
		return Reply{}, &RemoteWait{Task: receipt.Task, State: "canceled"}
	}
	return spec.reply(raw, receipt.Task, cancel)
}

func (m Model) webhookRequest(ctx context.Context, route WebhookRoute, auth, taskField, task string, payload map[string]any) (json.RawMessage, int, error) {
	if !Endpoint(m.Endpoint) {
		return nil, 0, errExternalReply
	}
	target, _ := url.Parse(m.Endpoint)
	target.Path = strings.TrimRight(target.Path, "/") + strings.ReplaceAll(route.Path, "{task}", task)
	target.RawPath = ""
	if task != "" && !strings.Contains(route.Path, "{task}") {
		if route.Method == http.MethodGet || route.Method == http.MethodDelete {
			query := url.Values{}
			query.Set(taskField, task)
			if m.Adapter.Template == "manus" {
				query.Set("order", "desc")
				query.Set("limit", "50")
			}
			target.RawQuery = query.Encode()
		} else {
			payload = map[string]any{taskField: task}
		}
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, errExternalReply
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, route.Method, target.String(), body)
	if err != nil {
		return nil, 0, errExternalReply
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	m.ExternalTrace.inject(req)
	if m.Key != "" {
		switch auth {
		case "api_key":
			req.Header.Set("API_KEY", m.Key)
		case "x-api-key":
			req.Header.Set("X-Api-Key", m.Key)
		case "x-manus-api-key":
			req.Header.Set("X-Manus-Api-Key", m.Key)
		default:
			req.Header.Set("Authorization", "Bearer "+m.Key)
		}
	}
	response, err := m.externalResponse(req)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status < 200 || status >= 300 {
		return nil, status, fmt.Errorf("外部服务 HTTP %d", status)
	}
	var wire json.RawMessage
	if status != http.StatusNoContent && decodeAdapterJSON(response.Body, &wire) != nil {
		return nil, status, errExternalReply
	}
	return wire, status, nil
}

func (s WebhookSpec) reply(raw json.RawMessage, expected string, cancel bool) (Reply, error) {
	if !json.Valid(raw) {
		return Reply{}, errExternalReply
	}
	task := pathString(raw, s.TaskPath)
	if value := readJSONPath(raw, s.TaskPath); task == "" && len(value) > 0 && value[0] >= '0' && value[0] <= '9' {
		task = string(value)
	}
	if task != "" && (!remoteTaskID.MatchString(task) || expected != "" && expected != task) {
		return Reply{}, errors.New("远端回执与原任务不符")
	}
	if task == "" {
		task = expected
	}
	text := pathString(raw, s.ResultPath)
	if value := readJSONPath(raw, s.ResultPath); text == "" && len(value) > 0 && (value[0] == '{' || value[0] == '[') {
		var compact bytes.Buffer
		if json.Compact(&compact, value) == nil {
			text = compact.String()
		}
	}
	remote := pathString(raw, s.StatePath)
	state := normalizeWebhookState(remote)
	if mapped := s.StateMapping[remote]; mapped != "" {
		state = mapped
	}
	if remote == "" && state == "unknown" {
		switch {
		case cancel:
			state = "working"
		case text != "":
			state = "completed"
		case expected == "":
			state = "submitted"
		default:
			state = "working"
		}
	}
	return webhookReply(task, state, text)
}

func webhookReply(task, state, text string) (Reply, error) {
	if state == "completed" && validExternalText(text) {
		reply := Reply{Text: text, FinishReason: "stop"}
		if task != "" {
			reply.Receipt = &RemoteWait{Task: task, State: state}
		}
		return reply, nil
	}
	if task != "" {
		return Reply{}, &RemoteWait{Task: task, State: state, NoText: state == "completed"}
	}
	return Reply{}, errExternalReply
}

func (m Model) manusStart(ctx context.Context, brief string) (Reply, error) {
	raw, _, err := m.webhookRequest(ctx, WebhookRoute{Method: http.MethodPost, Path: "/v2/task.create"},
		"x-manus-api-key", "", "", map[string]any{"message": map[string]string{"content": brief}, "share_visibility": "private"})
	if err != nil {
		return Reply{}, err
	}
	if string(readJSONPath(raw, "$.ok")) != "true" {
		return Reply{}, errExternalReply
	}
	task := pathString(raw, "$.task_id")
	if !remoteTaskID.MatchString(task) {
		return Reply{}, errExternalReply
	}
	return webhookReply(task, "submitted", "")
}

func (m Model) manusFollow(ctx context.Context, receipt RemoteWait, cancel bool) (Reply, error) {
	route := WebhookRoute{Method: http.MethodGet, Path: "/v2/task.listMessages"}
	if cancel {
		route = WebhookRoute{Method: http.MethodPost, Path: "/v2/task.stop"}
	}
	raw, _, err := m.webhookRequest(ctx, route, "x-manus-api-key", "task_id", receipt.Task, nil)
	if err != nil {
		return Reply{}, err
	}
	if string(readJSONPath(raw, "$.ok")) != "true" {
		return Reply{}, errExternalReply
	}
	if cancel {
		return webhookReply(receipt.Task, "canceled", "")
	}
	if pathString(raw, "$.task_id") != receipt.Task {
		return Reply{}, errors.New("远端回执与原任务不符")
	}
	var response struct {
		Messages []struct {
			Type   string
			Status struct {
				Agent string `json:"agent_status"`
			} `json:"status_update"`
			Message struct {
				Content  string
				Delivery string `json:"delivery_kind"`
			} `json:"assistant_message"`
		}
	}
	if json.Unmarshal(raw, &response) != nil {
		return Reply{}, errExternalReply
	}
	state, text := "", ""
	for _, item := range response.Messages {
		switch item.Type {
		case "status_update":
			if state == "" {
				state = item.Status.Agent
			}
		case "assistant_message":
			if text == "" && (item.Message.Delivery == "result" || item.Message.Delivery == "") && validExternalText(item.Message.Content) {
				text = item.Message.Content
			}
		}
	}
	switch state {
	case "stopped":
		return webhookReply(receipt.Task, "completed", text)
	case "error":
		return webhookReply(receipt.Task, "failed", "")
	case "waiting":
		return webhookReply(receipt.Task, "input-required", "")
	default:
		return webhookReply(receipt.Task, "working", "")
	}
}
