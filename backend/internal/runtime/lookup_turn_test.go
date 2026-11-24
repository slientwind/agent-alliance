package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestLookupTurnSealsBoundTermsAndOnlyReopensMatchingEvidence(t *testing.T) {
	vault, err := resources.NewVault(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	a := Activities{Catalog: resources.Catalog{Vault: vault}}
	files := []knowledge.InputSource{{ID: "quote", Version: 3, Text: "甲乙丙丁", ReceiptStep: 2, ExcerptStart: 1, ExcerptEnd: 3}}
	data := contextData{
		lookupFiles: files,
		hits:        []lookupHit{{source: 0, version: 3, start: 1, end: 3, text: "乙丙"}},
	}
	step := Step{"run", 2}
	stored, err := a.prepareLookupTurn(step, 1, data, `{"terms":[" 乙丙 ","乙丙"]}`)
	if err != nil || len(stored) == 0 {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := vault.Open(stored, (lookupAction{step, 2}).scope()); err == nil {
		t.Fatal("sealed action was readable with a different attempt")
	}
	raw, err := vault.Open(stored, (lookupAction{step, 1}).scope())
	var input lookupTurnInput
	if err != nil || json.Unmarshal([]byte(raw), &input) != nil || len(input.Terms) != 1 {
		t.Fatalf("failed to restore decision: %v, %+v", err, input)
	}
	if hits, ok := reopenLookupTurn(input.Citations, files); !ok || hits[0].text != "乙丙" {
		t.Fatal("matching citation did not reopen")
	}
	for _, change := range []func(*knowledge.InputSource){
		func(file *knowledge.InputSource) { file.Version++ },
		func(file *knowledge.InputSource) { file.Text = "甲己丙丁" },
		func(file *knowledge.InputSource) { file.ExcerptStart++ },
	} {
		file := files[0]
		change(&file)
		if _, ok := reopenLookupTurn(input.Citations, []knowledge.InputSource{file}); ok {
			t.Fatalf("accepted changed evidence: %+v", file)
		}
	}
}

func TestLookupTurnWorkflowSchedulesLocalEffectBeforeDependents(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a Activities
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.AdvanceTurn)
	env.RegisterActivity(a.ExecuteLookupAction)
	env.RegisterActivity(a.ExecuteStep)
	env.OnGetVersion("durable-local-actions", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	env.OnActivity(a.Topology, mock.Anything, "run").Return([]Node{
		{Ordinal: 1, LookupAnswer: true}, {Ordinal: 2, Dependencies: []int{1}},
	}, nil).Once()
	decision := env.OnActivity(a.AdvanceTurn, mock.Anything, Step{"run", 1}).Return(Result{LookupAttempt: 1}, nil).Once()
	effect := env.OnActivity(a.ExecuteLookupAction, mock.Anything, lookupAction{Step{"run", 1}, 1}).
		Return(Result{}, nil).NotBefore(decision).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).Return(Result{}, nil).NotBefore(effect).Once()
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}

func TestLookupTurnWorkflowKeepsOldHistoryActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a Activities
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.ExecuteStep)
	env.OnGetVersion("read-only-lookup-turns", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	env.OnGetVersion("durable-local-actions", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
	env.OnActivity(a.Topology, mock.Anything, "run").Return([]Node{{Ordinal: 1, LookupAnswer: true}}, nil).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).Return(func(context.Context, Step) (Result, error) {
		return Result{}, nil
	}).Once()
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}

func TestDurableLocalActionRevisitsStaleControlBeforeContinuing(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a Activities
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.AdvanceExecutionTurn)
	env.RegisterActivity(a.ExecuteAction)
	env.RegisterActivity(a.ExecuteStep)
	env.OnActivity(a.Topology, mock.Anything, "run").Return([]Node{
		{Ordinal: 1, Calculation: true}, {Ordinal: 2, Dependencies: []int{1}},
	}, nil).Once()
	step := Step{"run", 1}
	old := ActionRef{Step: step, Attempt: 1, Kind: calculateAction, ControlRevision: 2}
	current := ActionRef{Step: step, Attempt: 1, Kind: calculateAction, ControlRevision: 3}
	first := env.OnActivity(a.AdvanceExecutionTurn, mock.Anything, step).Return(Result{Action: &old}, nil).Once()
	stale := env.OnActivity(a.ExecuteAction, mock.Anything, old).Return(Result{RevisitTurn: true}, nil).NotBefore(first).Once()
	resumed := env.OnActivity(a.AdvanceExecutionTurn, mock.Anything, step).Return(Result{Action: &current}, nil).NotBefore(stale).Once()
	done := env.OnActivity(a.ExecuteAction, mock.Anything, current).Return(Result{}, nil).NotBefore(resumed).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).Return(Result{}, nil).NotBefore(done).Once()
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}
