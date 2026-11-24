package projects

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type deliveryStepPin struct {
	Agent    string `json:"agent"`
	Revision int    `json:"revision"`
	Phase    string `json:"lookupPhase"`
	inputs   string
}

// Template conditions use business step numbers. Stored plan conditions use
// the terminal executable node in each business step.
func deliveryPins(raw []byte) ([]deliveryStepPin, []int, error) {
	var expanded []struct {
		deliveryStepPin
		DependsOn []int `json:"dependsOn"`
	}
	if err := json.Unmarshal(raw, &expanded); err != nil {
		return nil, nil, err
	}
	nodes := make([]executionNode, len(expanded))
	for i, pin := range expanded {
		nodes[i].phase = pin.Phase
	}
	ordinals := deliveryOrdinals(nodes)
	pins := make([]deliveryStepPin, len(ordinals))
	logical := make([]int, len(expanded)+1)
	start := 1
	for i, ordinal := range ordinals {
		for node := start; node <= ordinal; node++ {
			logical[node] = i + 1
		}
		pin := expanded[ordinal-1].deliveryStepPin
		inputs := make([]int, 0, len(expanded[start-1].DependsOn))
		for _, dep := range expanded[start-1].DependsOn {
			if dep < 1 || dep >= start {
				return nil, nil, fmt.Errorf("delivery step has invalid dependency %d", dep)
			}
			inputs = append(inputs, logical[dep])
		}
		slices.Sort(inputs)
		var signature strings.Builder
		for _, step := range inputs {
			signature.WriteString(strconv.Itoa(step))
			signature.WriteByte(',')
		}
		pin.inputs = signature.String()
		pins[i] = pin
		start = ordinal + 1
	}
	return pins, ordinals, nil
}

func authoredWorkflowDelivery(ctx context.Context, tx pgx.Tx, p Project, graph []byte, contract *DeliveryContract) (DeliveryContract, error) {
	if contract == nil {
		var hasConditions bool
		if err := tx.QueryRow(ctx, `select exists(select from plan_delivery_contracts where (project_id,plan_version)=($1,$2))`, p.ID, p.PlanVersion).Scan(&hasConditions); err != nil {
			return DeliveryContract{}, err
		}
		if hasConditions {
			return DeliveryContract{}, web.Fail(422, "请核对哪些必交项适合复用，并明确提交模板的交付条件")
		}
		return DeliveryContract{Items: []DeliveryRequirement{}}, nil
	}
	_, ordinals, err := deliveryPins(graph)
	if err != nil {
		return DeliveryContract{}, err
	}
	items, err := validateBusinessDeliveryRequirements(*contract, len(ordinals))
	if err != nil {
		return DeliveryContract{}, err
	}
	for _, item := range items {
		if strings.ContainsAny(item.Label, "\r\n") {
			return DeliveryContract{}, web.Fail(422, "模板必交项每项限一行")
		}
	}
	return DeliveryContract{AllowPartial: contract.AllowPartial, Items: items}, nil
}

const deliveryPlanGraph = `coalesce((select jsonb_agg(jsonb_build_object(
 'agent',n.agent_id,'revision',n.agent_revision,'lookupPhase',n.lookup_phase,
 'dependsOn',array(select e.depends_on from node_edges e where (e.project_id,e.plan_version,e.ordinal)=
 (n.project_id,n.plan_version,n.ordinal) order by e.depends_on)) order by n.ordinal)
 from nodes n where (n.project_id,n.plan_version)=($1,$2)),'[]')`

