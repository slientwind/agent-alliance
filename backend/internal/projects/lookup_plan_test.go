package projects

import (
	"reflect"
	"testing"
)

func TestEvidenceFollowUpExpansion(t *testing.T) {
	in := PlanInput{
		Agents: []string{"research", "document"}, AgentRevisions: []int{1, 1},
		LookupSteps: []int{1}, FollowUpSteps: []int{1}, Sources: []SourcePin{{ID: "document", Version: 1}},
	}
	nodes, err := executionNodes(in)
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	var edges [][]int
	for _, node := range nodes {
		phases = append(phases, node.phase)
		edges = append(edges, node.dependencies)
	}
	if !reflect.DeepEqual(phases, []string{"search", "answer", "synthesize", ""}) ||
		!reflect.DeepEqual(edges, [][]int{nil, {1}, {2}, {3}}) {
		t.Fatalf("follow-up should precede dependent deliverables: %v %v", phases, edges)
	}
	for _, candidate := range []PlanInput{
		{Agents: in.Agents, AgentRevisions: in.AgentRevisions, Sources: in.Sources, FollowUpSteps: []int{1}},
		{Agents: in.Agents, AgentRevisions: in.AgentRevisions, Sources: in.Sources, LookupSteps: []int{1}, CalculationSteps: []int{1}, FollowUpSteps: []int{1}},
	} {
		if _, err := executionNodes(candidate); err == nil {
			t.Fatal("allowed follow-up for a step without supported source answering")
		}
	}
}

func TestEvidenceReviewBudgetsBothRoundsAndMapsTheFinalOutput(t *testing.T) {
	in := PlanInput{
		Agents: []string{"research", "document"}, AgentRevisions: []int{2, 1},
		LookupSteps: []int{1}, FollowUpSteps: []int{1}, EvidenceReviewSteps: []int{1},
		Sources: []SourcePin{{ID: "source", Version: 3}},
	}
	nodes, err := executionNodes(in)
	if err != nil || len(nodes) != 5 || nodes[2].phase != "review_evidence" || nodes[3].phase != "synthesize" ||
		*nodes[2].revision != 2 || !reflect.DeepEqual(nodes[2].dependencies, []int{2}) ||
		!reflect.DeepEqual(nodes[3].dependencies, []int{3}) || !reflect.DeepEqual(nodes[4].dependencies, []int{4}) ||
		!reflect.DeepEqual(deliveryOrdinals(nodes), []int{4, 5}) {
		t.Fatalf("unexpected bounded review plan: %+v %v", nodes, err)
	}
	for _, invalid := range []PlanInput{
		{Agents: in.Agents, AgentRevisions: in.AgentRevisions, Sources: in.Sources, LookupSteps: []int{1}, EvidenceReviewSteps: []int{1}},
		{Agents: in.Agents, AgentRevisions: in.AgentRevisions, Sources: in.Sources, LookupSteps: []int{1}, FollowUpSteps: []int{1}, EvidenceReviewSteps: []int{1, 1}},
		{Agents: make([]string, 10), AgentRevisions: []int{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, Sources: in.Sources, LookupSteps: []int{1}, FollowUpSteps: []int{1}, EvidenceReviewSteps: []int{1}},
	} {
		if _, err := executionNodes(invalid); err == nil {
			t.Fatalf("accepted review outside the fixed plan: %+v", invalid)
		}
	}
}
