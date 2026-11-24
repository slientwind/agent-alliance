package harness

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestIsReceiptQuestion(t *testing.T) {
	if !IsReceiptQuestion("请补充供应商的有效期确认函。") || !IsReceiptQuestion(strings.Repeat("问", 320)) {
		t.Fatal("bounded question was rejected")
	}
	for _, value := range []string{"", "问\n答", "问\u202e答", "问\u2028答", strings.Repeat("问", 321)} {
		if IsReceiptQuestion(value) {
			t.Fatalf("question was accepted: %q", value)
		}
	}
}

func TestLookupDecisionBranches(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  LookupDecision
	}{
		{`{"answer":"  到期日为周五。","references":[2,1]}`, LookupDecision{Answer: "到期日为周五。", References: []int{2, 1}}},
		{`{"question":"  请补充第二份报价的有效期。 "}`, LookupDecision{Question: "请补充第二份报价的有效期。"}},
		{"```json\n{\"terms\":[\"备用单\"]}\n```", LookupDecision{Terms: []string{"备用单"}}},
	} {
		got, err := DecodeLookup(tc.reply, 2)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("DecodeLookup(%q) = %+v, %v; want %+v", tc.reply, got, err, tc.want)
		}
	}
}

func TestLookupDecisionRejectsUnusableReplies(t *testing.T) {
	for _, tc := range []struct {
		reply string
		want  error
	}{
		{`{"terms":["期限"]`, ErrLookupDecision},
		{`{"terms":["期限"]} {"question":"请补件"}`, ErrLookupDecision},
		{`{"terms":["期限"],"terms":["备用单"]}`, ErrLookupDecision},
		{`{"terms":["期限"],"question":"请补件"}`, ErrLookupDecision},
		{`{"terms":["期限"],"operation":"download"}`, ErrLookupDecision},
		{`{"question":null}`, ErrLookupDecision},
		{`{"terms":["1","2","3","4","5","6","7"]}`, ErrLookupDecision},
		{`{"answer":"结论","references":[1,1]}`, ErrLookupReferences},
		{`{"answer":"结论","references":[3]}`, ErrLookupReferences},
	} {
		if decision, err := DecodeLookup(tc.reply, 2); !errors.Is(err, tc.want) || !reflect.DeepEqual(decision, LookupDecision{}) {
			t.Errorf("DecodeLookup(%q) = %+v, %v; want %v", tc.reply, decision, err, tc.want)
		}
	}
}

func TestLookupBatchPlan(t *testing.T) {
	want := [][]string{{"报价有效期"}, {"sku", "延期"}, {"到期日"}}
	plan, err := DecodeLookupBatches(`{"searchBatches":[[" 报价有效期 "],["SKU","延期"],["到期日"]]}`)
	if err != nil || !reflect.DeepEqual(plan, want) {
		t.Fatalf("unexpected normalized plan: %#v %v", plan, err)
	}
	for _, raw := range []string{
		`{"searchBatches":[["期限"]]}`,
		`{"searchBatches":[["一"],["二"],["三"],["四"]]}`,
		`{"searchBatches":[[],["延期"]]}`,
		`{"searchBatches":[["A"],[" a "]]}`,
		`{"searchBatches":[["一","二","三","四","五","六"],["七"]]}`,
		`{"searchBatches":[["一"],["二\n三"]]}`,
		`{"searchBatches":[["期限"],["延期"]],"question":"是否延期"}`,
		`{"searchBatches":[["期限"],["延期"]],"operation":"delegate"}`,
		`{"searchBatches":[["期限"],["延期"]],"searchBatches":[["一"],["二"]]}`,
		`{"searchBatches":[["期限"],["延期"]]`,
	} {
		if plan, err := DecodeLookupBatches(raw); !errors.Is(err, ErrLookupDecision) || plan != nil {
			t.Fatalf("accepted invalid plan %s: %#v %v", raw, plan, err)
		}
	}
}

func TestCalculationDecisionAllowsOnlyNumberedOperands(t *testing.T) {
	valid := `{"calculation":{"operation":"difference","operands":[2,1]}}`
	formula, err := DecodeCalculation(valid, 2)
	if err != nil || formula.Operation != "difference" || !reflect.DeepEqual(formula.Operands, []int{2, 1}) {
		t.Fatalf("valid ordered operands rejected: %+v %v", formula, err)
	}
	for _, raw := range []string{
		`{"calculation":{"operation":"sum","operation":"difference","operands":[1,2]}}`,
		`{"calculation":{"operation":"sum","operands":[1,2],"amount":800}}`,
		`{"calculation":{"operation":"sum","operands":[1,2]},"question":"补金额"}`,
		`{"calculation":{"operation":"sum","operands":[1,1]}}`,
		`{"calculation":{"operation":"difference","operands":[2,3]}}`,
		`{"calculation":{"operation":"sum","operands":[null,2]}}`,
		valid + valid,
	} {
		if _, err := DecodeCalculation(raw, 2); !errors.Is(err, ErrLookupDecision) {
			t.Errorf("invalid calculation was accepted: %s", raw)
		}
	}
}
