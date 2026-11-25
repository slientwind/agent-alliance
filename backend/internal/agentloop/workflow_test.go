package agentloop

import (
	"context"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestGeneralWorkflowLimitsToolCallsAndWaitsOnControl(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var actions, turns, waits int
	env.RegisterActivityWithOptions(func(_ context.Context, ref TurnRef) (TurnResult, error) {
		turns++
		if ref.StopReason != "" {
			return TurnResult{Done: true}, nil
		}
		return TurnResult{Tools: maxToolsPerTurn}, nil
	}, activity.RegisterOptions{Name: AdvanceTurnActivity})
	env.RegisterActivityWithOptions(func(_ context.Context, ref ToolRef) (ToolResult, error) {
		if ref.Turn == 1 && ref.Position == 1 && waits == 0 {
			waits++
			return ToolResult{Wait: true}, nil
		}
		actions++
		return ToolResult{}, nil
	}, activity.RegisterOptions{Name: ExecuteToolActivity})
	env.RegisterDelayedCallback(func() { env.SignalWorkflow("control", "continue") }, time.Minute)
	env.ExecuteWorkflow(GeneralAgentWorkflow, "unit-test")
	if err := env.GetWorkflowError(); err != nil || actions != maxActions || waits != 1 || turns != maxActions/maxToolsPerTurn+2 {
		t.Fatalf("actions=%d turns=%d waits=%d err=%v", actions, turns, waits, err)
	}
}

func TestGeneralWorkflowRechecksPendingToolWithoutSignal(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(_ context.Context, ref TurnRef) (TurnResult, error) {
		return TurnResult{Done: ref.Turn > 1, Tools: 1}, nil
	}, activity.RegisterOptions{Name: AdvanceTurnActivity})
	var tries int
	env.RegisterActivityWithOptions(func(_ context.Context, _ ToolRef) (ToolResult, error) {
		tries++
		return ToolResult{Wait: tries == 1, Recheck: tries == 1}, nil
	}, activity.RegisterOptions{Name: ExecuteToolActivity})
	start := env.Now()
	env.ExecuteWorkflow(GeneralAgentWorkflow, "pending")
	if err := env.GetWorkflowError(); err != nil || tries != 2 || env.Now().Sub(start) < time.Minute {
		t.Fatalf("pending approval attempts=%d time=%v err=%v", tries, env.Now().Sub(start), err)
	}
}
