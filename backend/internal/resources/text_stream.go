package resources

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"strings"
)

const TextStreamLimit = 64 << 10

var errTextStreamInterrupted = errors.New("文本流未返回完整答复")

// StreamTextReply calls the opted-in text endpoint once. Some compatible
// providers return an ordinary JSON reply to the same request.
func (m Model) StreamTextReply(ctx context.Context, messages []Message, emit func(string)) (Reply, error) {
	if m.External && m.Adapter != nil && m.Adapter.Stream && m.RemoteOperations.CanStart() {
		return m.generateExternal(ctx, messages, emit)
	}
	if !m.StreamText || m.OutputOperation != "text" || m.External || m.Tool != nil || !Endpoint(m.Endpoint) {
		return Reply{}, errors.New("文本流未配置")
	}
	resp, err := m.requestText(ctx, messages, true)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	return DecodeStreamingChatReply(resp.Header.Get("Content-Type"), resp.Body, emit, len(m.Tools) != 0)
}

func DecodeStreamingChatReply(contentType string, body io.Reader, emit func(string), allowTools bool) (Reply, error) {
	return DecodeObservedStreamingChatReply(contentType, body, emit, allowTools, nil)
}

func DecodeObservedStreamingChatReply(contentType string, body io.Reader, emit func(string), allowTools bool, observeChunk func()) (Reply, error) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	switch mediaType {
	case "text/event-stream":
		return decodeTextStream(body, emit, allowTools, observeChunk)
	case "", "application/json":
		reply, err := DecodeChatReply(body, allowTools)
		if err != nil {
			return Reply{}, err
		}
		if reply.FinishReason != "" && !textStreamFinished(reply.FinishReason, allowTools) {
			return Reply{}, errTextStreamInterrupted
		}
		return reply, nil
	default:
		return Reply{}, errTextStreamInterrupted
	}
}

func textStreamFinished(reason string, allowTools bool) bool {
	return reason == "stop" || (allowTools && reason == "tool_calls")
}

type toolDelta struct {
	Index *int `json:"index"`
	ToolCall
}

type streamedToolCall struct {
	ID, Type, Name string
	Arguments      strings.Builder
}

func decodeTextStream(input io.Reader, emit func(string), allowTools bool, observeChunk func()) (Reply, error) {
	scan := bufio.NewScanner(io.LimitReader(input, 4<<20))
	scan.Buffer(make([]byte, 8192), 256<<10)
	var frame, text, reasoning strings.Builder
	var answer Reply
	var calls []*streamedToolCall
	for scan.Scan() {
		line := scan.Text()
		if field, ok := strings.CutPrefix(line, "data:"); ok {
			if frame.Len()+len(field) > 256<<10 {
				return Reply{}, errTextStreamInterrupted
			}
			if frame.Len() > 0 {
				frame.WriteByte('\n')
			}
			frame.WriteString(strings.TrimPrefix(field, " "))
			continue
		}
		if line != "" || frame.Len() == 0 {
			continue
		}
		data := frame.String()
		frame.Reset()
		if data == "[DONE]" {
			if !textStreamFinished(answer.FinishReason, allowTools) {
				return Reply{}, errTextStreamInterrupted
			}
			for _, call := range calls {
				if call == nil {
					return Reply{}, errTextStreamInterrupted
				}
				answer.ToolCalls = append(answer.ToolCalls, ToolCall{ID: call.ID, Type: call.Type,
					Function: ToolFunction{Name: call.Name, Arguments: call.Arguments.String()}})
			}
			if answer.FinishReason == "tool_calls" {
				if !validToolCalls(answer.ToolCalls) {
					return Reply{}, errTextStreamInterrupted
				}
			} else if len(calls) != 0 || strings.TrimSpace(text.String()) == "" {
				return Reply{}, errTextStreamInterrupted
			}
			answer.Text = text.String()
			answer.ReasoningContent = reasoning.String()
			return answer, nil
		}
		var chunk struct {
			Error   json.RawMessage
			Choices []struct {
				Index int
				Delta struct {
					Content          string
					Role             string
					ReasoningContent string      `json:"reasoning_content"`
					ToolCalls        []toolDelta `json:"tool_calls"`
				}
				FinishReason string `json:"finish_reason"`
			}
			Usage *modelUsage
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || (chunk.Error != nil && string(chunk.Error) != "null") {
			return Reply{}, errTextStreamInterrupted
		}
		if chunk.Usage != nil {
			chunk.Usage.apply(&answer)
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != "" && !textStreamFinished(choice.FinishReason, allowTools) {
				return Reply{}, errTextStreamInterrupted
			}
			if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
				return Reply{}, errTextStreamInterrupted
			}
			if delta := choice.Delta.Content; delta != "" {
				if answer.FinishReason != "" || text.Len()+len(delta) > TextStreamLimit || strings.ContainsRune(delta, 0) {
					return Reply{}, errTextStreamInterrupted
				}
				if observeChunk != nil {
					observeChunk()
					observeChunk = nil
				}
				text.WriteString(delta)
				if emit != nil {
					emit(delta)
				}
			}
			if observeChunk != nil && choice.Delta.ReasoningContent != "" {
				observeChunk()
				observeChunk = nil
			}
			reasoning.WriteString(choice.Delta.ReasoningContent)
			for _, delta := range choice.Delta.ToolCalls {
				if !allowTools || answer.FinishReason != "" || !mergeToolDelta(&calls, delta) {
					return Reply{}, errTextStreamInterrupted
				}
				if observeChunk != nil && (delta.Function.Name != "" || delta.Function.Arguments != "") {
					observeChunk()
					observeChunk = nil
				}
			}
			if choice.FinishReason != "" {
				answer.FinishReason = choice.FinishReason
			}
		}
	}
	return Reply{}, errTextStreamInterrupted
}

func mergeToolDelta(calls *[]*streamedToolCall, delta toolDelta) bool {
	if delta.Index == nil || *delta.Index < 0 || *delta.Index >= 128 {
		return false
	}
	index := *delta.Index
	for len(*calls) <= index {
		*calls = append(*calls, nil)
	}
	call := (*calls)[index]
	if call == nil {
		call = &streamedToolCall{}
		(*calls)[index] = call
	}
	if delta.ID != "" {
		if call.ID != "" && call.ID != delta.ID {
			return false
		}
		call.ID = delta.ID
	}
	if delta.Type != "" {
		if call.Type != "" && call.Type != delta.Type {
			return false
		}
		call.Type = delta.Type
	}
	call.Name += delta.Function.Name
	call.Arguments.WriteString(delta.Function.Arguments)
	return true
}
