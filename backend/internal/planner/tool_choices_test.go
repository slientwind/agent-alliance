package planner

import (
	"slices"
	"testing"
)

func TestOfferToolChoiceRequiresCompatibleProjectScope(t *testing.T) {
	text := Candidate{ID: "choose", ProjectTeam: true, Operation: "model-text", Resource: "model-a"}
	regions := Candidate{ID: "region", ProjectTeam: true, Operation: "aggregate", Aggregation: &aggregateScope{Group: "地区", Amount: "净额", UnitColumn: "币种", FilterColumn: "状态", FilterValue: "已审核"}}
	departments := regions
	departments.ID = "department"
	scope := *regions.Aggregation
	scope.Group = "部门"
	departments.Aggregation = &scope

	profile := inputProfile{Count: 1, CSV: 1}
	offers := offerToolChoices([]Candidate{text, regions, departments}, profile, nil, "model-a")
	if len(offers) != 1 || offers[0].alternative.ID != departments.ID {
		t.Fatalf("expected one compatible proposal, got %+v", offers)
	}
	for _, change := range []struct {
		name string
		edit func(*Candidate)
	}{
		{"different unit", func(c *Candidate) { c.Aggregation.UnitColumn = "结算币种" }},
		{"different population", func(c *Candidate) { c.Aggregation.FilterValue = "全部" }},
		{"different measure", func(c *Candidate) { c.Aggregation.Amount = "原始金额" }},
		{"same grouping", func(c *Candidate) { c.Aggregation.Group = "地区" }},
		{"different team", func(c *Candidate) { c.ProjectTeam = false }},
	} {
		t.Run(change.name, func(t *testing.T) {
			next := departments
			mutable := *next.Aggregation
			next.Aggregation = &mutable
			change.edit(&next)
			if got := offerToolChoices([]Candidate{text, regions, next}, profile, nil, "model-a"); len(got) != 0 {
				t.Fatalf("unexpected choices: %+v", got)
			}
		})
	}
}

func TestResolveToolChoicePreservesFollowingStepAndDeliverable(t *testing.T) {
	options := map[string]planningOption{
		"R1": {Route: &routingOption{
			selector:    Candidate{ID: "selector"},
			primary:     Candidate{ID: "primary", Aggregation: &aggregateScope{Group: "地区"}},
			alternative: Candidate{ID: "alternative", Revision: 2, Aggregation: &aggregateScope{Group: "部门"}},
		}},
		"A1":  {Agent: "primary"},
		"A2L": {Agent: "lookup", Lookup: true},
	}
	s := Suggestion{
		Kind: "plan", Agents: []string{"R1", "A1", "A2L"}, LookupSteps: []int{3},
		Drafts: []Blueprint{{After: 2}},
	}
	if !resolveActions(&s, options, "") || s.ToolChoice == nil || s.ToolChoice.Revision != 2 ||
		!slices.Equal(s.Agents, []string{"selector", "primary", "lookup"}) ||
		!slices.Equal(s.LookupSteps, []int{3}) || s.Drafts[0].After != 2 {
		t.Fatalf("unexpected expanded proposal: %+v", s)
	}
	routeOnly := Suggestion{
		Kind: "plan", Agents: []string{"selector", "primary"}, ToolChoice: s.ToolChoice, OutputFormats: []string{"text", "xlsx"},
	}
	if !normalizeOutputs(&routeOnly, nil) {
		t.Fatal("expected a supported XLSX and text proposal")
	}
	checkOutputs(&routeOnly, []Candidate{{ID: "selector", Operation: "model-text"}, {ID: "primary", Operation: "aggregate"}})
	if len(routeOnly.Missing) != 1 || routeOnly.OutputGaps[routeOnly.Missing[0]] != "text" {
		t.Fatalf("selector decision was counted as a text deliverable: %+v", routeOnly)
	}
}
