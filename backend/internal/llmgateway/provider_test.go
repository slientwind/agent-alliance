package llmgateway

import (
	"alliance/internal/resources"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompatibleProvidersUsePrivateKeysAndTypedUsage(t *testing.T) {
	for _, kind := range []string{"bailian", "ark", "deepseek", "openai", "gemini", "vllm", "ollama"} {
		t.Run(kind, func(t *testing.T) {
			mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/base/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-key" {
					t.Errorf("unexpected upstream path or credential: %s", r.URL.Path)
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body["model"]) != `"fixture-model"` || len(body["tools"]) == 0 {
					t.Errorf("unexpected request: %v %v", body, err)
				}
				field := "max_tokens"
				if kind == "openai" {
					field = "max_completion_tokens"
				}
				if string(body[field]) != "256" {
					t.Errorf("configured output limit was not honored: %s", body[field])
				}
				io.WriteString(w, `{"choices":[{"message":{"content":null,"reasoning_content":"internal","tool_calls":[{"id":"call-a","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":4}}}`)
			}))
			defer mock.Close()
			provider, model := fixtureProvider(t, mock, kind)
			out, err := provider.Call(t.Context(), model, Request{Tier: Default, MaxTokens: 900,
				Messages: []resources.Message{{Role: "user", Content: "find"}}, Tools: []resources.ToolDefinition{lookupTool()}})
			if err != nil || out.Usage == nil || *out.Usage != (Usage{PromptTokens: 10, CompletionTokens: 2, CachedTokens: 4}) || len(out.Reply.ToolCalls) != 1 || out.Reply.ReasoningContent != "internal" {
				t.Fatalf("reply=%+v usage=%+v err=%v", out.Reply, out.Usage, err)
			}
		})
	}
}

func TestAnthropicNativeVisionAndToolResults(t *testing.T) {
	mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/base/v1/messages" || r.Header.Get("X-API-Key") != "fixture-key" || r.Header.Get("Anthropic-Version") == "" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Anthropic request path or headers: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		for _, required := range []string{`"system":"rules"`, `"type":"image"`, `"input_schema":{"type":"object"}`, `"type":"tool_result"`, `"tool_use_id":"previous"`, `"signature":"signed"`} {
			if !strings.Contains(string(body), required) {
				t.Errorf("missing %s in %s", required, body)
			}
		}
		io.WriteString(w, `{"content":[{"type":"thinking","thinking":"private","signature":"next-signed"},{"type":"tool_use","id":"next","name":"lookup","input":{"number":3}}],"stop_reason":"tool_use","usage":{"input_tokens":5,"cache_creation_input_tokens":3,"cache_read_input_tokens":2,"output_tokens":2}}`)
	}))
	defer mock.Close()
	provider, model := fixtureProvider(t, mock, "anthropic")
	image, err := resources.VisionProbePicture()
	if err != nil {
		t.Fatal(err)
	}
	in := Request{Tier: Vision, Tools: []resources.ToolDefinition{lookupTool()}, Messages: []resources.Message{
		{Role: "system", Content: "rules"}, {Role: "user", Content: "find"}, {Role: "assistant"}, {Role: "tool", ToolCallID: "previous", Content: "found"},
	}, ProviderHistory: map[int]ProviderMessage{2: {ProviderID: model.ProviderID, ModelID: model.ModelID,
		Blocks: []json.RawMessage{
			json.RawMessage(`{"type":"thinking","thinking":"private","signature":"signed"}`),
			json.RawMessage(`{"type":"tool_use","id":"previous","name":"lookup","input":{}}`),
		}}}}
	in.Pictures = append(in.Pictures, image)
	out, err := provider.Call(t.Context(), model, in)
	if err != nil || out.Usage == nil || *out.Usage != (Usage{PromptTokens: 10, CompletionTokens: 2, CachedTokens: 2}) || len(out.Reply.ToolCalls) != 1 || out.Reply.ToolCalls[0].Function.Arguments != `{"number":3}` {
		t.Fatalf("result=%+v err=%v", out, err)
	}
	if out.ProviderMessage == nil || !strings.Contains(string(out.ProviderMessage.Blocks[0]), "next-signed") {
		t.Fatal("provider-native continuation block missing")
	}
}

