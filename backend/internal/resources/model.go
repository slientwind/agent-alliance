package resources

import (
	"alliance/internal/egress"
	"alliance/internal/knowledge"
	"alliance/internal/skills"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Vault struct{ aead cipher.AEAD }

func NewVault(key string) (Vault, error) {
	raw, err := hex.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return Vault{}, errors.New("RESOURCE_KEY must be 32 hex-encoded bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return Vault{}, err
	}
	aead, err := cipher.NewGCM(block)
	return Vault{aead}, err
}
func (v Vault) Seal(value, scope string) ([]byte, error) {
	if v.aead == nil {
		return nil, errors.New("resource vault is not configured")
	}
	n := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return v.aead.Seal(n, n, []byte(value), []byte(scope)), nil
}
func (v Vault) Open(value []byte, scope string) (string, error) {
	if v.aead == nil {
		return "", errors.New("resource vault is not configured")
	}
	n := v.aead.NonceSize()
	if len(value) < n {
		return "", errors.New("invalid secret reference")
	}
	raw, err := v.aead.Open(nil, value[:n], value[n:], []byte(scope))
	return string(raw), err
}
func Endpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

type ToolDefinition struct {
	Type     string     `json:"type"`
	Function ToolSchema `json:"function"`
}

type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Reply struct {
	Receipt          *RemoteWait
	File             *File
	Text             string
	Tokens           int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	UsageReported    bool
	ToolCalls        []ToolCall
	ReasoningContent string
	FinishReason     string
	ExternalMetadata ExternalMetadata `json:"-"`
	wireUsage        *ExternalUsage
}

type Model struct {
	SkillRefs         []skills.Ref
	ProjectGrant      string
	OutputOperation   string
	ResourceID        string
	KeyGeneration     int
	StreamText        bool
	RemoteOperations  *RemoteOperations
	ExternalTrace     ExternalTrace
	AuthorizeExternal func(context.Context) error
	Adapter           *AdapterSpec
	Tools             []ToolDefinition
	MaxTokens         int
	TokenLimitField   string
	ReasoningEffort   string

	SourceRevision                                 int
	Policy                                         RemotePolicy
	Tool                                           *Tool
	Sources                                        []Source
	Pictures                                       []knowledge.Picture
	ImageRegion                                    *knowledge.ImageRegion
	ToolInput                                      string
	Endpoint, Name, Key, Operation, Team, BudgetID string
	Estimate                                       *string
	External                                       bool
	outbound                                       egress.Policy
}

func (m Model) Generate(ctx context.Context, messages []Message) (Reply, error) {
	if m.Tool != nil {
		text := m.ToolInput
		if text == "" && len(m.Sources) == 0 {
			for _, msg := range messages {
				if msg.Role == "user" {
					text = msg.Content
					break
				}
			}
		}
		return m.Tool.invoke(ctx, text, m.Sources)
	}
	if m.External {
		if !m.RemoteOperations.CanStart() {
			return Reply{}, errors.New("此发布修订的远端提交与成果验证已失效")
		}
		return m.generateExternal(ctx, messages, nil)
	}
	if !Endpoint(m.Endpoint) {
		return Reply{}, errors.New("invalid provider endpoint")
	}
	if ProducesPNG(m.OutputOperation) {
		return m.generateImage(ctx, messages)
	}
	resp, err := m.requestText(ctx, messages, false)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	return DecodeChatReply(resp.Body, len(m.Tools) != 0)
}

func (m Model) EncodeChatRequest(messages []Message, stream bool) ([]byte, error) {
	var providerMessages any = messages
	if m.OutputOperation == VisionOperation {
		var err error
		providerMessages, err = visionMessages(messages, m.Pictures)
		if err != nil {
			return nil, err
		}
	}
	request := map[string]any{"model": m.Name, "messages": providerMessages}
	if m.MaxTokens > 0 {
		field := "max_tokens"
		if m.TokenLimitField == "max_completion_tokens" {
			field = m.TokenLimitField
		}
		request[field] = m.MaxTokens
	}
	if m.ReasoningEffort != "" {
		request["reasoning_effort"] = m.ReasoningEffort
	}
	if len(m.Tools) != 0 {
		request["tools"] = m.Tools
	}
	if stream {
		request["stream"] = true
		request["stream_options"] = map[string]bool{"include_usage": true}
	}
	return json.Marshal(request)
}

func (m Model) requestText(ctx context.Context, messages []Message, stream bool) (*http.Response, error) {
	body, err := m.EncodeChatRequest(messages, stream)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Key)
	client, err := providerClient()
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("模型请求结果待核对")
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, ProviderStatus(resp.StatusCode)
	}
	return resp, nil
}

