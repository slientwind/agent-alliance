package runtime

import (
	"strings"
	"testing"

	"alliance/internal/knowledge"
)

func TestRequirementEvidenceDoesNotAdoptUnsupportedAmounts(t *testing.T) {
	const text = "总价为人民币120元。"
	d := contextData{
		lookupPhase:  "answer",
		lookupFiles:  []knowledge.InputSource{{ID: "source", Version: 3, Text: text}},
		hits:         []lookupHit{{version: 3, end: len([]rune(text)), text: text}},
		requirements: []evidenceRequirement{{1, "总价"}, {2, "有效期"}, {3, "税额"}},
	}
	decision, err := (Activities{}).interpretLookupReply(TextReplyRef{}, d, checkpointedReply{Text: `{
        "answer":"税额为999元。","references":[1],"items":[
          {"item":1,"answer":"总价为人民币120元。","references":[1]},
          {"item":2,"gap":"请补有效期。"},{"item":3,"answer":"税额为999元。","references":[1]}]}`}, requirementEvidenceReply, true)
	if err != nil || len(decision.findings) != 2 || len(decision.findings[0].Citations) != 1 ||
		decision.findings[1].Gap == "" || !strings.Contains(decision.text, "税额：待人工核对") ||
		strings.Contains(decision.text, "999") {
		t.Fatalf("invalid claim was adopted: %+v, %v", decision, err)
	}
	truncated, err := (Activities{}).interpretLookupReply(TextReplyRef{}, d, checkpointedReply{
		Text: `{"items":[{"item":2,"gap":"请补有效期。"}]}`, FinishReason: "length",
	}, requirementEvidenceReply, true)
	if err != nil || len(truncated.findings) != 0 || truncated.outcome != "lookup_unverified" {
		t.Fatalf("truncated envelope was accepted: %+v, %v", truncated, err)
	}
	legacy, err := (Activities{}).interpretLookupReply(TextReplyRef{}, d, checkpointedReply{
		Text: `{"answer":"总价为人民币120元。","references":[1]}`,
	}, requirementEvidenceReply, true)
	if err != nil || legacy.outcome != "lookup_answer" || len(legacy.findings) != 0 {
		t.Fatalf("historical reply did not keep its interpretation: %+v, %v", legacy, err)
	}
}
