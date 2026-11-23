package planner

import "testing"

func TestColumnSuggestionSelection(t *testing.T) {
	input := []columnSuggestionInput{
		{Fields: []columnField{{Name: "region"}, {Name: "amount"}}, Columns: []string{"area", "net"}},
		{Fields: []columnField{{Name: "amount"}}, Columns: []string{"net"}},
	}
	tests := []struct {
		name, reply, status string
	}{
		{"partial", "```json\n[{\"file\":2,\"field\":1,\"column\":1}]\n```", "ready"},
		{"empty", `[]`, "ambiguous"},
		{"null", `null`, "invalid"},
		{"missing column", `[{"file":1,"field":1}]`, "invalid"},
		{"foreign column", `[{"file":2,"field":1,"column":2}]`, "invalid"},
		{"unknown property", `[{"file":1,"field":1,"column":1,"value":"fake"}]`, "invalid"},
		{"same field twice", `[{"file":1,"field":1,"column":1},{"file":1,"field":1,"column":2}]`, "invalid"},
		{"same column twice", `[{"file":1,"field":1,"column":1},{"file":1,"field":2,"column":1}]`, "invalid"},
		{"trailing content", `[{"file":1,"field":1,"column":1}] []`, "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := parseColumnSuggestions(test.reply, input)
			if got.Status != test.status {
				t.Fatalf("status=%q, want %q", got.Status, test.status)
			}
			if got.Status != "ready" && len(got.Entries) > 0 {
				t.Fatalf("rejected entries escaped validation: %+v", got.Entries)
			}
		})
	}
}
