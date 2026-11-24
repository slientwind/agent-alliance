package harness

import "testing"

func TestDecodeEvidenceActionsChecksEveryOperation(t *testing.T) {
	sources := []bool{true, false, true}
	quotes := []bool{true, false}
	actions, err := DecodeEvidenceActions(`{"inspectEvidence":[{"source":3,"terms":["  POST-42 "]},{"reference":1,"side":"after"},{"source":1,"terms":["POST-42"]}]}`, sources, quotes)
	if err != nil || len(actions) != 3 || actions[0].Terms[0] != "post-42" || actions[1].Reference != 1 || actions[2].Source != 1 {
		t.Fatalf("mixed decision: %+v %v", actions, err)
	}
	for _, reply := range []string{
		`{"inspectEvidence":[{"source":2,"terms":["价格"]}]}`,
		`{"inspectEvidence":[{"source":4,"terms":["报价"]}]}`,
		`{"inspectEvidence":[{"reference":2,"side":"after"}]}`,
		`{"inspectEvidence":[{"source":1,"terms":["价格"]},{"reference":2,"side":"after"}]}`,
		`{"inspectEvidence":[{"source":1,"terms":[" 价格 ","价格"]}]}`,
		`{"inspectEvidence":[{"source":1,"terms":["A"]},{"source":1,"terms":["a"]}]}`,
		`{"inspectEvidence":[{"reference":1,"side":"after"},{"reference":1,"side":"after"}]}`,
		`{"inspectEvidence":[{"reference":1,"side":"across"}]}`,
		`{"inspectEvidence":[{"reference":1,"side":"after","end":994}]}`,
		`{"inspectEvidence":[{"source":1,"source":3,"terms":["销售"]}]}`,
		`{"inspectEvidence":[{"source":1,"terms":["a","b","c","d","e","f"]},{"source":3,"terms":["g"]}]}`,
		`{"inspectEvidence":[{"source":1,"terms":["负责\u200b人"]}]}`,
		`{"inspectEvidence":[{"reference":1,"side":"before"}],"answer":"已经完成"}`,
		`{"inspectEvidence":[{"reference":1,"side":"before"}]} {}`,
	} {
		if action, err := DecodeEvidenceActions(reply, sources, quotes); action != nil || err == nil {
			t.Errorf("unsafe decision accepted: %s → %+v %v", reply, action, err)
		}
	}
}
