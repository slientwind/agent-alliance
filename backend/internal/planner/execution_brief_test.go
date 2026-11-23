package planner

import "testing"

func TestExecutionBriefUsesAuthorizedRequest(t *testing.T) {
	const request = "提取维修单编号。\n最新报修 SRV-951。"
	const summary = "提取工单编号"
	for _, tc := range []struct {
		name, kind, input, text, wantText, wantBrief string
		hasFiles, gap                                bool
	}{
		{"plain plan", "plan", request, summary, summary, request + "\n\n执行方案：" + summary, false, false},
		{"missing text capability", "plan", request, summary, summary, request + "\n\n执行方案：" + summary, false, true},
		{"source capability proposal", "plan", request, summary, request, request, true, true},
		{"same wording", "plan", request, request, request, request, false, false},
		{"clarification", "question", request, summary, summary, "", false, false},
		{"simple answer", "answer", request, summary, summary, "", false, false},
		{"empty legacy request", "plan", "", summary, summary, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Suggestion{Kind: tc.kind, Text: tc.text, ExecutionBrief: "模型提供的未授权内容"}
			if tc.gap {
				s.Missing = []string{"工单提取"}
			}
			s.bindExecutionBrief(tc.input, tc.hasFiles)
			if s.Text != tc.wantText || s.ExecutionBrief != tc.wantBrief {
				t.Fatalf("text %q, execution %q", s.Text, s.ExecutionBrief)
			}
		})
	}
}