func TestProviderEmbeddingsAndInlineImages(t *testing.T) {
	picture, err := resources.VisionProbePicture()
	if err != nil {
		t.Fatal(err)
	}
	mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/base/v1/embeddings":
			io.WriteString(w, `{"data":[{"index":1,"embedding":[2,3]},{"index":0,"embedding":[1,0]}],"usage":{"prompt_tokens":10}}`)
		case "/base/v1/images/generations":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"b64_json": picture.Data}}, "usage": map[string]int{"input_tokens": 5}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()
	provider, model := fixtureProvider(t, mock, "openai")
	if out, err := provider.Call(t.Context(), model, Request{Tier: Embedding, Embeddings: []string{"one", "two"}}); err != nil || out.Usage == nil || out.Usage.PromptTokens != 10 || len(out.Embeddings) != 2 || out.Embeddings[0][0] != 1 || out.Embeddings[1][0] != 2 {
		t.Fatalf("embedding result=%+v err=%v", out, err)
	}
	if out, err := provider.Call(t.Context(), model, Request{Tier: Image, ImagePrompt: "a blue square"}); err != nil || out.Usage == nil || *out.Usage != (Usage{Images: 1, PromptTokens: 5}) || out.Reply.File == nil || out.Reply.File.Mime != "image/png" {
		t.Fatalf("image result=%+v err=%v", out, err)
	}
}

func TestProviderRedactsErrorsAndRejectsRedirects(t *testing.T) {
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("provider followed redirect")
	}))
	defer redirect.Close()
	mock := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect/chat/completions" {
			http.Redirect(w, r, redirect.URL, http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":"fixture-key"}`)
	}))
	defer mock.Close()
	provider, model := fixtureProvider(t, mock, "openai")
	in := Request{Tier: Default, Messages: []resources.Message{{Role: "user", Content: "test"}}}
	_, err := provider.Call(t.Context(), model, in)
	var problem ProviderError
	if !errors.As(err, &problem) || !problem.Retryable || problem.Status != 429 || strings.Contains(err.Error(), "fixture-key") {
		t.Fatalf("rate limit not sanitized: %v", err)
	}
	model.Endpoint = mock.URL + "/redirect"
	_, err = provider.Call(t.Context(), model, in)
	if !errors.As(err, &problem) || problem.Status != 307 {
		t.Fatalf("expected redirects to be rejected, got %v", err)
	}
}

func TestAnthropicStreamKeepsThinkingOutOfText(t *testing.T) {
	const input = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":6,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"private"}}
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}
data: {"type":"content_block_stop","index":0}
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"visible"}}
data: {"type":"content_block_stop","index":1}
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"a","name":"lookup","input":{}}}
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"n\":"}}
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"1}"}}
data: {"type":"content_block_stop","index":2}
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}
data: {"type":"message_stop"}
`
	var native anthropicResponse
	var shown strings.Builder
	observed := 0
	if err := readAnthropicStream(strings.NewReader(input), &native, func(text string) {
		if observed != 1 {
			t.Fatal("first output timing did not capture the reasoning chunk")
		}
		shown.WriteString(text)
	}, func() { observed++ }); err != nil {
		t.Fatal(err)
	}
	out, err := anthropicResult(Candidate{}, native, true)
	if err != nil || shown.String() != "visible" || out.Reply.ReasoningContent != "private" || out.Reply.ToolCalls[0].Function.Arguments != `{"n":1}` || out.Usage == nil || out.Usage.CompletionTokens != 3 {
		t.Fatalf("stream=%+v shown=%q err=%v", out, shown.String(), err)
	}
}

func lookupTool() resources.ToolDefinition {
	return resources.ToolDefinition{Type: "function", Function: resources.ToolSchema{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}
}

func fixtureProvider(t *testing.T, mock *httptest.Server, kind string) (HTTPProvider, Candidate) {
	t.Helper()
	vault, err := resources.NewVault(strings.Repeat("af", 32))
	if err != nil {
		t.Fatal(err)
	}
	key, err := vault.Seal("fixture-key", providerKeyScope("provider"))
	if err != nil {
		t.Fatal(err)
	}
	return HTTPProvider{Vault: vault, Client: mock.Client()}, Candidate{
		Name: "fixture-model", ModelID: "model", ProviderID: "provider", Kind: kind, EncryptedKey: key,
		Endpoint: mock.URL + "/base/v1", MaxOutputTokens: 256,
	}
}
