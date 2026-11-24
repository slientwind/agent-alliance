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
	reply, err := decodeTextStream(strings.NewReader(wire), func(chunk string) { displayed.WriteString(chunk) })
	if err != nil || reply.Text != "甲乙" || displayed.String() != reply.Text || reply.Tokens != 37 || reply.FinishReason != "stop" {
		t.Fatalf("reply=%+v draft=%q err=%v", reply, displayed.String(), err)
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
		reply, err := decodeTextStream(strings.NewReader(text+suffix), func(chunk string) { displayed.WriteString(chunk) })
		if err == nil || reply.Text != "" || displayed.String() != "草稿" {
			t.Errorf("suffix=%q reply=%+v draft=%q err=%v", suffix, reply, displayed.String(), err)
		}
	}
}
