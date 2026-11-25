package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"

	"alliance/internal/web"
)

type a2aProtocol struct{ Model }

type part struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}
type a2aResult struct {
	Kind      string
	ID        string
	ContextID string
	Parts     []part
	Usage     json.RawMessage
	Metadata  json.RawMessage
	Status    struct {
		State   string
		Message struct {
			MessageID string
			Parts     []part
		}
	}
	Artifacts []struct{ Parts []part }
}

func (m a2aProtocol) submit(ctx context.Context, messages []Message, _ func(string)) (Reply, error) {
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
	return m.remote(ctx, "message/send", nil, map[string]any{
		"message":       map[string]any{"kind": "message", "messageId": id, "role": "user", "parts": parts},
		"configuration": map[string]any{"blocking": true, "acceptedOutputModes": []string{"text/plain"}},
	})
}

func (m a2aProtocol) supplement(ctx context.Context, receipt RemoteWait, messageID, input string) (Reply, error) {
	return m.remote(ctx, "message/send", &receipt, map[string]any{
		"message": map[string]any{"kind": "message", "messageId": messageID, "role": "user",
			"taskId": receipt.Task, "contextId": receipt.Context, "parts": []part{{Kind: "text", Text: input}}},
		"configuration": map[string]any{"blocking": true, "acceptedOutputModes": []string{"text/plain"}},
	})
}
func (m a2aProtocol) follow(ctx context.Context, receipt RemoteWait, cancel bool) (Reply, error) {
	method := "tasks/get"
	params := map[string]any{"id": receipt.Task, "historyLength": 0}
	if cancel {
		method = "tasks/cancel"
		delete(params, "historyLength")
	}
	return m.remote(ctx, method, &receipt, params)
}

func (m a2aProtocol) remote(ctx context.Context, method string, expected *RemoteWait, params any) (reply Reply, err error) {
	if !Endpoint(m.Endpoint) {
		return Reply{}, errors.New("外部主机未获准")
	}
	rpcID := web.Token()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpcID, "method": method, "params": params})
	if err != nil {
		return Reply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", m.Endpoint, bytes.NewReader(raw))
	if err != nil {
		return Reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	m.ExternalTrace.inject(req)
	if m.Key != "" {
		req.Header.Set("Authorization", "Bearer "+m.Key)
	}
	rsp, err := m.externalResponse(req)
	if err != nil {
		return Reply{}, err
	}
	defer rsp.Body.Close()
	var usage *ExternalUsage
	defer func() {
		if !errors.Is(err, errRemoteTaskMismatch) {
			reply.ExternalMetadata = externalMetadata(rsp.Header, usage)
		}
	}()
	if rsp.StatusCode != 200 {
		return Reply{}, fmt.Errorf("外部服务 HTTP %d", rsp.StatusCode)
	}
	var envelope struct {
		JSONRPC, ID string
		Result      a2aResult
		Usage       json.RawMessage
		Error       json.RawMessage
	}
	if json.NewDecoder(io.LimitReader(rsp.Body, 4<<20)).Decode(&envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID != rpcID || len(envelope.Error) != 0 {
		return Reply{}, errors.New("无法确定外部调用结果")
	}
	result := envelope.Result
	if expected != nil && (result.Kind != "task" || result.ID != expected.Task || (expected.Context != "" && result.ContextID != expected.Context)) {
		return Reply{}, errRemoteTaskMismatch
	}
	for _, candidate := range []json.RawMessage{result.Usage, envelope.Usage, readJSONPath(result.Metadata, "$.usage")} {
		if usage = parseExternalUsage(candidate); usage != nil {
			break
		}
	}
	var receipt *RemoteWait
	parts := result.Parts
	if result.Kind == "task" {
		if result.ID == "" || len(result.ID) > 512 || len(result.ContextID) > 512 {
			return Reply{}, errors.New("外部任务缺少标识")
		}
		receipt = &RemoteWait{Task: result.ID, Context: result.ContextID, State: result.Status.State}
		if receipt.State == "input-required" {
			receipt.Question = remoteQuestion(result.Status.Message.Parts)
			message := result.Status.Message.MessageID
			if len(message) <= 256 && !strings.ContainsRune(message, 0) {
				receipt.QuestionMessage = message
			}
		}
		switch receipt.State {
		case "submitted", "working", "input-required", "auth-required", "canceled", "failed", "rejected", "unknown":
			return Reply{}, receipt
		case "completed":
		default:
			return Reply{}, errors.New("外部任务状态无效")
		}
		for _, a := range result.Artifacts {
			parts = append(parts, a.Parts...)
		}
		if len(parts) == 0 {
			parts = result.Status.Message.Parts
		}
	} else if result.Kind != "message" {
		return Reply{}, errors.New("不支持的外部响应类型")
	}
	var text []string
	for _, p := range parts {
		if p.Kind == "text" && p.Text != "" {
			text = append(text, p.Text)
		}
	}
	if len(text) == 0 {
		if receipt != nil {
			receipt.NoText = true
			return Reply{}, receipt
		}
		return Reply{}, errors.New("返回成果不包含已声明支持的文本操作")
	}
	return Reply{Text: strings.Join(text, "\n"), Receipt: receipt}, nil
}

func remoteQuestion(parts []part) string {
	const limit = 1000
	var question []rune
	for _, p := range parts {
		if p.Kind != "text" || web.Blank(p.Text) {
			continue
		}
		if len(question) != 0 {
			question = append(question, '\n')
		}
		for _, ch := range strings.TrimSpace(p.Text) {
			if !unicode.IsControl(ch) || ch == '\n' || ch == '\t' {
				question = append(question, ch)
			}
			if len(question) >= limit {
				return strings.TrimSpace(string(question[:limit]))
			}
		}
	}
	return strings.TrimSpace(string(question))
}
