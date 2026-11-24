package runtime

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func dagTestEnvironment(nodes []Node) (*testsuite.TestWorkflowEnvironment, Activities) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a Activities
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.ExecuteStep)
	env.OnActivity(a.Topology, mock.Anything, "run").Return(nodes, nil).Once()
	env.OnGetVersion("step-remote-input", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion).Maybe()
	env.OnGetVersion("step-source-receipts", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion).Maybe()
	env.OnGetVersion("step-dispatch-waits", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion).Maybe()
	return env, a
}

func TestDAGTextCheckpointVersionAndDependencies(t *testing.T) {
	for _, version := range []workflow.Version{workflow.DefaultVersion, 1} {
		mode := "recorded replies"
		if version == workflow.DefaultVersion {
			mode = "existing workflow history"
		}
		t.Run(mode, func(t *testing.T) {
			first := Step{"run", 1}
			env, a := dagTestEnvironment([]Node{{Ordinal: 1, CheckpointText: true}, {Ordinal: 2, Dependencies: []int{1}}})
			env.OnGetVersion("text-reply-checkpoints", workflow.DefaultVersion, 1).Return(version).Once()
			ready := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).Return(Result{}, nil).Once()
			if version == workflow.DefaultVersion {
				ready.NotBefore(env.OnActivity(a.ExecuteStep, mock.Anything, first).Return(Result{}, nil).Once())
			} else {
				env.RegisterActivity(a.AdvanceTextStep)
				env.RegisterActivity(a.CommitTextReply)
				ref := TextReplyRef{Step: first, Attempt: 1}
				saved := env.OnActivity(a.AdvanceTextStep, mock.Anything, first).Return(Result{TextReply: &ref}, nil).Once()
				committed := env.OnActivity(a.CommitTextReply, mock.Anything, ref).NotBefore(saved).Return(Result{}, nil).Once()
				ready.NotBefore(committed)
			}
			env.ExecuteWorkflow(Collaborate, "run")
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			env.AssertExpectations(t)
		})
	}
}

func TestDAGEvidenceQuestionActivityVersion(t *testing.T) {
	for _, version := range []workflow.Version{workflow.DefaultVersion, 1} {
		env, a := dagTestEnvironment([]Node{{Ordinal: 1, CheckpointEvidence: true, EvidenceQuestions: true},
			{Ordinal: 2, Dependencies: []int{1}, CheckpointEvidence: true, EvidenceQuestions: true}})
		env.OnGetVersion("evidence-question-handoffs", workflow.DefaultVersion, 1).Return(version).Once()
		advance, commit := a.AdvanceEvidenceStep, a.CommitEvidenceReply
		if version != workflow.DefaultVersion {
			advance, commit = a.AdvanceEvidenceQuestion, a.CommitEvidenceQuestion
		}
		env.RegisterActivity(advance)
		env.RegisterActivity(commit)
		first := Step{"run", 1}
		ref := TextReplyRef{Step: first, Attempt: 1}
		recorded := env.OnActivity(advance, mock.Anything, first).Return(Result{TextReply: &ref}, nil).Once()
		adopted := env.OnActivity(commit, mock.Anything, ref).NotBefore(recorded).Return(Result{}, nil).Once()
		env.OnActivity(advance, mock.Anything, Step{"run", 2}).NotBefore(adopted).Return(Result{}, nil).Once()
		env.ExecuteWorkflow(Collaborate, "run")
		if err := env.GetWorkflowError(); err != nil {
			t.Fatalf("workflow version %d: %v", version, err)
		}
		env.AssertExpectations(t)
	}
}

