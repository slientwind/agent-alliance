package runtime

import (
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
