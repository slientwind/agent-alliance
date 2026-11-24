package harness

import "testing"

func TestDecodeRequirementEvidence(t *testing.T) {
	items := map[int]string{2: "报价总价", 5: "报价有效期", 6: "供应商资质"}
	valid := `{"items":[{"item":2,"answer":"总价已给出。","references":[1]},{"item":5,"gap":"请补乙方书面报价。"}]}`
	reply, err := DecodeRequirementEvidence(valid, items, 2)
	if err != nil || len(reply.Items) != 2 || reply.Items[0].References[0] != 1 || reply.Items[1].Gap == "" {
		t.Fatalf("structured reply: %+v %v", reply, err)
	}
	for _, raw := range []string{
		`{"items":[{"item":1,"gap":"未知项"}]}`,
		`{"items":[{"item":2,"item":5,"gap":"重复字段"}]}`,
		`{"items":[{"item":2,"gap":"重复项"},{"item":2,"gap":"重复项"}]}`,
		`{"items":[{"item":2,"answer":"越界","references":[3]}]}`,
		`{"items":[{"item":2,"answer":"重复引用","references":[1,1]}]}`,
		`{"items":[{"item":2,"gap":"缺资料","references":null}]}`,
		`{"items":[{"item":2,"gap":"缺资料"}],"question":"不允许混入动作"}`,
		`{"items":[{"item":2,"gap":"缺资料"}],"answer":"顶层也必须附编号"}`,
		`{"items":[{"item":2,"gap":"截断"`,
	} {
		if _, err := DecodeRequirementEvidence(raw, items, 2); err == nil {
			t.Errorf("invalid structured reply accepted: %s", raw)
		}
	}
}