func TestDAGStartsReadySuccessorBeforeUnrelatedActivityFinishes(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "independent scheduling"
		if legacy {
			name = "existing workflow history"
		}
		t.Run(name, func(t *testing.T) {
			env, a := dagTestEnvironment([]Node{{Ordinal: 1}, {Ordinal: 2}, {Ordinal: 3, Dependencies: []int{1}}, {Ordinal: 4, Dependencies: []int{2, 3}}})
			if legacy {
				env.OnGetVersion("independent-dag-dispatch", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion)
			}
			var ranBeforeSlow, slowDone atomic.Bool
			fast := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).Return(Result{}, nil).Once()
			slow := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).After(20*time.Second).
				Run(func(mock.Arguments) { slowDone.Store(true) }).Return(Result{}, nil).Once()
			dependent := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 3}).NotBefore(fast).
				Run(func(mock.Arguments) { ranBeforeSlow.Store(!slowDone.Load()) }).Return(Result{}, nil).Once()
			env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 4}).NotBefore(dependent, slow).Return(Result{}, nil).Once()
			env.ExecuteWorkflow(Collaborate, "run")
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			env.AssertExpectations(t)
			if ranBeforeSlow.Load() == legacy {
				t.Fatalf("unexpected dispatch ordering: successor before slow sibling=%t", ranBeforeSlow.Load())
			}
		})
	}
}

func TestDAGAllowsIndependentProgressDuringRemoteWait(t *testing.T) {
	env, a := dagTestEnvironment([]Node{{Ordinal: 1}, {Ordinal: 2}, {Ordinal: 3, Dependencies: []int{2}}, {Ordinal: 4, Dependencies: []int{1, 3}}})
	var downstreamReady atomic.Bool
	remote := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).Return(Result{Wait: true}, nil).Once()
	independent := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).Return(Result{}, nil).Once()
	independentSuccessor := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 3}).NotBefore(independent).
		Run(func(mock.Arguments) { downstreamReady.Store(true) }).Return(Result{}, nil).Once()
	resumed := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).NotBefore(remote, independentSuccessor).Return(Result{}, nil).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 4}).NotBefore(resumed, independentSuccessor).Return(Result{}, nil).Once()
	env.RegisterDelayedCallback(func() {
		if !downstreamReady.Load() {
			t.Error("independent branch waited for the remote receipt")
		}
		env.SignalWorkflow("control", remoteReceiptSignal(1))
	}, 15*time.Second)
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}

func TestDAGRetainsAnEarlyReceiptAndWakesOnlyItsBranch(t *testing.T) {
	env, a := dagTestEnvironment([]Node{{Ordinal: 1}, {Ordinal: 2}})
	var firstResumed, secondSignalled atomic.Bool
	first := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).After(5*time.Second).Return(Result{Wait: true}, nil).Once()
	second := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).Return(Result{Wait: true}, nil).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).NotBefore(first).
		Run(func(mock.Arguments) { firstResumed.Store(true) }).Return(Result{}, nil).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).NotBefore(second).
		Run(func(mock.Arguments) {
			if !secondSignalled.Load() {
				t.Error("receipt for step one woke step two")
			}
		}).Return(Result{}, nil).Once()
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("control", remoteReceiptSignal(1)) }, time.Second)
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("control", remoteReceiptSignal(1)) }, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		if !firstResumed.Load() {
			t.Error("receipt arriving before Wait was lost")
		}
		secondSignalled.Store(true)
		env.SignalWorkflow("control", sourceReceiptPrefix+"2")
	}, 15*time.Second)
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}

func TestDAGRunControlWakesAllWaitingBranches(t *testing.T) {
	env, a := dagTestEnvironment([]Node{{Ordinal: 1}, {Ordinal: 2}})
	for ordinal := 1; ordinal <= 2; ordinal++ {
		step := Step{"run", ordinal}
		waiting := env.OnActivity(a.ExecuteStep, mock.Anything, step).Return(Result{Wait: true}, nil).Once()
		env.OnActivity(a.ExecuteStep, mock.Anything, step).NotBefore(waiting).Return(Result{}, nil).Once()
	}
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("control", "confirmed-run-resume") }, time.Second)
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}

