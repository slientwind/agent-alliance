package harness

import "testing"

func TestReportChoiceOnlyAcceptsAnOfferedPosition(t *testing.T) {
	options := ReportOptions{{Name: "按地区", Revision: 4, Group: "地区", Amount: "核准额"}, {Name: "按部门", Revision: 8, Group: "部门", Amount: "核准额"}}
	for _, tc := range []struct {
		response      string
		offered, want int
	}{
		{" { \"choice\" : 2 } ", 2, 2},
		{`{"choice":1}`, 1, 1},
		{`{"choice":2}`, 1, 0},
		{`{"choice":1}`, 0, 0},
		{`{"choice":0}`, 2, 0},
		{`{"choice":3}`, 2, 0},
		{`{"choice":2,"choice":1}`, 2, 0},
		{`{"choice":2,"tool":"unlisted"}`, 2, 0},
		{`{"choice":"2"}`, 2, 0},
		{`{"choice":1`, 2, 0},
		{`{"choice":2} {"choice":1}`, 2, 0},
		{"```json\n{\"choice\":1}\n```", 2, 0},
	} {
		got, message := options[:tc.offered].Accept(tc.response)
		if got != tc.want {
			t.Errorf("Accept(%q) with %d candidates = %d, want %d", tc.response, tc.offered, got, tc.want)
		}
		if got == 2 && message != "选择：按部门 v8\n按「部门」分组，汇总「核准额」。" {
			t.Errorf("unexpected selected revision: %q", message)
		}
	}
}
