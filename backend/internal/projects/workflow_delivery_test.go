package projects

import (
	"reflect"
	"testing"
)

func TestWorkflowDeliveryPinsUseOutputNodesAndBusinessDependencies(t *testing.T) {
	graph := []byte(`[
 {"agent":"input","revision":1},
 {"agent":"lookup","revision":2,"lookupPhase":"search","dependsOn":[1]},
 {"agent":"lookup","revision":2,"lookupPhase":"answer","dependsOn":[2]},
 {"agent":"lookup","revision":2,"lookupPhase":"synthesize","dependsOn":[3]},
 {"agent":"report","revision":3,"dependsOn":[4,1]}
]`)
	pins, ordinals, err := deliveryPins(graph)
	if err != nil || !reflect.DeepEqual(ordinals, []int{1, 4, 5}) || len(pins) != 3 {
		t.Fatalf("output mapping: %+v %v %v", pins, ordinals, err)
	}
	if pins[1] != (deliveryStepPin{Agent: "lookup", Revision: 2, Phase: "synthesize", inputs: "1,"}) || pins[2].inputs != "1,2," {
		t.Fatalf("proof condition must follow the synthesis and preserve logical dependencies: %+v", pins)
	}
}

func TestWorkflowDeliveryConditionsRequireReviewForRemovalOrRebinding(t *testing.T) {
	pins := []deliveryStepPin{{Agent: "read", Revision: 1}, {Agent: "summary", Revision: 1, inputs: "1,"}}
	baseline := DeliveryContract{Items: []DeliveryRequirement{{Step: 2, Label: "注明依据"}}}
	added := DeliveryContract{Items: []DeliveryRequirement{{Step: 1, Label: "列出原件"}, {Step: 2, Label: "注明依据 "}}}
	if !retainsDeliveryConditions(baseline, pins, added, pins) {
		t.Fatal("an additional condition should preserve the existing obligation")
	}
	if retainsDeliveryConditions(baseline, pins, DeliveryContract{}, pins) {
		t.Fatal("an empty request discarded a required item")
	}
	rebound := append([]deliveryStepPin(nil), pins...)
	rebound[1].inputs = ""
	if retainsDeliveryConditions(baseline, pins, added, rebound) {
		t.Fatal("an unchanged label masked a different business step")
	}
}
