package harness

import "testing"

func TestAutonomyPolicyDecidesFromLivePermissionsAndRegisteredRisk(t *testing.T) {
	p := AutonomyPolicy{MaxAutomaticCostMicroCny: 100, MaxAutomaticBatch: 3}
	free, over, exact := int64(0), int64(101), int64(100)
	for _, tc := range []struct {
		name string
		gate ToolGate
		want AutonomyDecision
	}{
		{"read", ToolGate{Risk: ReadOnly, Authorized: true, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{L0, "execute", ""}},
		{"sandbox at threshold", ToolGate{Risk: IsolatedSandbox, Authorized: true, QuotaAvailable: true, EstimatedCost: &exact, BatchSize: 3}, AutonomyDecision{L0, "execute", ""}},
		{"file", ToolGate{Risk: FileGeneration, Authorized: true, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{L0, "execute", ""}},
		{"unknown cost", ToolGate{Risk: ReadOnly, Authorized: true, QuotaAvailable: true, BatchSize: 1}, AutonomyDecision{L1, "confirm", "unknown_cost"}},
		{"batch", ToolGate{Risk: ReadOnly, Authorized: true, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 4}, AutonomyDecision{L1, "confirm", "large_batch"}},
		{"cost", ToolGate{Risk: ReadOnly, Authorized: true, QuotaAvailable: true, EstimatedCost: &over, BatchSize: 1}, AutonomyDecision{L1, "confirm", "high_cost"}},
		{"external payment", ToolGate{Risk: ReadOnly, Authorized: true, QuotaAvailable: true, ExternalPayment: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{L1, "confirm", "external_payment"}},
		{"send outranks cost", ToolGate{Risk: ExternalSend, Authorized: true, QuotaAvailable: true, EstimatedCost: &over, BatchSize: 1}, AutonomyDecision{L2, "review", "external_send"}},
		{"write", ToolGate{Risk: SystemWrite, Authorized: true, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{L2, "review", "system_write"}},
		{"deliver", ToolGate{Risk: FormalDelivery, Authorized: true, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{L2, "review", "formal_delivery"}},
		{"revoked", ToolGate{Risk: SystemWrite, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{"", "deny", "access_ended"}},
		{"no hard budget", ToolGate{Risk: FileGeneration, Authorized: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{"", "deny", "quota_exhausted"}},
		{"unknown tool risk", ToolGate{Risk: "invented", Authorized: true, QuotaAvailable: true, EstimatedCost: &free, BatchSize: 1}, AutonomyDecision{"", "deny", "unknown_tool_risk"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := p.Evaluate(tc.gate); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
