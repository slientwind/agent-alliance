package runtime

import (
	"alliance/internal/knowledge"
	"strings"
	"testing"
)

func TestReadOnlyLookupRefinementUsesNewVerifiedLocations(t *testing.T) {
	files := []knowledge.InputSource{
		{ID: "quote", Name: "报价.md", Version: 2, Text: "报价表仅用于采购。\n另见质保单。"},
		{ID: "warranty", Name: "质保单.md", Version: 4, Text: "质保单必须在周五前签收。"},
	}
	prior, _ := searchForGoal(`{"terms":["报价表"]}`, files, "", nil)
	hits := verifiedHits(prior, files)
	if len(hits) != 1 {
		t.Fatalf("wrong initial evidence: %q", prior)
	}
	body, outcome := groundedAnswer(`{"terms":["报价表","质保单"]}`, hits, files)
	if outcome != "lookup_refined" || !strings.Contains(body, "周五前签收") ||
		strings.Contains(body, hits[0].line()) {
		t.Fatalf("refinement did not give exclusively new evidence: %s %q", outcome, body)
	}
}

func TestReadOnlyLookupStopsAndRetainsExistingEvidence(t *testing.T) {
	files := []knowledge.InputSource{{ID: "a", Name: "培训.xlsx", Version: 3, Text: "培训签到截止时间为周五。"}}
	prior, _ := searchForGoal(`{"terms":["签到"]}`, files, "", nil)
	hits := verifiedHits(prior, files)
	if len(hits) != 1 {
		t.Fatalf("wrong initial evidence: %q", prior)
	}
	for _, tc := range []struct{ raw, want string }{
		{`{"terms":["签到"]}`, "lookup_no_progress"},
		{`{"terms":["费用"]}`, "lookup_no_progress"},
		{`{"terms":["签到"]`, "lookup_unverified"},
		{`{"answer":"周一。","references":[2]}`, "lookup_unverified"},
	} {
		body, code := groundedAnswer(tc.raw, hits, files)
		if code != tc.want || !strings.Contains(body, hits[0].line()) {
			t.Errorf("groundedAnswer(%q) = %s, %q; want %s with verified evidence", tc.raw, code, body, tc.want)
		}
	}
}
