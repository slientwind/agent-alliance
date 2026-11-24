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
	if !m.StreamText || m.OutputOperation != "text" || m.External || m.Tool != nil || !Endpoint(m.Endpoint) {
		return Reply{}, errors.New("文本流未配置")
	}
	resp, err := m.requestText(ctx, messages, true)
	if err != nil {
		return Reply{}, err
	}
	defer resp.Body.Close()
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "text/event-stream":
		return decodeTextStream(resp.Body, emit)
	case "", "application/json":
		reply, err := decodeTextReply(resp.Body)
		if err != nil {
			return Reply{}, err
		}
		if reply.FinishReason != "" && reply.FinishReason != "stop" {
			return Reply{}, errTextStreamInterrupted
		}
		return reply, nil
	default:
		return Reply{}, errTextStreamInterrupted
	}
}

func decodeTextStream(input io.Reader, emit func(string)) (Reply, error) {
	scan := bufio.NewScanner(io.LimitReader(input, 4<<20))
	scan.Buffer(make([]byte, 8192), 256<<10)
	var frame, text strings.Builder
	var answer Reply
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
			if answer.FinishReason != "stop" || strings.TrimSpace(text.String()) == "" {
				return Reply{}, errTextStreamInterrupted
			}
			answer.Text = text.String()
			return answer, nil
		}
		var chunk struct {
			Error   json.RawMessage
			Choices []struct {
				Index int
				Delta struct {
					Content string
					Role    string
				}
				FinishReason string `json:"finish_reason"`
			}
			Usage *struct {
				Total int `json:"total_tokens"`
			}
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil || (chunk.Error != nil && string(chunk.Error) != "null") {
			return Reply{}, errTextStreamInterrupted
		}
		if chunk.Usage != nil {
			answer.Tokens = chunk.Usage.Total
		}
		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != "" && choice.FinishReason != "stop" {
				return Reply{}, errTextStreamInterrupted
			}
			if choice.Delta.Role != "" && choice.Delta.Role != "assistant" {
				return Reply{}, errTextStreamInterrupted
			}
			if delta := choice.Delta.Content; delta != "" {
				if answer.FinishReason != "" || text.Len()+len(delta) > TextStreamLimit || strings.ContainsRune(delta, 0) {
					return Reply{}, errTextStreamInterrupted
				}
				text.WriteString(delta)
				emit(delta)
			}
			if choice.FinishReason == "stop" {
				answer.FinishReason = "stop"
			}
		}
	}
	return Reply{}, errTextStreamInterrupted
}
