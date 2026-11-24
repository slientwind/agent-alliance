package runtime

import (
	"alliance/internal/harness"
	"alliance/internal/resources"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"
)

const remoteReceiptPrefix = "remote-receipt/"
const sourceReceiptPrefix = "source-receipt/"

func remoteReceiptSignal(ordinal int) string {
	return remoteReceiptPrefix + strconv.Itoa(ordinal)
}

// Topology returns nodes in ordinal order, with all dependencies earlier.
// Consecutive nodes without a direct edge cannot have an indirect edge.
func hasParallelSteps(nodes []Node) bool {
	for i := 1; i < len(nodes); i++ {
		if !slices.Contains(nodes[i].Dependencies, nodes[i-1].Ordinal) {
			return true
		}
	}
	return false
}

type scheduledStep struct {
	future      workflow.Future
	lastWake    int
	seenWake    int
	localCalls  int
	legacyCheck bool
	waiting     bool
	finished    bool
}

// A signal is a hint to re-read committed state in the Activity. Retain the
// hint while an Activity is in flight so its eventual Wait cannot miss it.
func scheduleIndependentSteps(ctx workflow.Context, run string, nodes []Node, lookupTurns, localActions bool) error {
	if len(nodes) == 0 {
		return errors.New("run has no executable steps")
	}
	byOrdinal := make(map[int]int, len(nodes))
	for i, node := range nodes {
		byOrdinal[node.Ordinal] = i
	}
	steps := make([]scheduledStep, len(nodes))
	control := workflow.GetSignalChannel(ctx, "control")
	var wake, globalWake, finished int
	var stopping bool
	var terminalErr error
	for finished < len(nodes) {
		if !stopping {
			for i, node := range nodes {
				step := &steps[i]
				latestWake := max(globalWake, step.lastWake)
				if step.finished || step.future != nil || (step.waiting && step.seenWake >= latestWake) {
					continue
				}
				if !dependenciesFinished(node, byOrdinal, steps) {
					continue
				}
				step.waiting, step.localCalls, step.legacyCheck = false, 0, false
				step.seenWake = latestWake
				step.future = startDAGStep(ctx, run, node, lookupTurns, localActions)
			}
		}
		readyIndex := -1
		var active, waiting bool
		selector := workflow.NewSelector(ctx)
		for i := range steps {
			step := &steps[i]
			waiting = waiting || step.waiting
			if step.future != nil {
				active = true
				selector.AddFuture(step.future, func(workflow.Future) { readyIndex = i })
			}
		}
		if !active && stopping {
			return terminalErr
		}
		if !active && !waiting {
			return errors.New("dependency cycle or missing node")
		}
		if !stopping {
			selector.AddReceive(control, func(c workflow.ReceiveChannel, _ bool) {
				var signal string
				c.Receive(ctx, &signal)
				wake++
				suffix, receipt := strings.CutPrefix(signal, remoteReceiptPrefix)
				if !receipt {
					suffix, receipt = strings.CutPrefix(signal, sourceReceiptPrefix)
				}
				if receipt {
					if ordinal, err := strconv.Atoi(suffix); err == nil {
						if i, exists := byOrdinal[ordinal]; exists {
							steps[i].lastWake = wake
						}
					}
					return
				}
				globalWake = wake
			})
		}
		selector.Select(ctx)
		if readyIndex == -1 {
			continue
		}
		step := &steps[readyIndex]
		var outcome Result
		err := step.future.Get(ctx, &outcome)
		step.future = nil
		if stopping {
			continue
		}
		if outcome.Stop || err != nil {
			// Let already dispatched calls commit their receipts before leaving.
			stopping, terminalErr = true, err
			continue
		}
		switch {
		case outcome.Action != nil || outcome.Observation != nil || outcome.RevisitTurn || outcome.TextReply != nil:
			if step.localCalls == localTransitionLimit(nodes[readyIndex]) {
				step.waiting = true
				continue
			}
			step.localCalls++
			step.future = advanceLocalActivity(ctx, run, nodes[readyIndex], outcome)
		case outcome.LookupAttempt > 0 && !step.legacyCheck:
			step.legacyCheck = true
			action := lookupAction{Step: Step{run, nodes[readyIndex].Ordinal}, Attempt: outcome.LookupAttempt}
			step.future = workflow.ExecuteActivity(ctx, "ExecuteLookupAction", action)
		case outcome.Wait || outcome.LookupAttempt > 0:
			step.waiting = true
		default:
			step.finished = true
			finished++
		}
	}
	return nil
}

func dependenciesFinished(node Node, byOrdinal map[int]int, steps []scheduledStep) bool {
	for _, ordinal := range node.Dependencies {
		i, exists := byOrdinal[ordinal]
		if !exists || !steps[i].finished {
			return false
		}
	}
	return true
}

func startDAGStep(ctx workflow.Context, run string, node Node, lookupTurns, localActions bool) workflow.Future {
	// Topologies recorded by older workflows decode Image as false.
	if node.Image {
		ctx = workflow.WithStartToCloseTimeout(ctx, resources.ImageTimeout+30*time.Second)
	}
	activity := "ExecuteStep"
	if localActions && (node.LookupAnswer || node.Calculation) {
		activity = localTurnActivity(node)
	} else if lookupTurns && node.LookupAnswer {
		activity = "AdvanceTurn"
	} else if node.CheckpointText {
		activity = "AdvanceTextStep"
	}
	return workflow.ExecuteActivity(ctx, activity, Step{run, node.Ordinal})
}

func localTurnActivity(node Node) string {
	if node.batchLookupActions {
		return "AdvanceBatchedLookup"
	}
	if node.observeLocalActions {
		return "AdvanceObservedTurn"
	}
	return "AdvanceExecutionTurn"
}

func localTransitionLimit(node Node) int {
	if node.batchLookupActions {
		return 2*harness.MaxLookupBatches + 4
	}
	return 4
}

func advanceLocalActivity(ctx workflow.Context, run string, node Node, outcome Result) workflow.Future {
	if outcome.TextReply != nil {
		return workflow.ExecuteActivity(ctx, "CommitTextReply", *outcome.TextReply)
	}
	if outcome.RevisitTurn {
		return workflow.ExecuteActivity(ctx, localTurnActivity(node), Step{run, node.Ordinal})
	}
	if outcome.Observation != nil {
		return workflow.ExecuteActivity(ctx, "CommitLocalObservation", *outcome.Observation)
	}
	return workflow.ExecuteActivity(ctx, "ExecuteAction", *outcome.Action)
}
