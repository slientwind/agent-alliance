package agentloop

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WorkflowName        = "GeneralAgentWorkflow"
	AdvanceTurnActivity = "GeneralAgent.AdvanceTurn"
	ExecuteToolActivity = "GeneralAgent.ExecuteTool"
	maxModelTurns       = 8
	maxToolsPerTurn     = 4
	maxActions          = 24
)

type TurnRef struct {
	RunID      string
	Turn       int
	StopReason string
}

type ToolRef struct {
	RunID    string
	Turn     int
	Position int
}

type TurnResult struct {
	Done  bool
	Wait  bool
	Tools int
}

type ToolResult struct {
	Done, Wait, Recheck bool
	PollSeconds         int
}

// GeneralAgentWorkflow passes only durable identifiers through Temporal. The
// model messages, tool arguments and observations stay encrypted in Postgres.
func GeneralAgentWorkflow(ctx workflow.Context, run string) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3, InitialInterval: time.Second},
	})
	signals := workflow.GetSignalChannel(ctx, "control")
	var remaining = maxActions
	for turn := 1; turn <= maxModelTurns; turn++ {
		var result TurnResult
		for {
			if err := workflow.ExecuteActivity(ctx, AdvanceTurnActivity, TurnRef{RunID: run, Turn: turn}).Get(ctx, &result); err != nil {
				return err
			}
			if !result.Wait {
				break
			}
			var signal string
			signals.Receive(ctx, &signal)
		}
		if result.Done {
			return nil
		}
		if result.Tools < 1 || result.Tools > maxToolsPerTurn {
			return fmt.Errorf("invalid tool count for turn %d", turn)
		}
		if result.Tools > remaining {
			break
		}
		for index := 1; index <= result.Tools; index++ {
			var tool ToolResult
			ref := ToolRef{RunID: run, Turn: turn, Position: index}
			for {
				if err := workflow.ExecuteActivity(ctx, ExecuteToolActivity, ref).Get(ctx, &tool); err != nil {
					return err
				}
				if !tool.Wait {
					break
				}
				waitOnTool(ctx, signals, tool.Recheck, tool.PollSeconds)
			}
			if tool.Done {
				return nil
			}
			remaining--
		}
	}
	v := TurnRef{RunID: run, Turn: maxModelTurns + 1, StopReason: "已达到本次任务的处理上限。请缩小范围后继续。"}
	return workflow.ExecuteActivity(ctx, AdvanceTurnActivity, v).Get(ctx, nil)
}

func waitOnTool(ctx workflow.Context, signals workflow.ReceiveChannel, recheck bool, pollSeconds int) {
	var signal string
	if !recheck && pollSeconds == 0 {
		signals.Receive(ctx, &signal)
		return
	}
	timerCtx, cancel := workflow.WithCancel(ctx)
	defer cancel()
	interval := time.Minute
	if pollSeconds > 0 {
		interval = time.Duration(pollSeconds) * time.Second
	}
	timer := workflow.NewTimer(timerCtx, interval)
	workflow.NewSelector(ctx).AddFuture(timer, func(workflow.Future) {}).AddReceive(signals, func(channel workflow.ReceiveChannel, _ bool) {
		channel.Receive(ctx, &signal)
	}).Select(ctx)
}
