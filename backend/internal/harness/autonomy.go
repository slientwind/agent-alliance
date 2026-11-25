package harness

type AutonomyLevel string

const (
	L0 AutonomyLevel = "L0"
	L1 AutonomyLevel = "L1"
	L2 AutonomyLevel = "L2"
)

type ToolRisk string

const (
	ReadOnly        ToolRisk = "read_only"
	IsolatedSandbox ToolRisk = "sandbox"
	FileGeneration  ToolRisk = "file_generation"
	ExternalSend    ToolRisk = "external_send"
	SystemWrite     ToolRisk = "system_write"
	FormalDelivery  ToolRisk = "formal_delivery"
)

type AutonomyPolicy struct {
	MaxAutomaticCostMicroCny int64
	MaxAutomaticBatch        int
}

// ToolGate is populated from a tool registration and live authorization and
// quota checks. Tool arguments and model annotations never supply this data.
type ToolGate struct {
	Risk            ToolRisk
	Authorized      bool
	QuotaAvailable  bool
	ExternalPayment bool
	EstimatedCost   *int64
	BatchSize       int
}

type AutonomyDecision struct {
	Level  AutonomyLevel `json:"level,omitempty"`
	Action string        `json:"action"`
	Reason string        `json:"reason,omitempty"`
}

func (p AutonomyPolicy) Evaluate(g ToolGate) AutonomyDecision {
	if !g.Authorized {
		return AutonomyDecision{Action: "deny", Reason: "access_ended"}
	}
	if !g.QuotaAvailable {
		return AutonomyDecision{Action: "deny", Reason: "quota_exhausted"}
	}
	if g.BatchSize < 1 || p.MaxAutomaticBatch < 1 || p.MaxAutomaticCostMicroCny < 0 ||
		(g.EstimatedCost != nil && *g.EstimatedCost < 0) {
		return AutonomyDecision{Action: "deny", Reason: "invalid_limits"}
	}
	switch g.Risk {
	case ExternalSend, SystemWrite, FormalDelivery:
		return AutonomyDecision{Level: L2, Action: "review", Reason: string(g.Risk)}
	case ReadOnly, IsolatedSandbox, FileGeneration:
	default:
		return AutonomyDecision{Action: "deny", Reason: "unknown_tool_risk"}
	}
	switch {
	case g.ExternalPayment:
		return AutonomyDecision{Level: L1, Action: "confirm", Reason: "external_payment"}
	case g.EstimatedCost == nil:
		return AutonomyDecision{Level: L1, Action: "confirm", Reason: "unknown_cost"}
	case *g.EstimatedCost > p.MaxAutomaticCostMicroCny:
		return AutonomyDecision{Level: L1, Action: "confirm", Reason: "high_cost"}
	case g.BatchSize > p.MaxAutomaticBatch:
		return AutonomyDecision{Level: L1, Action: "confirm", Reason: "large_batch"}
	default:
		return AutonomyDecision{Level: L0, Action: "execute"}
	}
}
