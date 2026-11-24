package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func note(seq int64, kind, body string, recent bool) conversationMessage {
	return conversationMessage{seq: seq, kind: kind, body: body, bytes: len(body), recent: recent}
}

func TestPlanningContextKeepsLatestAndTracesExcerpts(t *testing.T) {
	history := []conversationMessage{
		note(1, "user", "原始目标：核对交付清单", false),
		note(9, "user", "原始约束：保留行号", false),
		note(11, "assistant", "需要确认范围。"+strings.Repeat("测试", 1000), true),
		note(14, "user", "最新要求：只看本周。", true),
	}
	const budget = 1200
	got, err := assemblePlanningContext(history, nil, budget, true)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Compacted || len(got.Brief) > budget || !utf8.ValidString(got.Brief) {
		t.Fatalf("invalid compacted prompt of %d bytes", len(got.Brief))
	}
	for _, part := range []string{history[0].body, history[1].body, "需要确认范围", excerptNotice, history[3].body} {
		if !strings.Contains(got.Brief, part) {
			t.Errorf("prompt missing %q", part)
		}
	}
	if strings.Contains(got.UserRequest, "需要确认范围") || !strings.HasSuffix(got.UserRequest, history[3].body) {
		t.Fatalf("invalid execution request: %q", got.UserRequest)
	}
	if len(got.used) != 4 || got.used[2].seq != 11 || len(got.used[2].body) >= len(history[2].body) {
		t.Fatalf("expected ordered references with recorded excerpt")
	}
}

func TestPlanningContextPreservesShortTranscriptAndRefusesPartialLatest(t *testing.T) {
	history := []conversationMessage{note(1, "user", "问一", true), note(2, "assistant", "答一", true), note(3, "user", "问二", true)}
	got, err := assemblePlanningContext(history, nil, 100, false)
	if err != nil || got.Compacted || got.Brief != "user: 问一\nassistant: 答一\nuser: 问二" || got.UserRequest != "问一\n问二" {
		t.Fatalf("short history changed: %+v %v", got, err)
	}
	if _, err := assemblePlanningContext(history, nil, 10, true); err == nil {
		t.Fatal("expected an error when the latest message cannot fit")
	}
}

func TestPlanningContextSeparatesRecalledBackground(t *testing.T) {
	early := "旧记录：PO-7125 的收货地址已有更正。"
	history := []conversationMessage{
		note(1, "user", "原始目标：核对到货", false),
		note(20, "assistant", strings.Repeat("旧页", 800), true),
		note(51, "user", "核对 PO-7125 的最终到货日期。", true),
	}
	span := conversationMessage{seq: 4, kind: "user", body: early, bytes: 700, start: 200, recalled: true}
	got, err := assemblePlanningContext(history, []conversationMessage{span}, 4200, true)
	if err != nil || len(got.Brief) > 4200 || !utf8.ValidString(got.Brief) ||
		!strings.Contains(got.Brief, recalledRole+": "+early+excerptNotice) || strings.Contains(got.UserRequest, early) ||
		!strings.HasSuffix(got.UserRequest, history[2].body) || got.used[1].start != span.start {
		t.Fatalf("wrong bounded planning context: %+v %v", got, err)
	}
}

func TestPlanningRecallTermsIncludeIdentifiersAndChinesePhrases(t *testing.T) {
	cues := planningRecallTerms("麻烦核对北湾合同续签的 PO-7125 和 PO-7125；请补充成本明细")
	if len(cues) < 3 || cues[0].weight != 3 || !strings.Contains(cues[0].pattern, "po-7125") {
		t.Fatalf("missing prioritized identifier: %v", cues)
	}
	count, chinese := 0, false
	for _, cue := range cues {
		if strings.Contains(cue.pattern, "po-7125") {
			count++
		}
		chinese = chinese || cue.pattern == "湾合同续"
	}
	if count != 1 || !chinese {
		t.Fatalf("missing phrases or duplicate terms: %v", cues)
	}
}
