package resources

import (
	"strings"
	"testing"
)

func TestDecodeTextStreamReadsOnlyAssistantTextAndUsage(t *testing.T) {
	wire := strings.Join([]string{
		": ping", "",
		`data: {"choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"hidden"}}]}`, "",
		`data: {"choices":[{"index":0,"delta":{"content":"甲"}}]}`, "",
		`data: {"choices":[{"index":1,"delta":{"content":"other"}},`,
		`data: {"index":0,"delta":{"content":"乙"}}]}`, "",
		`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, "",
		`data: {"choices":[],"usage":{"total_tokens":37}}`, "",
		"data: [DONE]", "", "",
	}, "\r\n")
	var displayed strings.Builder
	reply, err := decodeTextStream(strings.NewReader(wire), func(chunk string) { displayed.WriteString(chunk) }, false, nil)
	if err != nil || reply.Text != "甲乙" || displayed.String() != reply.Text || reply.Tokens != 37 || reply.FinishReason != "stop" || reply.ReasoningContent != "hidden" {
		t.Fatalf("reply=%+v draft=%q err=%v", reply, displayed.String(), err)
	}
}

func TestDecodeTextStreamReassemblesToolCalls(t *testing.T) {
	const frames = `data: {"choices":[{"index":0,"delta":{"reasoning_content":"private","tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"inspect","arguments":"{}"}},{"index":0,"id":"a","type":"function","function":{"name":"lookup","arguments":"{\"id\":"}}]}}]}

data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"7}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}}

data: [DONE]

`
	var display strings.Builder
	reply, err := decodeTextStream(strings.NewReader(frames), func(chunk string) { display.WriteString(chunk) }, true, nil)
	if err != nil || len(reply.ToolCalls) != 2 || reply.ToolCalls[0].ID != "a" || reply.ToolCalls[0].Function.Arguments != `{"id":7}` || reply.ToolCalls[1].ID != "b" {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
	if display.Len() != 0 || reply.ReasoningContent != "private" || reply.Tokens != 16 || reply.PromptTokens != 12 || reply.CompletionTokens != 4 || reply.CachedTokens != 3 {
		t.Fatalf("reply=%+v display=%q", reply, display.String())
	}
	if _, err := decodeTextStream(strings.NewReader(frames), nil, false, nil); err == nil {
		t.Fatal("accepted unrequested tool calls")
	}
}

func TestDecodeTextStreamDoesNotAcceptAnInterruptedDraft(t *testing.T) {
	const text = "data: {\"choices\":[{\"delta\":{\"content\":\"草稿\"}}]}\n\n"
	for _, suffix := range []string{
		"",
		"data: [DONE]\n\n",
		"data: {\"choices\":[{\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n",
		"data: {\"error\":{\"message\":\"aborted\"}}\n\n",
		"data: {\"choices\":[{\"delta\":{\"role\":\"tool\",\"content\":\"不能显示\"}}]}\n\n",
		"data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n",
	} {
		var displayed strings.Builder
		reply, err := decodeTextStream(strings.NewReader(text+suffix), func(chunk string) { displayed.WriteString(chunk) }, false, nil)
		if err == nil || reply.Text != "" || displayed.String() != "草稿" {
			t.Errorf("suffix=%q reply=%+v draft=%q err=%v", suffix, reply, displayed.String(), err)
		}
	}
}
