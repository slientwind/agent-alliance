package agentloop

import (
	"alliance/internal/harness"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"errors"
)

type toolFailure struct {
	Operation string `json:"operation"`
	Code      string `json:"code"`
	Guidance  string `json:"guidance"`
}

func failureObservation(operation, code, guidance string) toolFailure {
	return toolFailure{Operation: operation, Code: code, Guidance: guidance}
}

func recoverToolObservation(operation string, failure error) toolFailure {
	var input *resources.ToolInputError
	switch {
	case errors.Is(failure, harness.ErrToolArguments), errors.As(failure, &input):
		return failureObservation(operation, "invalid_input", "根据已有资料修正参数，再提交一组有效输入。")
	case errors.Is(failure, policy.ErrDenied):
		return failureObservation(operation, "access_ended", "停止使用此工具，继续使用本任务已获准的资料，或说明缺项。")
	default:
		return failureObservation(operation, "result_unconfirmed", "没有确认产物。可改用此任务已启用的其他工具。保留用户明确指定的交付格式，无法完成时说明缺项。")
	}
}
