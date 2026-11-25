package llmgateway

import (
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type Request struct {
	Tier            Tier
	Messages        []resources.Message
	Tools           []resources.ToolDefinition
	Pictures        []knowledge.Picture
	MaxTokens       int
	ReasoningEffort string
	ProviderHistory map[int]ProviderMessage
	Embeddings      []string
	ImagePrompt     string
	EmitText        func(string)
}

type Result struct {
	Reply           resources.Reply
	Embeddings      [][]float64
	Usage           *Usage
	ProviderMessage *ProviderMessage
}

// ProviderMessage retains signed blocks for a subsequent turn on the same
// provider and model. It stays inside server-side execution state.
type ProviderMessage struct {
	ProviderID string
	ModelID    string
	Blocks     []json.RawMessage
}

type HTTPProvider struct {
	Vault  resources.Vault
	Client *http.Client
}

type ProviderError struct {
	Status    int
	Retryable bool
	Unsent    bool
}

func (e ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("model provider returned HTTP %d", e.Status)
	}
	return "model provider result unavailable"
}

func providerKeyScope(id string) string { return "model-provider/" + id }

func (p HTTPProvider) Call(ctx context.Context, candidate Candidate, in Request) (Result, error) {
	key := ""
	if len(candidate.EncryptedKey) != 0 {
		var err error
		key, err = p.Vault.Open(candidate.EncryptedKey, providerKeyScope(candidate.ProviderID))
		if err != nil {
			return Result{}, ProviderError{Unsent: true}
		}
	}
	if !providerEndpoint(candidate.Kind, candidate.Endpoint) {
		return Result{}, ProviderError{Unsent: true}
	}
	if candidate.Kind == "anthropic" {
		return p.anthropicChat(ctx, candidate, in, key)
	}
	switch in.Tier {
	case Embedding:
		return p.embed(ctx, candidate, in, key)
	case Image:
		return p.generateImage(ctx, candidate, in, key)
	default:
		return p.compatibleChat(ctx, candidate, in, key)
	}
}

func providerEndpoint(kind, endpoint string) bool {
	base, err := url.Parse(endpoint)
	if err != nil || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" {
		return false
	}
	switch kind {
	case "vllm", "ollama":
		return base.Scheme == "http" || base.Scheme == "https"
	case "ark", "bailian", "deepseek", "openai", "anthropic", "gemini":
		return base.Scheme == "https"
	default:
		return false
	}
}

func outputLimit(candidate Candidate, requested int) int {
	if requested <= 0 || (candidate.MaxOutputTokens > 0 && requested > candidate.MaxOutputTokens) {
		return candidate.MaxOutputTokens
	}
	return requested
}

func (p HTTPProvider) compatibleChat(ctx context.Context, model Candidate, in Request, key string) (Result, error) {
	m := resources.Model{Name: model.Name, Tools: in.Tools, MaxTokens: outputLimit(model, in.MaxTokens)}
	if len(in.Pictures) != 0 {
		m.OutputOperation = resources.VisionOperation
		m.Pictures = in.Pictures
	}
	switch model.Kind {
	case "openai":
		m.TokenLimitField = "max_completion_tokens"
		m.ReasoningEffort = in.ReasoningEffort
	case "deepseek", "gemini", "vllm", "ollama":
		m.ReasoningEffort = in.ReasoningEffort
	}
	body, err := m.EncodeChatRequest(in.Messages, in.EmitText != nil)
	if err != nil {
		return Result{}, ProviderError{Unsent: true}
	}
	resp, err := p.post(ctx, model, "chat/completions", key, body)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var reply resources.Reply
	if in.EmitText != nil {
		reply, err = resources.DecodeStreamingChatReply(resp.Header.Get("Content-Type"), resp.Body, in.EmitText, len(in.Tools) > 0)
	} else {
		reply, err = resources.DecodeChatReply(resp.Body, len(in.Tools) > 0)
	}
	if err != nil {
		return Result{}, ProviderError{Retryable: true}
	}
	return resultFromReply(reply), nil
}

func resultFromReply(reply resources.Reply) Result {
	out := Result{Reply: reply}
	if reply.UsageReported {
		out.Usage = &Usage{PromptTokens: int64(reply.PromptTokens), CompletionTokens: int64(reply.CompletionTokens), CachedTokens: int64(reply.CachedTokens)}
	}
	return out
}

