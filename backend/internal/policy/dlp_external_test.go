package policy

import (
	"strings"
	"testing"
)

func TestTokensStayStableWithinTheirExchange(t *testing.T) {
	scanner, err := NewDLPScanner([]DLPRule{{Kind: DLPCustom, Sensitivity: SensitivitySecret, Match: "fixture conspiracy"}})
	if err != nil {
		t.Fatal(err)
	}
	output, tokens, counts, denied := redactExternalFields(scanner,
		[]string{"Preview [MOBILE_01] for 13800000000", "also 13800000000"}, "", nil)
	if denied || len(tokens) != 1 || counts[DLPMobile] != 2 ||
		output[0] != "Preview [MOBILE_01] for [MOBILE_02]" || output[1] != "also [MOBILE_02]" {
		t.Fatalf("unexpected first projection: %v %v %v", output, counts, denied)
	}
	output, further, _, denied := redactExternalFields(scanner, []string{"correct 13800000000"}, "", tokens)
	if denied || len(further) != 1 || output[0] != "correct [MOBILE_02]" {
		t.Fatalf("supplement did not reuse the existing map: %v", output)
	}
	for _, text := range []string{"echo [MOBILE_02]", "no fixture conspiracy", "API key: never-echo", strings.Repeat("a", maxDLPOutboundBytes+1)} {
		output, _, _, denied = redactExternalFields(scanner, []string{text}, "", tokens)
		if !denied || output != nil {
			t.Fatal("restricted or invented placeholder input was not blocked")
		}
	}
	output, _, _, denied = redactExternalFields(scanner, []string{"technical note"}, SensitivityConfidential, nil)
	if denied || len(output) != 1 || output[0] != "[CLASSIFIED_FIELD_01]" {
		t.Fatalf("confidential label did not protect the entire field: %v", output)
	}
	if output, _, _, denied := redactExternalFields(scanner, []string{"technical note"}, SensitivitySecret, nil); !denied || output != nil {
		t.Fatal("explicit secret label was allowed to leave")
	}
}
