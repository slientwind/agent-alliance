package resources

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

type externalFrame struct {
	name string
	data string
}

// readExternalFrames bounds each frame and the whole stream. LangGraph can
// close a successfully streamed state response without a separate end event.
func readExternalFrames(body io.Reader, canEnd func() bool, onFrame func(externalFrame) (bool, error)) error {
	const frameLimit = 256 << 10
	limited := &io.LimitedReader{R: body, N: (4 << 20) + 1}
	lines := bufio.NewScanner(limited)
	lines.Buffer(make([]byte, 8192), frameLimit)
	var name string
	var data strings.Builder
	for lines.Scan() {
		line := lines.Text()
		switch {
		case line == "":
			if data.Len() != 0 || name != "" {
				done, err := onFrame(externalFrame{name: name, data: data.String()})
				if err != nil || done {
					return err
				}
			}
			name = ""
			data.Reset()
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			value := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			if data.Len()+len(value)+1 > frameLimit {
				return errExternalReply
			}
			if data.Len() != 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
	if lines.Err() != nil || limited.N == 0 || data.Len() != 0 || name != "" || canEnd == nil || !canEnd() {
		return errExternalReply
	}
	return nil
}

func appendExternalText(text *strings.Builder, delta string, emit func(string)) error {
	if strings.ContainsRune(delta, 0) || text.Len()+len(delta) > TextStreamLimit {
		return errExternalReply
	}
	text.WriteString(delta)
	if emit != nil && delta != "" {
		emit(delta)
	}
	return nil
}

func streamResult(text string, err error) (Reply, error) {
	if err != nil || !validExternalText(text) {
		return Reply{}, errExternalReply
	}
	return Reply{Text: text, FinishReason: "stop"}, nil
}

func decodeDify(body io.Reader, emit func(string)) (Reply, error) {
	var text strings.Builder
	var agent, workflow, messageEnd, workflowDone bool
	err := readExternalFrames(body, nil, func(frame externalFrame) (bool, error) {
		var event struct {
			Event, Answer string
			Data          struct{ Status string }
		}
		if frame.data == "" || frame.data == "[PING]" {
			return false, nil
		}
		if json.Unmarshal([]byte(frame.data), &event) != nil {
			return false, errExternalReply
		}
		if event.Event == "" {
			event.Event = frame.name
		}
		switch event.Event {
		case "error":
			return false, errExternalReply
		case "workflow_started":
			workflow = true
		case "workflow_finished":
			if event.Data.Status != "succeeded" {
				return false, errExternalReply
			}
			workflowDone = true
			return messageEnd, nil
		case "agent_message":
			agent = true
			return false, appendExternalText(&text, event.Answer, emit)
		case "message":
			if !agent {
				return false, appendExternalText(&text, event.Answer, emit)
			}
		case "message_replace":
			text.Reset()
			return false, appendExternalText(&text, event.Answer, nil)
		case "message_end":
			messageEnd = true
			return !workflow || workflowDone, nil
		}
		return false, nil
	})
	return streamResult(text.String(), err)
}

func decodeCoze(body io.Reader, emit func(string)) (Reply, error) {
	var text strings.Builder
	seen := map[string]bool{}
	err := readExternalFrames(body, nil, func(frame externalFrame) (bool, error) {
		switch frame.name {
		case "conversation.chat.completed":
			return true, nil
		case "error", "conversation.chat.failed", "conversation.chat.requires_action", "conversation.chat.canceled", "done":
			return false, errExternalReply
		case "conversation.message.delta", "conversation.message.completed":
			var event struct {
				ID, Role, Type, Content string
				ContentType             string `json:"content_type"`
			}
			if json.Unmarshal([]byte(frame.data), &event) != nil {
				return false, errExternalReply
			}
			if event.Role != "assistant" || event.Type != "answer" || event.ContentType != "text" {
				return false, nil
			}
			if frame.name == "conversation.message.completed" && seen[event.ID] {
				return false, nil
			}
			seen[event.ID] = true
			return false, appendExternalText(&text, event.Content, emit)
		}
		return false, nil
	})
	return streamResult(text.String(), err)
}

func decodeLangGraph(body io.Reader, emit func(string)) (Reply, error) {
	var text string
	err := readExternalFrames(body, func() bool { return text != "" }, func(frame externalFrame) (bool, error) {
		switch frame.name {
		case "error", "interrupt":
			return false, errExternalReply
		case "end":
			return true, nil
		case "values":
			var event struct {
				Interrupt json.RawMessage `json:"__interrupt__"`
				Messages  []struct {
					Type, Role string
					Content    json.RawMessage
				}
			}
			if json.Unmarshal([]byte(frame.data), &event) != nil ||
				(len(event.Interrupt) != 0 && string(event.Interrupt) != "[]" && string(event.Interrupt) != "null") {
				return false, errExternalReply
			}
			for i := len(event.Messages) - 1; i >= 0; i-- {
				message := event.Messages[i]
				if message.Type != "ai" && message.Role != "assistant" {
					continue
				}
				var content string
				if json.Unmarshal(message.Content, &content) != nil || !validExternalText(content) {
					text = ""
					break
				}
				if emit != nil && strings.HasPrefix(content, text) {
					if delta := strings.TrimPrefix(content, text); delta != "" {
						emit(delta)
					}
				}
				text = content
				break
			}
		}
		return false, nil
	})
	return streamResult(text, err)
}
