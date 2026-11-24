package runtime

import (
	"strings"
	"testing"

	"alliance/internal/harness"
	"alliance/internal/knowledge"
)

func TestFocusedEvidenceKeepsSourceAndExcerptBoundaries(t *testing.T) {
	const anchor = "甲报价含安装。"
	const details = "送货须提前三天预约。"
	const receipt = "乙说明补件有效。"
	files := []knowledge.InputSource{
		{ID: "a", Name: "甲报价", Version: 1, Text: anchor + "\n" + details + "\n有效期有待核对。"},
		{ID: "b", Name: "乙报价", Version: 2, Text: "有效期至下月初。"},
		{ID: "c", Name: "仅选中原句", Version: 3, Text: receipt + "这句不在获准的补件范围。", ReceiptStep: 1, ExcerptEnd: len([]rune(receipt))},
	}
	first := []lookupHit{
		{source: 0, version: 1, start: 0, end: len([]rune(anchor)), text: anchor},
		{source: 2, version: 3, start: 0, end: len([]rune(receipt)), text: receipt},
	}
	context := contextData{lookupFiles: files, hits: first}
	context.offerFocusedEvidence()
	if !context.focusedLookup || !strings.Contains(context.prompt, "S2 · 乙报价 v2") ||
		strings.Contains(context.prompt, "仅选中原句") || strings.Contains(context.prompt, details) ||
		!strings.Contains(context.prompt, "当前引文编号：1。") {
		t.Fatalf("unexpected model catalog: %s", context.prompt)
	}
	search := harness.EvidenceAction{Source: 2, Terms: []string{"有效期"}}
	observation, err := observeFocusedEvidence(search, first, first, files)
	if err != nil || len(observation.Refinement) != 1 || observation.Refinement[0].Source != "b" || observation.Outcome != "lookup_refined" {
		t.Fatalf("focused search: %+v %v", observation, err)
	}
	adjacent := harness.EvidenceAction{Reference: 1, Side: "after"}
	observation, err = observeFocusedEvidence(adjacent, first, first, files)
	if err != nil || len(observation.Refinement) != 1 || observation.Refinement[0].Source != "a" {
		t.Fatalf("adjacent quote: %+v %v", observation, err)
	}
	fresh, ok := reopenLookupTurn(observation.Refinement, files)
	if !ok || len(fresh) != 1 || fresh[0].text != details {
		t.Fatalf("wrong adjacent range: %+v", fresh)
	}
	observation, err = observeFocusedEvidence(adjacent, first, append(first, fresh...), files)
	if err != nil || observation.Outcome != "lookup_no_progress" || len(observation.Refinement) != 0 {
		t.Fatalf("repeated quote: %+v %v", observation, err)
	}
	if _, err := observeFocusedEvidence(harness.EvidenceAction{Reference: 2, Side: "after"}, first, first, files); err == nil {
		t.Fatal("read past the permitted receipt excerpt")
	}
	if _, err := observeFocusedEvidence(harness.EvidenceAction{Source: 3, Terms: []string{"原句"}}, first, first, files); err == nil {
		t.Fatal("searched a receipt as if it were an original")
	}
}