func DecodeChatReply(body io.Reader, allowTools bool) (Reply, error) {
	var result struct {
		Choices []struct {
			Message      Message
			FinishReason string `json:"finish_reason"`
		}
		Usage modelUsage
	}
	if err := json.NewDecoder(io.LimitReader(body, 4<<20)).Decode(&result); err != nil || len(result.Choices) == 0 {
		return Reply{}, errors.New("供应商响应缺少有效正文")
	}
	choice := result.Choices[0]
	reply := Reply{
		Text: choice.Message.Content, ToolCalls: choice.Message.ToolCalls,
		ReasoningContent: choice.Message.ReasoningContent, FinishReason: choice.FinishReason,
	}
	if len(reply.ToolCalls) != 0 || reply.FinishReason == "tool_calls" {
		if !allowTools || reply.FinishReason != "tool_calls" || !validToolCalls(reply.ToolCalls) {
			return Reply{}, errors.New("供应商响应缺少有效工具调用")
		}
	} else if strings.TrimSpace(reply.Text) == "" {
		return Reply{}, errors.New("供应商响应缺少有效正文")
	}
	result.Usage.apply(&reply)
	return reply, nil
}

type modelUsage struct {
	Total      *int `json:"total_tokens"`
	Prompt     *int `json:"prompt_tokens"`
	Completion *int `json:"completion_tokens"`
	CacheHit   *int `json:"prompt_cache_hit_tokens"`
	Details    struct {
		Cached *int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func (u modelUsage) apply(reply *Reply) {
	reply.Tokens = 0
	if u.Total != nil {
		reply.Tokens = *u.Total
	}
	if u.Prompt != nil {
		reply.PromptTokens = *u.Prompt
	}
	if u.Completion != nil {
		reply.CompletionTokens = *u.Completion
	}
	if reply.Tokens == 0 {
		reply.Tokens = reply.PromptTokens + reply.CompletionTokens
	}
	reply.CachedTokens = 0
	if u.CacheHit != nil {
		reply.CachedTokens = *u.CacheHit
	}
	if u.Details.Cached != nil {
		reply.CachedTokens = *u.Details.Cached
	}
	reply.UsageReported = u.Prompt != nil && u.Completion != nil && reply.PromptTokens >= 0 && reply.CompletionTokens >= 0 &&
		reply.CachedTokens >= 0 && reply.CachedTokens <= reply.PromptTokens
	usage := validateExternalUsage(&ExternalUsage{Input: externalInt(u.Prompt), Output: externalInt(u.Completion),
		Total: externalInt(u.Total), Cached: firstInt(externalInt(u.Details.Cached), externalInt(u.CacheHit))})
	if usage != nil {
		reply.wireUsage = usage
	}
}

func validToolCalls(calls []ToolCall) bool {
	if len(calls) == 0 || len(calls) > 128 {
		return false
	}
	ids := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if _, exists := ids[call.ID]; exists || call.ID == "" || call.Type != "function" || call.Function.Name == "" {
			return false
		}
		ids[call.ID] = struct{}{}
	}
	return true
}
