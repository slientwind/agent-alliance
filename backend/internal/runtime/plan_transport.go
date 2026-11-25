package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	GeneralWorkflowName = "GeneralAgentWorkflow"
	GeneralAdvanceName  = "GeneralAgent.AdvanceTurn"
	GeneralExecuteName  = "GeneralAgent.ExecuteTool"
)

const plannedPrefix = "approved-plan/"

func GeneralQueue() string { return Queue() + "-general" }

func PlannedRunInput(run string) string { return plannedPrefix + run }

func PlannedRunID(input string) (string, bool) { return strings.CutPrefix(input, plannedPrefix) }

type PlannedCall struct {
	Operation string          `json:"operation"`
	Reference json.RawMessage `json:"reference"`
}

type planTransportKey struct{}

// RunApprovedPlan uses the reviewed plan's pinned agents and ordered steps in
// the general worker. The workflow options come from the general entry point.
func RunApprovedPlan(ctx workflow.Context, run string) error {
	return scheduleDAG(workflow.WithValue(ctx, planTransportKey{}, run), run)
}

func schedulePlanActivity(ctx workflow.Context, operation string, reference any) workflow.Future {
	run, general := ctx.Value(planTransportKey{}).(string)
	if !general {
		return workflow.ExecuteActivity(ctx, operation, reference)
	}
	raw, err := json.Marshal(reference)
	if err != nil {
		result, setter := workflow.NewFuture(ctx)
		setter.Set(nil, err)
		return result
	}
	input := struct {
		RunID   string
		Planned *PlannedCall
	}{run, &PlannedCall{Operation: operation, Reference: raw}}
	method := GeneralAdvanceName
	switch operation {
	case "ExecuteStep", "ExecuteLookupAction", "ExecuteAction", "ExecuteEvidenceAction":
		method = GeneralExecuteName
	}
	return workflow.ExecuteActivity(ctx, method, input)
}

func readPlanResult(ctx workflow.Context, future workflow.Future, into any) error {
	if _, general := ctx.Value(planTransportKey{}).(string); !general || into == nil {
		return future.Get(ctx, into)
	}
	var envelope struct{ Planned json.RawMessage }
	if err := future.Get(ctx, &envelope); err != nil {
		return err
	}
	return json.Unmarshal(envelope.Planned, into)
}

// ProcessApprovedPlan accepts only the fixed operations of the approved-plan
// engine. The referenced plans, observations, and tool inputs remain in SQL.
func (a Activities) ProcessApprovedPlan(ctx context.Context, call PlannedCall, tool bool) (json.RawMessage, error) {
	if tool {
		switch call.Operation {
		case "ExecuteStep":
			return invokePlan(ctx, call.Reference, a.ExecuteStep)
		case "ExecuteLookupAction":
			return invokePlan(ctx, call.Reference, a.ExecuteLookupAction)
		case "ExecuteAction":
			return invokePlan(ctx, call.Reference, a.ExecuteAction)
		case "ExecuteEvidenceAction":
			return invokePlan(ctx, call.Reference, a.ExecuteEvidenceAction)
		}
		return nil, invalidPlanOperation()
	}

	switch call.Operation {
	case "Topology":
		return invokePlan(ctx, call.Reference, a.Topology)
	case "EnableStepRemoteInput":
		return preparePlan(ctx, call.Reference, a.EnableStepRemoteInput)
	case "EnableStepSourceReceipts":
		return preparePlan(ctx, call.Reference, a.EnableStepSourceReceipts)
	case "EnableStepToolChoices":
		return preparePlan(ctx, call.Reference, a.EnableStepToolChoices)
	case "EnableStepDispatchWaits":
		return preparePlan(ctx, call.Reference, a.EnableStepDispatchWaits)
	case "AdvanceTurn":
		return invokePlan(ctx, call.Reference, a.AdvanceTurn)
	case "AdvanceTextStep":
		return invokePlan(ctx, call.Reference, a.AdvanceTextStep)
	case "AdvanceEvidenceStep":
		return invokePlan(ctx, call.Reference, a.AdvanceEvidenceStep)
	case "AdvanceEvidenceQuestion":
		return invokePlan(ctx, call.Reference, a.AdvanceEvidenceQuestion)
	case "AdvanceFocusedEvidence":
		return invokePlan(ctx, call.Reference, a.AdvanceFocusedEvidence)
	case "AdvanceRequirementEvidence":
		return invokePlan(ctx, call.Reference, a.AdvanceRequirementEvidence)
	case "AdvanceExecutionTurn":
		return invokePlan(ctx, call.Reference, a.AdvanceExecutionTurn)
	case "AdvanceObservedTurn":
		return invokePlan(ctx, call.Reference, a.AdvanceObservedTurn)
	case "AdvanceBatchedLookup":
		return invokePlan(ctx, call.Reference, a.AdvanceBatchedLookup)
	case "CommitTextReply":
		return invokePlan(ctx, call.Reference, a.CommitTextReply)
	case "CommitEvidenceReply":
		return invokePlan(ctx, call.Reference, a.CommitEvidenceReply)
	case "CommitEvidenceQuestion":
		return invokePlan(ctx, call.Reference, a.CommitEvidenceQuestion)
	case "CommitLocalObservation":
		return invokePlan(ctx, call.Reference, a.CommitLocalObservation)
	case "CommitEvidenceObservation":
		return invokePlan(ctx, call.Reference, a.CommitEvidenceObservation)
	default:
		return nil, invalidPlanOperation()
	}
}

func invalidPlanOperation() error {
	return temporal.NewNonRetryableApplicationError("invalid approved-plan operation", "InvalidPlanOperation", nil)
}

func invokePlan[Ref, Result any](ctx context.Context, raw json.RawMessage, fn func(context.Context, Ref) (Result, error)) (json.RawMessage, error) {
	var ref Ref
	if err := json.Unmarshal(raw, &ref); err != nil {
		return nil, invalidPlanOperation()
	}
	result, err := fn(ctx, ref)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func preparePlan(ctx context.Context, raw json.RawMessage, fn func(context.Context, string) error) (json.RawMessage, error) {
	return invokePlan(ctx, raw, func(ctx context.Context, run string) (bool, error) { return true, fn(ctx, run) })
}
