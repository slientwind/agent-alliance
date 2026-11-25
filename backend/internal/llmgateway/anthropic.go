package llmgateway

import (
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"strings"
)

type anthropicBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	Data      string          `json:"data,omitempty"`
}

type anthropicUsage struct {
	Input      *int `json:"input_tokens"`
	Output     *int `json:"output_tokens"`
	CacheWrite *int `json:"cache_creation_input_tokens"`
	CacheRead  *int `json:"cache_read_input_tokens"`
}

type anthropicResponse struct {
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      anthropicUsage    `json:"usage"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

func anthropicRequest(model Candidate, in Request) ([]byte, error) {
	limit := outputLimit(model, in.MaxTokens)
	if limit <= 0 {
		return nil, errors.New("Anthropic requires an output limit")
	}
	var system []string
	var conversation []anthropicMessage
	attached := false
	for index, message := range in.Messages {
		var blocks []any
		role := message.Role
		switch role {
		case "system":
			if len(conversation) != 0 {
				return nil, errors.New("system message must precede Anthropic conversation")
			}
			system = append(system, message.Content)
			continue
		case "user":
			blocks = []any{anthropicBlock{Type: "text", Text: message.Content}}
			if !attached && len(in.Pictures) != 0 {
				if len(in.Pictures) > knowledge.MaxPictures {
					return nil, errors.New("too many Anthropic input images")
				}
				var bytes int
				for _, picture := range in.Pictures {
					bytes += len(picture.Data)
					if len(picture.Data) == 0 || len(picture.Data) > knowledge.PictureLimit || bytes > knowledge.RunPictureLimit ||
						(picture.Media != "image/png" && picture.Media != "image/jpeg") {
						return nil, errors.New("invalid Anthropic input image")
					}
					blocks = append(blocks, map[string]any{"type": "image", "source": map[string]string{
						"type": "base64", "media_type": picture.Media, "data": base64.StdEncoding.EncodeToString(picture.Data),
					}})
				}
				attached = true
			}
		case "assistant":
			native := in.ProviderHistory[index]
			if native.ProviderID == model.ProviderID && native.ModelID == model.ModelID && len(native.Blocks) != 0 {
				for _, block := range native.Blocks {
					blocks = append(blocks, block)
				}
			} else {
				if message.Content != "" {
					blocks = append(blocks, anthropicBlock{Type: "text", Text: message.Content})
				}
				for _, call := range message.ToolCalls {
					blocks = append(blocks, anthropicBlock{Type: "tool_use", ID: call.ID, Name: call.Function.Name, Input: json.RawMessage(call.Function.Arguments)})
				}
			}
		case "tool":
			role = "user"
			blocks = []any{map[string]string{"type": "tool_result", "tool_use_id": message.ToolCallID, "content": message.Content}}
		default:
			return nil, errors.New("unsupported Anthropic message role")
		}
		if len(blocks) == 0 {
			continue
		}
		last := len(conversation) - 1
		if last >= 0 && conversation[last].Role == role {
			conversation[last].Content = append(conversation[last].Content, blocks...)
		} else {
			conversation = append(conversation, anthropicMessage{role, blocks})
		}
	}
	if len(conversation) == 0 || (len(in.Pictures) != 0 && !attached) {
		return nil, errors.New("Anthropic message required")
	}
	request := map[string]any{"model": model.Name, "max_tokens": limit, "messages": conversation}
	if len(system) != 0 {
		request["system"] = strings.Join(system, "\n\n")
	}
	if in.EmitText != nil {
		request["stream"] = true
	}
	if len(in.Tools) != 0 {
		tools := make([]map[string]any, 0, len(in.Tools))
		for _, tool := range in.Tools {
			tools = append(tools, map[string]any{"name": tool.Function.Name, "description": tool.Function.Description, "input_schema": tool.Function.Parameters})
		}
		request["tools"] = tools
	}
	if in.ReasoningEffort != "" && in.ReasoningEffort != "none" && limit > 1024 {
		budget := 1024
		switch in.ReasoningEffort {
		case "medium":
			budget = max(budget, limit/2)
		case "high":
			budget = max(budget, limit*3/4)
		}
		request["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	}
	return json.Marshal(request)
}

func (p HTTPProvider) anthropicChat(ctx context.Context, model Candidate, in Request, key string) (Result, error) {
	if in.Tier == Image || in.Tier == Embedding {
		return Result{}, ProviderError{Unsent: true}
	}
	body, err := anthropicRequest(model, in)
	if err != nil {
		return Result{}, ProviderError{Unsent: true}
	}
	resp, err := p.post(ctx, model, "messages", key, body)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	var native anthropicResponse
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if media == "text/event-stream" && in.EmitText != nil {
		err = readAnthropicStream(resp.Body, &native, in.EmitText, in.observeChunk)
	} else {
		err = readProviderJSON(resp.Body, 4<<20, &native)
	}
	if err != nil {
		return Result{}, ProviderError{Retryable: true}
	}
	return anthropicResult(model, native, len(in.Tools) != 0)
}

func anthropicResult(model Candidate, native anthropicResponse, allowTools bool) (Result, error) {
	var reply resources.Reply
	var text, reasoning strings.Builder
	ids := make(map[string]bool)
	for _, raw := range native.Content {
		var block anthropicBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return Result{}, ProviderError{Retryable: true}
		}
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "thinking":
			reasoning.WriteString(block.Thinking)
		case "tool_use":
			if !allowTools || block.ID == "" || block.Name == "" || ids[block.ID] || len(reply.ToolCalls) >= 128 {
				return Result{}, ProviderError{Retryable: true}
			}
			ids[block.ID] = true
			reply.ToolCalls = append(reply.ToolCalls, resources.ToolCall{ID: block.ID, Type: "function",
				Function: resources.ToolFunction{Name: block.Name, Arguments: string(block.Input)}})
		}
	}
	reply.Text, reply.ReasoningContent = text.String(), reasoning.String()
	switch native.StopReason {
	case "end_turn", "stop_sequence":
		reply.FinishReason = "stop"
	case "tool_use":
		reply.FinishReason = "tool_calls"
	case "max_tokens":
		reply.FinishReason = "length"
	default:
		return Result{}, ProviderError{Retryable: true}
	}
	if (len(reply.ToolCalls) != 0) != (reply.FinishReason == "tool_calls") || (len(reply.ToolCalls) == 0 && strings.TrimSpace(reply.Text) == "") {
		return Result{}, ProviderError{Retryable: true}
	}
	u := native.Usage
	if u.Input != nil && u.Output != nil {
		var read, write int
		if u.CacheRead != nil {
			read = *u.CacheRead
		}
		if u.CacheWrite != nil {
			write = *u.CacheWrite
		}
		reply.PromptTokens, reply.CompletionTokens, reply.CachedTokens = *u.Input+write+read, *u.Output, read
		reply.Tokens = reply.PromptTokens + reply.CompletionTokens
		reply.UsageReported = *u.Input >= 0 && *u.Output >= 0 && read >= 0 && write >= 0 && reply.Tokens >= 0
	}
	out := resultFromReply(reply)
	out.ProviderMessage = &ProviderMessage{ProviderID: model.ProviderID, ModelID: model.ModelID, Blocks: native.Content}
	return out, nil
}

func readAnthropicStream(body io.Reader, native *anthropicResponse, emit func(string), observeChunk func()) error {
	const maxResponse = 4 << 20
	scan := bufio.NewScanner(io.LimitReader(body, maxResponse))
	scan.Buffer(make([]byte, 8192), 256<<10)
	var blocks []anthropicBlock
	active := -1
	var input strings.Builder
	var started bool
	var textBytes int
	observe := func(nonempty bool) {
		if nonempty && observeChunk != nil {
			observeChunk()
			observeChunk = nil
		}
	}
	emitText := func(text string) error {
		textBytes += len(text)
		if textBytes > resources.TextStreamLimit || strings.ContainsRune(text, 0) {
			return errors.New("invalid Anthropic text delta")
		}
		if text != "" {
			observe(true)
			emit(text)
		}
		return nil
	}
	for scan.Scan() {
		line, ok := strings.CutPrefix(scan.Text(), "data:")
		if !ok {
			continue
		}
		var event struct {
			Type    string
			Index   int
			Message anthropicResponse
			Block   anthropicBlock `json:"content_block"`
			Usage   anthropicUsage
			Delta   struct {
				Type, Text, Thinking, Signature string
				PartialJSON                     string `json:"partial_json"`
				StopReason                      string `json:"stop_reason"`
			}
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return err
		}
		switch event.Type {
		case "message_start":
			if started {
				return errors.New("duplicate Anthropic message start")
			}
			started = true
			native.Usage = event.Message.Usage
		case "content_block_start":
			if !started || active != -1 || event.Index != len(blocks) || len(blocks) >= 256 {
				return errors.New("invalid Anthropic block index")
			}
			active = event.Index
			blocks = append(blocks, event.Block)
			observe(event.Block.Thinking != "" ||
				event.Block.Type == "tool_use" && event.Block.Name != "" ||
				event.Block.Type == "redacted_thinking" && event.Block.Data != "")
			if event.Block.Type == "text" {
				if err := emitText(event.Block.Text); err != nil {
					return err
				}
			}
		case "content_block_delta":
			if active == -1 || event.Index != active {
				return errors.New("invalid Anthropic block delta")
			}
			b := &blocks[active]
			switch event.Delta.Type {
			case "text_delta":
				if b.Type != "text" {
					return errors.New("invalid Anthropic text delta")
				}
				if err := emitText(event.Delta.Text); err != nil {
					return err
				}
				b.Text += event.Delta.Text
			case "thinking_delta":
				observe(event.Delta.Thinking != "")
				b.Thinking += event.Delta.Thinking
			case "signature_delta":
				b.Signature += event.Delta.Signature
			case "input_json_delta":
				observe(event.Delta.PartialJSON != "")
				input.WriteString(event.Delta.PartialJSON)
			}
		case "content_block_stop":
			if active == -1 || event.Index != active {
				return errors.New("invalid Anthropic block stop")
			}
			if input.Len() != 0 {
				blocks[active].Input = json.RawMessage(input.String())
				input.Reset()
			}
			active = -1
		case "message_delta":
			native.StopReason = event.Delta.StopReason
			if event.Usage.Output != nil {
				native.Usage.Output = event.Usage.Output
			}
		case "message_stop":
			if !started || active != -1 || native.StopReason == "" {
				return errors.New("incomplete Anthropic stream")
			}
			for _, block := range blocks {
				raw, err := json.Marshal(block)
				if err != nil {
					return err
				}
				native.Content = append(native.Content, raw)
			}
			return nil
		case "error":
			return errors.New("Anthropic stream interrupted")
		}
	}
	return errors.New("incomplete Anthropic stream")
}
