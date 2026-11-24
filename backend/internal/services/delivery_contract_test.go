package services

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServiceDeliveryOptInChangesAdmissionAndRequestIdentity(t *testing.T) {
	in := request{Input: "整理原件", Version: 1, AuthorizeCalls: 2}
	caller := subject{Actor: "member", Service: "service"}
	strict := serviceAdmission{count: 2}
	if err := strict.validateInput(in); err != nil {
		t.Fatal(err)
	}
	defaultKey := in.fingerprint(caller)
	in.AllowPartialDelivery = true
	if err := strict.validateInput(in); err == nil || !strings.Contains(err.Error(), "必交项齐全") {
		t.Fatalf("strict version admitted partial delivery: %v", err)
	}
	optIn := strict
	optIn.allowsPartialDelivery = true
	if err := optIn.validateInput(in); err != nil {
		t.Fatal(err)
	}
	if defaultKey == in.fingerprint(caller) {
		t.Fatal("same request fingerprint for different delivery commitments")
	}
	var omitted request
	if err := json.Unmarshal([]byte(`{"input":"整理原件","version":1,"authorizeCalls":2}`), &omitted); err != nil ||
		omitted.fingerprint(caller) != defaultKey {
		t.Fatalf("omitted delivery option changed the default: %v", err)
	}
}
