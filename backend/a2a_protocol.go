package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go-v1/a2a"
	"github.com/a2aproject/a2a-go-v1/a2acompat/a2av0"
	"github.com/a2aproject/a2a-go-v1/a2asrv"
)

type A2AInfo struct {
	MessageID   string         `json:"messageId"`
	RequestHash string         `json:"requestHash"`
	PriorRunID  string         `json:"priorRunId,omitempty"`
	TaskID      a2a.TaskID     `json:"taskId"`
	ContextID   string         `json:"contextId"`
	History     []*a2a.Message `json:"history"`
	State       a2a.TaskState  `json:"state,omitempty"`
}
type a2aSubmissionKey struct{}
type a2aEndpoint struct {
	server             *Server
	kind, target, base string
	principal          Principal
}

var _ a2asrv.RequestHandler = (*a2aEndpoint)(nil)

func (h *a2aEndpoint) authorize(tenant string, write bool) error {
	if tenant != "" && tenant != h.principal.TenantID && tenant != h.kind+"/"+h.target {
		return a2a.ErrUnauthorized
	}
	if write && h.principal.Role == "viewer" {
		return a2a.ErrUnauthorized
	}
	return nil
}
func (h *a2aEndpoint) getRun(ctx context.Context, taskID a2a.TaskID) (Run, error) {
	st, e := h.server.store.readState(ctx)
	if e != nil {
		return Run{}, a2a.ErrInternalError
	}
	var latest Run
	for _, r := range st.Runs {
		if r.A2A != nil && r.A2A.TaskID == taskID && r.TargetID == h.target && r.TargetType == strings.TrimSuffix(h.kind, "s") && (latest.ID == "" || r.StartedAt.After(latest.StartedAt)) {
			latest = r
		}
	}
	if latest.ID == "" {
		return Run{}, a2a.ErrTaskNotFound
	}
	return latest, nil
}
func taskFromRun(r Run, history *int) *a2a.Task {
	state := a2a.TaskStateSubmitted
	switch r.Status {
	case "running":
		state = a2a.TaskStateWorking
	case "succeeded":
		state = a2a.TaskStateCompleted
	case "failed":
		state = a2a.TaskStateFailed
	case "cancelled":
		state = a2a.TaskStateCanceled
	}
	if r.Status == "succeeded" && r.A2A.State != "" {
		state = r.A2A.State
	}
	timestamp := r.StartedAt
	if r.EndedAt != nil {
		timestamp = *r.EndedAt
	}
	t := &a2a.Task{ID: r.A2A.TaskID, ContextID: r.A2A.ContextID, Status: a2a.TaskStatus{State: state, Timestamp: &timestamp}, History: append([]*a2a.Message{}, r.A2A.History...), Metadata: map[string]any{"runId": r.ID, "traceId": r.TraceID}}
	if terminal(r.Status) {
		text := r.Output
		if r.Error != "" {
			text = r.Error
		}
		msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, t, a2a.NewTextPart(text))
		msg.ID = "result_" + r.ID
		t.Status.Message = msg
		t.History = append(t.History, msg)
		if state == a2a.TaskStateCompleted {
			t.Artifacts = []*a2a.Artifact{{ID: a2a.ArtifactID("artifact_" + r.ID), Name: "result", Parts: a2a.ContentParts{a2a.NewTextPart(r.Output)}}}
		}
	}
	if history != nil {
		n := max(0, *history)
		if len(t.History) > n {
			t.History = t.History[len(t.History)-n:]
		}
	}
	return t
}
func (h *a2aEndpoint) GetTask(ctx context.Context, q *a2a.GetTaskRequest) (*a2a.Task, error) {
	if e := h.authorize(q.Tenant, false); e != nil {
		return nil, e
	}
	if q.HistoryLength != nil && *q.HistoryLength < 0 {
		return nil, a2a.ErrInvalidParams
	}
	r, e := h.getRun(ctx, q.ID)
	if e != nil {
		return nil, e
	}
	return taskFromRun(r, q.HistoryLength), nil
}
func (h *a2aEndpoint) ListTasks(ctx context.Context, q *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if e := h.authorize(q.Tenant, false); e != nil {
		return nil, e
	}
	if q.PageSize < 0 || q.PageSize > 100 || (q.HistoryLength != nil && *q.HistoryLength < 0) {
		return nil, a2a.ErrInvalidParams
	}
	size := q.PageSize
	if size == 0 {
		size = 50
	}
	offset := 0
	if q.PageToken != "" {
		b, e := base64.RawURLEncoding.DecodeString(q.PageToken)
		if e != nil {
			return nil, a2a.ErrInvalidParams
		}
		var n int
		n, e = strconv.Atoi(string(b))
		if e != nil || n < 0 {
			return nil, a2a.ErrInvalidParams
		}
		offset = n
	}
	st, e := h.server.store.readState(ctx)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	latest := map[a2a.TaskID]Run{}
	for _, r := range st.Runs {
		if r.A2A != nil && r.TargetID == h.target && r.TargetType == strings.TrimSuffix(h.kind, "s") {
			old, ok := latest[r.A2A.TaskID]
			if !ok || r.StartedAt.After(old.StartedAt) {
				latest[r.A2A.TaskID] = r
			}
		}
	}
	tasks := []*a2a.Task{}
	for _, r := range latest {
		t := taskFromRun(r, q.HistoryLength)
		if q.ContextID != "" && t.ContextID != q.ContextID || q.Status != "" && t.Status.State != q.Status {
			continue
		}
		if q.StatusTimestampAfter != nil && !t.Status.Timestamp.After(*q.StatusTimestampAfter) {
			continue
		}
		if !q.IncludeArtifacts {
			t.Artifacts = nil
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].Status.Timestamp.Equal(*tasks[j].Status.Timestamp) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].Status.Timestamp.After(*tasks[j].Status.Timestamp)
	})
	total := len(tasks)
	offset = min(offset, total)
	end := min(offset+size, total)
	out := &a2a.ListTasksResponse{Tasks: tasks[offset:end], TotalSize: total, PageSize: size}
	if end < total {
		out.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(end)))
	}
	return out, nil
}
func (h *a2aEndpoint) CancelTask(ctx context.Context, q *a2a.CancelTaskRequest) (*a2a.Task, error) {
	if e := h.authorize(q.Tenant, true); e != nil {
		return nil, e
	}
	r, e := h.getRun(ctx, q.ID)
	if e != nil {
		return nil, e
	}
	if r.Status == "cancelled" {
		return taskFromRun(r, nil), nil
	}
	if terminal(r.Status) {
		return nil, a2a.ErrTaskNotCancelable
	}
	if e = h.server.cancelDurable(ctx, r.ID); e != nil {
		return nil, a2a.ErrTaskNotCancelable
	}
	return h.GetTask(ctx, &a2a.GetTaskRequest{ID: q.ID})
}
func messageText(m *a2a.Message) (string, error) {
	parts := []string{}
	for _, p := range m.Parts {
		if p == nil {
			return "", a2a.ErrInvalidParams
		}
		switch v := p.Content.(type) {
		case a2a.Text:
			parts = append(parts, string(v))
		case a2a.Data:
			b, e := json.Marshal(v.Value)
			if e != nil {
				return "", a2a.ErrInvalidParams
			}
			parts = append(parts, string(b))
		default:
			return "", a2a.ErrUnsupportedContentType
		}
	}
	return strings.Join(parts, "\n"), nil
}
func (h *a2aEndpoint) SendMessage(ctx context.Context, q *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	if e := h.authorize(q.Tenant, true); e != nil {
		return nil, e
	}
	if q.Message == nil || q.Message.ID == "" || len(q.Message.ID) > 128 || q.Message.Role != a2a.MessageRoleUser {
		return nil, a2a.ErrInvalidParams
	}
	if q.Config != nil {
		if q.Config.HistoryLength != nil && *q.Config.HistoryLength < 0 {
			return nil, a2a.ErrInvalidParams
		}
		if len(q.Config.AcceptedOutputModes) > 0 {
			ok := false
			for _, v := range q.Config.AcceptedOutputModes {
				if v == "text/plain" {
					ok = true
				}
			}
			if !ok {
				return nil, a2a.ErrUnsupportedContentType
			}
		}
	}
	messageBytes, _ := json.Marshal(q.Message)
	requestHash := hashToken(string(messageBytes))
	st, e := h.server.store.readState(ctx)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	for _, previous := range st.Runs {
		if previous.TargetID == h.target && previous.TargetType == strings.TrimSuffix(h.kind, "s") && previous.A2A != nil && previous.A2A.MessageID == q.Message.ID {
			if previous.A2A.RequestHash != requestHash {
				return nil, a2a.NewError(a2a.ErrInvalidParams, "messageId 已用于不同请求")
			}
			return h.awaitTask(ctx, previous, q.Config)
		}
	}
	input, e := messageText(q.Message)
	if e != nil {
		return nil, e
	}
	if !nonempty(input) || len(input) > 64000 {
		return nil, a2a.ErrInvalidParams
	}
	info := &A2AInfo{MessageID: q.Message.ID, RequestHash: requestHash, TaskID: a2a.NewTaskID(), ContextID: q.Message.ContextID, History: []*a2a.Message{q.Message}}
	if q.Message.TaskID != "" {
		old, e := h.getRun(ctx, q.Message.TaskID)
		if e != nil {
			return nil, e
		}
		task := taskFromRun(old, nil)
		if task.Status.State != a2a.TaskStateInputRequired && task.Status.State != a2a.TaskStateAuthRequired {
			return nil, a2a.NewError(a2a.ErrInvalidParams, "任务当前状态不接受后续消息")
		}
		if info.ContextID != "" && info.ContextID != task.ContextID {
			return nil, a2a.ErrInvalidParams
		}
		info.PriorRunID = old.ID
		info.TaskID = task.ID
		info.ContextID = task.ContextID
		info.History = append(task.History, q.Message)
	} else if info.ContextID != "" {
		list, e := h.ListTasks(ctx, &a2a.ListTasksRequest{ContextID: info.ContextID, PageSize: 1})
		if e != nil {
			return nil, e
		}
		if len(list.Tasks) == 0 {
			return nil, a2a.NewError(a2a.ErrInvalidParams, "contextId 不存在于此 Agent")
		}
		info.History = append(list.Tasks[0].History, q.Message)
	}
	if info.ContextID == "" {
		info.ContextID = a2a.NewContextID()
	}
	if len(info.History) > 100 {
		return nil, a2a.NewError(a2a.ErrInvalidParams, "会话最多 100 条消息")
	}
	if len(info.History) > 1 {
		messages := []string{}
		for _, m := range info.History {
			v, e := messageText(m)
			if e != nil {
				return nil, e
			}
			messages = append(messages, string(m.Role)+": "+v)
		}
		input = strings.Join(messages, "\n\n")
		if len(input) > 64000 {
			return nil, a2a.ErrInvalidParams
		}
	}
	body, _ := json.Marshal(map[string]string{"input": input})
	req := httptest.NewRequest("POST", "/api/v1/"+h.kind+"/"+h.target+"/invoke", bytes.NewReader(body))
	req = req.WithContext(context.WithValue(context.WithValue(ctx, principalKey{}, h.principal), a2aSubmissionKey{}, info))
	req.Header.Set("Idempotency-Key", "a2a:"+stableHex(h.kind+h.target+q.Message.ID, 16))
	_, result, e := h.server.invoke(httptest.NewRecorder(), req, h.kind, h.target)
	if e != nil {
		var ae apiError
		if errors.As(e, &ae) && ae.code == 404 {
			return nil, a2a.ErrTaskNotFound
		}
		return nil, a2a.NewError(a2a.ErrInvalidParams, e.Error())
	}
	run := result.(Run)
	if q.Config != nil && q.Config.PushConfig != nil {
		_, e = h.CreateTaskPushConfig(ctx, &a2a.CreateTaskPushConfigRequest{TaskID: run.A2A.TaskID, Config: *q.Config.PushConfig})
		if e != nil {
			return nil, e
		}
	}
	return h.awaitTask(ctx, run, q.Config)
}
func (h *a2aEndpoint) awaitTask(ctx context.Context, run Run, config *a2a.SendMessageConfig) (a2a.SendMessageResult, error) {
	blocking := config == nil || config.Blocking == nil || *config.Blocking
	if blocking {
		for !terminal(run.Status) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			var e error
			run, e = h.getRun(ctx, run.A2A.TaskID)
			if e != nil {
				return nil, e
			}
		}
	}
	var length *int
	if config != nil {
		length = config.HistoryLength
	}
	return taskFromRun(run, length), nil
}

