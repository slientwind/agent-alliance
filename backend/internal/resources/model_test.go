package resources

import (
	"alliance/internal/knowledge"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTextRequestIncludesOptInToolsAndPairedMessages(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	model := Model{Name: "model", MaxTokens: 2048, Tools: []ToolDefinition{{Type: "function", Function: ToolSchema{Name: "lookup", Parameters: schema}}}}
	calls := []ToolCall{{ID: "call1", Type: "function", Function: ToolFunction{Name: "lookup", Arguments: `{"query":"invoice"}`}}}
	messages := []Message{
		{Role: "user", Content: "Find an invoice"},
		{Role: "assistant", ToolCalls: calls, ReasoningContent: "reasoning"},
		{Role: "tool", ToolCallID: "call1", Content: `{"found":true}`},
	}
	for _, vision := range []bool{false, true} {
		if vision {
			model.OutputOperation = VisionOperation
			model.Pictures = []knowledge.Picture{{Media: "image/png", Data: []byte{1}, Name: "document", Version: 1}}
		}
		body, err := model.textRequestBody(messages, true)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			MaxTokens int               `json:"max_tokens"`
			Messages  []json.RawMessage `json:"messages"`
			Tools     []ToolDefinition  `json:"tools"`
			Stream    bool              `json:"stream"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if !request.Stream || len(request.Messages) != 3 || request.MaxTokens != 2048 || len(request.Tools) != 1 || string(request.Tools[0].Function.Parameters) != string(schema) {
			t.Fatalf("unexpected request %s", body)
		}
		var assistant, result Message
		if err := json.Unmarshal(request.Messages[1], &assistant); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(request.Messages[2], &result); err != nil {
			t.Fatal(err)
		}
		if assistant.ReasoningContent != "reasoning" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0] != calls[0] || !reflect.DeepEqual(result, messages[2]) {
			t.Fatalf("tool pair changed: %s", body)
		}
		if vision && !strings.Contains(string(request.Messages[0]), "data:image/png;base64,AQ==") {
			t.Fatalf("vision request missing the image: %s", body)
		}
	}
}

func TestLegacyTextRequestOmitsUnconfiguredSettings(t *testing.T) {
	body, err := (Model{Name: "model"}).textRequestBody([]Message{{Role: "user", Content: "hello"}}, false)
	if err != nil || string(body) != `{"messages":[{"role":"user","content":"hello"}],"model":"model"}` {
		t.Fatalf("body=%s err=%v", body, err)
	}
}

func TestDecodeTextReplyRecognizesToolOnlyResponseAndUsage(t *testing.T) {
	const wire = `{"choices":[{"message":{"role":"assistant","content":null,"reasoning_content":"checking","tool_calls":[{"id":"call1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":12,"prompt_tokens":8,"completion_tokens":4,"prompt_cache_hit_tokens":6,"prompt_tokens_details":{"cached_tokens":5}}}`
	reply, err := decodeTextReply(strings.NewReader(wire), true)
	if err != nil || reply.Text != "" || reply.ReasoningContent != "checking" || len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Function.Name != "lookup" {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
	if reply.Tokens != 12 || reply.PromptTokens != 8 || reply.CompletionTokens != 4 || reply.CachedTokens != 5 {
		t.Fatalf("incorrect provider usage %+v", reply)
	}
	if _, err := decodeTextReply(strings.NewReader(wire), false); err == nil {
		t.Fatal("accepted unrequested tool calls")
	}
	if _, err := decodeTextReply(strings.NewReader(strings.Replace(wire, `"id":"call1"`, `"id":""`, 1)), true); err == nil {
		t.Fatal("accepted an unidentifiable tool call")
	}
}