func (p HTTPProvider) post(ctx context.Context, model Candidate, operation, key string, body []byte) (*http.Response, error) {
	endpoint, err := url.JoinPath(model.Endpoint, operation)
	if err != nil {
		return nil, ProviderError{Unsent: true}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, ProviderError{Unsent: true}
	}
	req.Header.Set("Content-Type", "application/json")
	if model.Kind == "anthropic" {
		req.Header.Set("Anthropic-Version", "2023-06-01")
		req.Header.Set("X-API-Key", key)
	} else if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := p.Client
	if client == nil {
		client, err = resources.ProviderHTTPClient()
		if err != nil {
			return nil, ProviderError{Unsent: true}
		}
	}
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return nil, ProviderError{Retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		status := resp.StatusCode
		return nil, ProviderError{Status: status,
			Retryable: status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout}
	}
	return resp, nil
}

func readProviderJSON(body io.Reader, limit int64, target any) error {
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return errors.New("model response unreadable or too large")
	}
	return json.Unmarshal(raw, target)
}

func (p HTTPProvider) embed(ctx context.Context, candidate Candidate, in Request, key string) (Result, error) {
	if len(in.Embeddings) == 0 || len(in.Embeddings) > 32 {
		return Result{}, ProviderError{Unsent: true}
	}
	body, err := json.Marshal(map[string]any{"model": candidate.Name, "input": in.Embeddings, "encoding_format": "float"})
	if err != nil {
		return Result{}, ProviderError{Unsent: true}
	}
	resp, err := p.post(ctx, candidate, "embeddings", key, body)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var data struct {
		Data []struct {
			Index     int
			Embedding []float64
		}
		Usage struct {
			Prompt *int64 `json:"prompt_tokens"`
		}
	}
	if readProviderJSON(resp.Body, 8<<20, &data) != nil || len(data.Data) != len(in.Embeddings) {
		return Result{}, ProviderError{Retryable: true}
	}
	out := Result{Embeddings: make([][]float64, len(in.Embeddings))}
	dims := len(data.Data[0].Embedding)
	for _, row := range data.Data {
		if dims == 0 || dims > 8192 || row.Index < 0 || row.Index >= len(out.Embeddings) || len(row.Embedding) != dims || out.Embeddings[row.Index] != nil {
			return Result{}, ProviderError{Retryable: true}
		}
		out.Embeddings[row.Index] = row.Embedding
	}
	if data.Usage.Prompt != nil && *data.Usage.Prompt >= 0 {
		out.Usage = &Usage{PromptTokens: *data.Usage.Prompt}
	}
	return out, nil
}

func (p HTTPProvider) generateImage(ctx context.Context, candidate Candidate, in Request, key string) (Result, error) {
	if strings.TrimSpace(in.ImagePrompt) == "" {
		return Result{}, ProviderError{Unsent: true}
	}
	request := map[string]any{"model": candidate.Name, "prompt": in.ImagePrompt, "n": 1, "response_format": "b64_json"}
	if candidate.Kind == "openai" && strings.HasPrefix(candidate.Name, "gpt-image") {
		delete(request, "response_format")
		request["output_format"] = "png"
	}
	body, err := json.Marshal(request)
	if err != nil {
		return Result{}, ProviderError{Unsent: true}
	}
	resp, err := p.post(ctx, candidate, "images/generations", key, body)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var data struct {
		Data []struct {
			PNG []byte `json:"b64_json"`
		}
		Usage struct {
			Input  int64 `json:"input_tokens"`
			Output int64 `json:"output_tokens"`
		}
	}
	if readProviderJSON(resp.Body, (8<<20)+32_768, &data) != nil || len(data.Data) != 1 || len(data.Data[0].PNG) > 6<<20 || data.Usage.Input < 0 || data.Usage.Output < 0 {
		return Result{}, ProviderError{Retryable: true}
	}
	png := data.Data[0].PNG
	width, height, err := knowledge.PNGDimensions(bytes.NewReader(png))
	if err != nil {
		return Result{}, ProviderError{Retryable: true}
	}
	return Result{Reply: resources.Reply{
		Text: fmt.Sprintf("PNG 图像 · %d × %d 像素", width, height),
		File: &resources.File{Name: "image.png", Mime: "image/png", Data: png},
	}, Usage: &Usage{Images: 1, PromptTokens: data.Usage.Input, CompletionTokens: data.Usage.Output}}, nil
}
