package projects

import "testing"

func TestDeliveryRequirementsBindToBusinessStepOutput(t *testing.T) {
	nodes := []executionNode{{phase: "search"}, {phase: "answer_calc"}, {}}
	input := DeliveryContract{Items: []DeliveryRequirement{{1, " 合计金额 "}, {2, "原件清单"}}}
	items, err := normalizeDeliveryRequirements(input, nodes)
	if err != nil || len(items) != 2 || items[0] != (DeliveryRequirement{2, "合计金额"}) || items[1].Step != 3 {
		t.Fatalf("got %+v, %v", items, err)
	}
	for _, invalid := range []DeliveryContract{
		{AllowPartial: true},
		{Items: []DeliveryRequirement{{0, "原件清单"}}},
		{Items: []DeliveryRequirement{{1, "ABC"}, {1, " abc "}}},
	} {
		if _, err := normalizeDeliveryRequirements(invalid, nodes); err == nil {
			t.Fatalf("accepted invalid agreement: %+v", invalid)
		}
	}
}

func TestDeliveryCoverageRequiresExplicitPartialModeAndResponsible(t *testing.T) {
	covered := coverageItem{State: "covered", Artifact: "accepted"}
	missing := coverageItem{State: "missing", ResponsibleActive: true, ContinueWhen: "收到原件"}
	coverage := deliveryCoverage{Items: []coverageItem{covered, missing}}
	if err := coverage.authorize(true); err == nil {
		t.Fatal("default agreement authorized partial delivery")
	}
	coverage.AllowPartial = true
	if err := coverage.authorize(false); err == nil {
		t.Fatal("full delivery accepted a missing item")
	}
	if err := coverage.authorize(true); err != nil {
		t.Fatal(err)
	}
	coverage.Items[1].ResponsibleActive = false
	if err := coverage.authorize(true); err == nil {
		t.Fatal("partial delivery accepted an unavailable responsible member")
	}
	coverage.Items[1].State = "unchecked"
	if err := coverage.authorize(true); err == nil {
		t.Fatal("partial delivery accepted an unreviewed item")
	}
}
