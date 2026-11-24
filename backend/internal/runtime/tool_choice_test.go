package runtime

import (
	"strings"
	"testing"

	"alliance/internal/harness"
	"alliance/internal/knowledge"
)

func TestReportChoiceProjectsCSVHeadersInOrder(t *testing.T) {
	c := toolChoice{options: harness.ReportOptions{{Name: "整理", Revision: 2, Group: "组别", Amount: "金额"}}}
	files := []knowledge.InputSource{{Name: "a.CSV", Text: "\ufeff组别,\"金额,元\"\n内部数据,100\n"}, {Name: "note.txt", Text: "资料正文"}, {Name: "bad.csv", Text: "\"unterminated"}, {Name: "b.csv", Text: "组别,币种\n采购,元"}}
	const want = `{"Goal":"比较","Headers":[{"Name":"a.CSV","Columns":["组别","金额,元"]},{"Name":"b.csv","Columns":["组别","币种"]}],"Candidates":[{"Name":"整理","Revision":2,"Group":"组别","Amount":"金额","FilterColumn":"","FilterValue":"","UnitColumn":""}]}`
	if got := c.input("比较", files); got != want {
		t.Fatalf("unexpected model input:\n%s", got)
	}
}

func TestOfficeChoiceKeepsContentOutOfTheDecision(t *testing.T) {
	c := toolChoice{profile: "docx", office: harness.OfficeOptions{
		{Name: "对账简报", Revision: 1, Title: "内部核对"},
		{Name: "复核简报", Revision: 3, Title: "复核记录"},
	}}
	input := c.input("制作复核简报", []knowledge.InputSource{{Name: "核对原件.docx", Version: 2, Text: "不向选择步骤发送的正文"}})
	if strings.Contains(input, "不向选择步骤") || !strings.Contains(input, `"Name":"核对原件.docx","Version":2`) ||
		!strings.Contains(input, `"Name":"复核简报","Revision":3,"Title":"复核记录"`) {
		t.Fatalf("unexpected routing input %s", input)
	}
	for _, tc := range []struct {
		reply, reason string
		want          int
	}{
		{`{ "choice": 1 }`, "stop", 1},
		{`{"choice":2}`, "", 2},
		{`{"choice":2}`, "length", 0},
		{`{"choice":2}`, "tool_calls", 0},
		{`{"choice":2,"params":{"title":"改名"}}`, "stop", 0},
		{`{"choice":1,"choice":2}`, "stop", 0},
		{`{"choice":2}{"choice":1}`, "stop", 0},
		{`{"choice":3}`, "stop", 0},
	} {
		if picked, _ := c.accept(tc.reply, tc.reason); picked != tc.want {
			t.Errorf("accept(%q, %q) = %d, want %d", tc.reply, tc.reason, picked, tc.want)
		}
	}
}