func existingDelivery(ctx context.Context, tx pgx.Tx, project string, plan int) (DeliveryContract, []deliveryStepPin, error) {
	var raw, graph []byte
	err := tx.QueryRow(ctx, `select (select jsonb_build_object('allowPartial',c.allow_partial,'items',array(
   select jsonb_build_object('step',q.ordinal,'label',q.label) from plan_delivery_requirements q
   where (q.project_id,q.plan_version)=(c.project_id,c.plan_version) order by q.item))
   from plan_delivery_contracts c where (c.project_id,c.plan_version)=($1,$2)),`+deliveryPlanGraph, project, plan).
		Scan(&raw, &graph)
	if err != nil || raw == nil {
		return DeliveryContract{}, nil, err
	}
	var contract DeliveryContract
	if err := json.Unmarshal(raw, &contract); err != nil {
		return DeliveryContract{}, nil, err
	}
	pins, ordinals, err := deliveryPins(graph)
	if err != nil {
		return DeliveryContract{}, nil, err
	}
	logical := make(map[int]int, len(ordinals))
	for i, ordinal := range ordinals {
		logical[ordinal] = i + 1
	}
	for i := range contract.Items {
		ordinal := contract.Items[i].Step
		step, ok := logical[ordinal]
		if !ok {
			return DeliveryContract{}, nil, fmt.Errorf("delivery condition refers to non-terminal node %d", ordinal)
		}
		contract.Items[i].Step = step
	}
	return contract, pins, nil
}

func retainsDeliveryConditions(baseline DeliveryContract, baselinePins []deliveryStepPin, contract DeliveryContract, pins []deliveryStepPin) bool {
	provided := make(map[DeliveryRequirement]bool, len(contract.Items))
	for _, item := range contract.Items {
		provided[DeliveryRequirement{Step: item.Step, Label: strings.TrimSpace(item.Label)}] = true
	}
	for _, item := range baseline.Items {
		if !provided[item] || item.Step < 1 || item.Step > len(pins) || item.Step > len(baselinePins) ||
			pins[item.Step-1] != baselinePins[item.Step-1] {
			return false
		}
	}
	return true
}

func workflowPlanDelivery(ctx context.Context, tx pgx.Tx, p Project, plan, templateVersion int, in PlanInput) (*DeliveryContract, error) {
	var raw, graph []byte
	err := tx.QueryRow(ctx, `select v.delivery_contract,v.nodes from project_workflow_template_binding b
   join workflow_template_versions v on(v.template_id,v.version)=(b.template_id,coalesce(nullif($2,0),b.version))
   where b.project_id=$1`, p.ID, templateVersion).Scan(&raw, &graph)
	if err == pgx.ErrNoRows {
		return in.DeliveryContract, nil
	}
	if err != nil {
		return nil, err
	}
	var defaults, previous DeliveryContract
	var defaultPins, previousPins []deliveryStepPin
	if raw != nil && (p.PlanVersion == 0 || templateVersion > 0) {
		if err := json.Unmarshal(raw, &defaults); err != nil {
			return nil, err
		}
		defaultPins, _, err = deliveryPins(graph)
		if err != nil {
			return nil, err
		}
	}
	if p.PlanVersion > 0 {
		previous, previousPins, err = existingDelivery(ctx, tx, p.ID, p.PlanVersion)
		if err != nil {
			return nil, err
		}
	}
	if len(defaults.Items) == 0 && len(previous.Items) == 0 {
		return in.DeliveryContract, nil
	}
	if err := tx.QueryRow(ctx, `select `+deliveryPlanGraph, p.ID, plan).Scan(&graph); err != nil {
		return nil, err
	}
	pins, _, err := deliveryPins(graph)
	if err != nil {
		return nil, err
	}
	contract := &DeliveryContract{Items: defaults.Items}
	if in.DeliveryContract != nil {
		contract = in.DeliveryContract
	}
	if !retainsDeliveryConditions(defaults, defaultPins, *contract, pins) || !retainsDeliveryConditions(previous, previousPins, *contract, pins) {
		if in.DeliveryContract == nil || !in.ConfirmDeliveryChanges {
			return nil, web.Fail(409, "必交项或对应步骤已变化，请核对并确认本次交付条件")
		}
		if err := database.Audit(ctx, tx, p.Owner, "plan.delivery_conditions.confirm", p.ID, map[string]int{"plan": plan}); err != nil {
			return nil, err
		}
	}
	return contract, nil
}