func (h *a2aEndpoint) SubscribeToTask(ctx context.Context, q *a2a.SubscribeToTaskRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if e := h.authorize(q.Tenant, false); e != nil {
			yield(nil, e)
			return
		}
		last := ""
		for {
			r, e := h.getRun(ctx, q.ID)
			if e != nil {
				yield(nil, e)
				return
			}
			t := taskFromRun(r, nil)
			raw, _ := json.Marshal(t)
			if string(raw) != last {
				initial := last == ""
				last = string(raw)
				if initial {
					if !yield(t, nil) {
						return
					}
				} else {
					for _, artifact := range t.Artifacts {
						if !yield(&a2a.TaskArtifactUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Artifact: artifact, LastChunk: true}, nil) {
							return
						}
					}
					if !yield(&a2a.TaskStatusUpdateEvent{TaskID: t.ID, ContextID: t.ContextID, Status: t.Status}, nil) {
						return
					}
				}
				if terminal(r.Status) {
					return
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
	}
}
func (h *a2aEndpoint) SendStreamingMessage(ctx context.Context, q *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		copy := *q
		config := a2a.SendMessageConfig{}
		if q.Config != nil {
			config = *q.Config
		}
		no := false
		config.Blocking = &no
		copy.Config = &config
		result, e := h.SendMessage(ctx, &copy)
		if e != nil {
			yield(nil, e)
			return
		}
		task := result.(*a2a.Task)
		for event, e := range h.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: task.ID}) {
			if !yield(event, e) {
				return
			}
		}
	}
}
func (h *a2aEndpoint) GetExtendedAgentCard(ctx context.Context, q *a2a.GetExtendedAgentCardRequest) (*a2a.AgentCard, error) {
	if e := h.authorize(q.Tenant, false); e != nil {
		return nil, e
	}
	return h.card(ctx)
}
func (h *a2aEndpoint) card(ctx context.Context) (*a2a.AgentCard, error) {
	st, e := h.server.store.readState(ctx)
	if e != nil {
		return nil, a2a.ErrInternalError
	}
	name, description, version := "", "", 0
	if h.kind == "agents" {
		a, ok := st.Agents[h.target]
		if !ok {
			return nil, a2a.ErrTaskNotFound
		}
		name, description, version = a.Name, a.Description, a.Version
	} else {
		v, ok := st.Services[h.target]
		if !ok || v.Published == nil {
			return nil, a2a.ErrTaskNotFound
		}
		name, description, version = v.Name, v.Description, v.Published.Version
	}
	raw := map[string]any{"name": name, "description": description, "version": strconv.Itoa(version), "capabilities": map[string]bool{"streaming": true, "pushNotifications": true, "extendedAgentCard": true}, "defaultInputModes": []string{"text/plain", "application/json"}, "defaultOutputModes": []string{"text/plain"}, "skills": []any{map[string]any{"id": h.target, "name": name, "description": description, "tags": []string{"alliance"}}}, "securitySchemes": map[string]any{"workspaceKey": map[string]any{"httpAuthSecurityScheme": map[string]string{"scheme": "bearer"}}}, "securityRequirements": []any{map[string]any{"schemes": map[string]any{"workspaceKey": []string{}}}}, "supportedInterfaces": []any{map[string]string{"url": h.base + "/rpc", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}, map[string]string{"url": h.base + "/rest", "protocolBinding": "HTTP+JSON", "protocolVersion": "1.0"}, map[string]string{"url": h.base + "/rpc-0.3", "protocolBinding": "JSONRPC", "protocolVersion": "0.3"}}}
	b, _ := json.Marshal(raw)
	var card a2a.AgentCard
	if e = json.Unmarshal(b, &card); e != nil {
		return nil, e
	}
	if addr := os.Getenv("ALLIANCE_GRPC_PUBLIC_ADDR"); addr != "" {
		card.SupportedInterfaces = append(card.SupportedInterfaces, &a2a.AgentInterface{URL: addr, ProtocolBinding: "GRPC", ProtocolVersion: "1.0", Tenant: h.kind + "/" + h.target})
	}
	return &card, nil
}
func (g *Gateway) serveA2A(w http.ResponseWriter, r *http.Request, p Principal, s *Server) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/a2a/") {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/a2a/"), "/")
	if len(parts) == 3 && parts[0] == "remote" {
		g.serveRemoteA2A(w, r, p, s, parts[1], parts[2])
		return true
	}
	if len(parts) < 3 || (parts[0] != "agents" && parts[0] != "services") {
		replyError(w, missing())
		return true
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	prefix := "/api/v1/a2a/" + parts[0] + "/" + parts[1]
	h := &a2aEndpoint{s, parts[0], parts[1], scheme + "://" + r.Host + prefix, p}
	if _, e := h.card(r.Context()); e != nil {
		replyError(w, missing())
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
	switch parts[2] {
	case ".well-known":
		if r.Method != "GET" || len(parts) != 4 || parts[3] != "agent-card.json" {
			replyError(w, missing())
			break
		}
		card, _ := h.card(r.Context())
		if strings.HasPrefix(r.Header.Get("A2A-Version"), "0.3") {
			a2asrv.NewAgentCardHandler(a2av0.NewStaticAgentCardProducer(card)).ServeHTTP(w, r)
		} else {
			a2asrv.NewStaticAgentCardHandler(card).ServeHTTP(w, r)
		}
	case "rpc":
		a2asrv.NewJSONRPCHandler(protocolHandler(h, "1.0"), a2asrv.WithTransportKeepAlive(10*time.Second)).ServeHTTP(w, r)
	case "rpc-0.3":
		a2av0.NewJSONRPCHandler(protocolHandler(h, "0.3"), a2asrv.WithTransportKeepAlive(10*time.Second)).ServeHTTP(w, r)
	case "rest":
		ctx, _ := a2asrv.NewCallContext(r.Context(), a2asrv.NewServiceParams(r.Header))
		r = r.WithContext(ctx)
		http.StripPrefix(prefix+"/rest", a2asrv.NewRESTHandler(protocolHandler(h, "1.0"), a2asrv.WithTransportKeepAlive(10*time.Second))).ServeHTTP(w, r)
	default:
		replyError(w, missing())
	}
	return true
}

// Protocol-wide validation also applies to custom durable handlers, not only the SDK executor.
type protocolInterceptor struct {
	Expected string
	a2asrv.PassthroughCallInterceptor
}

func (i protocolInterceptor) Before(ctx context.Context, call *a2asrv.CallContext, req *a2asrv.Request) (context.Context, any, error) {
	version := "0.3"
	if versions, ok := call.ServiceParams().Get("A2A-Version"); ok && len(versions) > 0 {
		version = versions[0]
	}
	if version == "0.3.0" {
		version = "0.3"
	}
	if version == "1.0.0" {
		version = "1.0"
	}
	if version != i.Expected {
		return ctx, nil, a2a.ErrVersionNotSupported
	}

	return ctx, nil, nil
}
func protocolHandler(handler a2asrv.RequestHandler, expected string) a2asrv.RequestHandler {
	return &a2asrv.InterceptedHandler{Handler: handler, Interceptors: []a2asrv.CallInterceptor{protocolInterceptor{Expected: expected}}}
}
