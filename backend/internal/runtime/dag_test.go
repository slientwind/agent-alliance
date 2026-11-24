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
	return env, a
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
		env.SignalWorkflow("control", remoteReceiptSignal(2))
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

func TestDAGEnablesStepRemoteInputBeforeDispatch(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a Activities
	env.RegisterActivity(a.Topology)
	env.RegisterActivity(a.EnableStepRemoteInput)
	env.RegisterActivity(a.ExecuteStep)
	env.OnActivity(a.Topology, mock.Anything, "run").Return([]Node{{Ordinal: 1}, {Ordinal: 2}}, nil).Once()
	enabled := env.OnActivity(a.EnableStepRemoteInput, mock.Anything, "run").Return(nil).Once()
	for ordinal := 1; ordinal <= 2; ordinal++ {
		env.OnActivity(a.ExecuteStep, mock.Anything, Step{"run", ordinal}).NotBefore(enabled).Return(Result{}, nil).Once()
	}
	env.ExecuteWorkflow(Collaborate, "run")
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	env.AssertExpectations(t)
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