func TestDAGEnablesStepWaitsBeforeDispatch(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a Activities
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.EnableStepRemoteInput)
	env.RegisterActivity(a.EnableStepSourceReceipts)
	env.RegisterActivity(a.EnableStepDispatchWaits)
	env.RegisterActivity(a.ExecuteStep)
	env.OnActivity(a.Topology, mock.Anything, "run").Return([]Node{{Ordinal: 1}, {Ordinal: 2}}, nil).Once()
	enabled := env.OnActivity(a.EnableStepRemoteInput, mock.Anything, "run").Return(nil).Once()
	receipts := env.OnActivity(a.EnableStepSourceReceipts, mock.Anything, "run").NotBefore(enabled).Return(nil).Once()
	dispatch := env.OnActivity(a.EnableStepDispatchWaits, mock.Anything, "run").NotBefore(receipts).Return(nil).Once()
	for ordinal := 1; ordinal <= 2; ordinal++ {
		env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", ordinal}).NotBefore(dispatch).Return(Result{}, nil).Once()
	}
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
}

func TestDAGToolChoiceVersionAndBranchWake(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "new workflow"
		if legacy {
			name = "recorded workflow history"
		}
		t.Run(name, func(t *testing.T) {
			env, a := dagTestEnvironment([]Node{
				{Ordinal: 1}, {Ordinal: 2, Dependencies: []int{1}, ToolChoice: true},
				{Ordinal: 3, Dependencies: []int{1}, ToolChoice: true}, {Ordinal: 4, Dependencies: []int{1}},
				{Ordinal: 5, Dependencies: []int{2, 3, 4}},
			})
			first := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).Return(Result{}, nil).Once()
			if legacy {
				env.OnGetVersion("step-tool-choices", workflow.DefaultVersion, 1).Return(workflow.DefaultVersion).Once()
			} else {
				env.RegisterActivity(a.EnableStepToolChoices)
				enabled := env.OnActivity(a.EnableStepToolChoices, mock.Anything, "run").Return(nil).Once()
				first.NotBefore(enabled)
			}
			var independent, resumedFirst, secondSignalled atomic.Bool
			waitA := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).Return(Result{Wait: true}, nil).Once()
			waitB := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 3}).Return(Result{Wait: true}, nil).Once()
			doneA := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).NotBefore(waitA).
				Run(func(mock.Arguments) { resumedFirst.Store(true) }).Return(Result{}, nil).Once()
			doneB := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 3}).NotBefore(waitB).
				Run(func(mock.Arguments) {
					if !secondSignalled.Load() {
						t.Error("confirming the first tool woke the second")
					}
				}).Return(Result{}, nil).Once()
			doneC := env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 4}).
				Run(func(mock.Arguments) { independent.Store(true) }).Return(Result{}, nil).Once()
			env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 5}).NotBefore(doneA, doneB, doneC).Return(Result{}, nil).Once()
			env.RegisterDelayedCallback(func() {
				if !independent.Load() {
					t.Error("independent work did not finish before confirmation")
				}
				env.SignalWorkflow("control", sourceReceiptPrefix+"2")
			}, time.Second)
			env.RegisterDelayedCallback(func() {
				if !resumedFirst.Load() {
					t.Error("the first confirmation did not resume its step")
				}
				secondSignalled.Store(true)
				env.SignalWorkflow("control", sourceReceiptPrefix+"3")
			}, 3*time.Second)
			env.ExecuteWorkflow(Collaborate, "run")
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			env.AssertExpectations(t)
		})
	}
}

func TestDAGDrainsDispatchedActivitiesOnStop(t *testing.T) {
	env, a := dagTestEnvironment([]Node{{Ordinal: 1}, {Ordinal: 2}, {Ordinal: 3, Dependencies: []int{2}}})
	var receiptReturned atomic.Bool
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 1}).After(time.Second).Return(Result{Stop: true}, nil).Once()
	env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", 2}).After(10*time.Second).
		Run(func(mock.Arguments) { receiptReturned.Store(true) }).Return(Result{}, nil).Once()
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
	if !receiptReturned.Load() {
		t.Fatal("stop abandoned a running activity")
	}
}
