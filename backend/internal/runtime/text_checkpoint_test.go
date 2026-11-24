package runtime

import "testing"

func TestCheckpointedReplyComplete(t *testing.T) {
	for _, tc := range []struct {
		name, text, reason string
		want               bool
	}{
		{"final", "核对完成", "stop", true},
		{"provider omits reason", "核对完成", "", true},
		{"truncated decision", `{"choice":2}`, "length", false},
		{"unfinished action", `{"choice":2}`, "tool_calls", false},
		{"filtered", "草稿", "content_filter", false},
		{"empty", "\n \t", "stop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := checkpointedReply{Text: tc.text, FinishReason: tc.reason}
			if got := reply.complete(); got != tc.want {
				t.Fatalf("complete = %v, want %v", got, tc.want)
			}
		})
	}
}
