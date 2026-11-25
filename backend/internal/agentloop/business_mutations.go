package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"alliance/internal/bizconnectors"
	"alliance/internal/capability"
	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"github.com/jackc/pgx/v5"
)

const businessWriteList = "list_business_write_actions"
const businessWriteProposal = "propose_business_write"

var businessWriteTools = []registeredTool{
	{businessWriteList, "列出当前任务可申请的核心系统变更动作及可变更字段。", `{"type":"object","properties":{},"additionalProperties":false}`, harness.ReadOnly, capability.Knowledge},
	{businessWriteProposal, "生成核心业务系统变更预览，等待有权负责人审核。只有取得正式回执时才视为写回完成。",
		`{"type":"object","properties":{"policyId":{"type":"string","format":"uuid"},"recordKey":{"type":"string","maxLength":160},"values":{"type":"object","maxProperties":12}},"required":["policyId","recordKey","values"],"additionalProperties":false}`,
		harness.SystemWrite, capability.Knowledge},
}

func (a Activities) listBusinessMutations(ctx context.Context, ref ToolRef, task toolWork) (resources.Reply, error) {
	if a.Business == nil {
		return resources.Reply{}, policy.ErrDenied
	}
	if err := decodeToolArgs(task.args, new(struct{})); err != nil {
		return resources.Reply{}, harness.ErrToolArguments
	}
	items, err := a.Business.VisibleMutations(ctx, ref.RunID, task.actor)
	if err != nil {
		return resources.Reply{}, err
	}
	for len(items) > 0 {
		body, err := json.Marshal(items)
		if err != nil || len(body) <= 8192 {
			return resources.Reply{Text: string(body)}, err
		}
		items = items[:len(items)-1]
	}
	return resources.Reply{Text: "[]"}, nil
}

func (a Activities) executeBusinessMutation(ctx context.Context, ref ToolRef, args string) (ToolResult, error) {
	var input bizconnectors.MutationRequest
	decoder := json.NewDecoder(strings.NewReader(args))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&input)
	var progress bizconnectors.MutationResult
	if err == nil {
		businessRef := bizconnectors.MutationRef{Run: ref.RunID, Turn: ref.Turn, Position: ref.Position}
		progress, err = a.Business.PrepareMutation(ctx, businessRef, input)
		if err == nil && progress.Status != "duplicate" {
			progress, err = a.Business.StepMutation(ctx, businessRef)
		}
	} else {
		err = harness.ErrToolArguments
	}
	if err == nil {
		switch progress.Status {
		case "pending", "approved", "executing", "reconciling":
			return ToolResult{Wait: true, Recheck: true, PollSeconds: 12}, nil
		}
	}
	if err != nil && !errors.Is(err, bizconnectors.ErrBadQuery) && !errors.Is(err, bizconnectors.ErrUnavailable) &&
		!errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, harness.ErrToolArguments) {
		return ToolResult{}, err
	}
	return ToolResult{}, database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		current, loadErr := loadRun(ctx, tx, ref.RunID)
		if loadErr != nil || current.terminal() {
			return loadErr
		}
		var state string
		if readErr := tx.QueryRow(ctx, "select state from general_agent_actions where (run_id,turn,position)=($1,$2,$3) for update",
			ref.RunID, ref.Turn, ref.Position).Scan(&state); readErr != nil || state != "queued" {
			return readErr
		}
		if err != nil {
			return a.recordObservation(ctx, tx, ref, "rejected", "没有可确认的写回预览。", "", "",
				failureObservation(businessWriteProposal, "preview_unavailable", "核对当前空间、操作、业务记录和变更字段。"))
		}
		body, marshalErr := json.Marshal(progress)
		if marshalErr != nil {
			return marshalErr
		}
		if progress.Status == "succeeded" {
			return a.recordObservation(ctx, tx, ref, "completed", string(body), "", "")
		}
		return a.recordObservation(ctx, tx, ref, "rejected", string(body), "", "",
			failureObservation(businessWriteProposal, progress.Status, "以写回卡片和正式回执为准。已有写回处理中时无需再次提交。"))
	})
}
