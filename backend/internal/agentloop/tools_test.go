package agentloop

import (
	"alliance/internal/harness"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestTrustedFinancialAggregationUsesPinnedSources(t *testing.T) {
	var calls atomic.Int32
	workbook := sampleWorkbook(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in struct {
			Operation string
			Config    map[string]string
			Sources   []knowledge.InputSource
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Operation != "aggregate" || in.Config["group"] != "cost_center" ||
			in.Config["amount"] != "amount" || in.Config["filterValue"] != "approved" || len(in.Sources) != 1 || in.Sources[0].ID != "pinned" || in.Sources[0].Version != 7 {
			t.Errorf("unexpected tool request: %+v, %v", in, err)
		}
		json.NewEncoder(w).Encode(resources.Reply{Text: "总金额：27.00", File: &resources.File{
			Name: "deliverable.xlsx", Mime: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: workbook,
		}})
	}))
	defer mock.Close()
	t.Setenv("CONNECTOR_URL", mock.URL)
	t.Setenv("CONNECTOR_TOKEN", "unit-test")
	request := toolWork{name: "aggregate_csv", args: `{"group":"cost_center","amount":"amount","filterColumn":"state","filterValue":"approved"}`,
		sources: []knowledge.InputSource{{ID: "pinned", Version: 7, Text: "cost_center,amount,state\noperations,27,approved"}}}
	result, err := invokeTrusted(t.Context(), request)
	if err != nil || result.File == nil || result.Text != "总金额：27.00" || calls.Load() != 1 || lookupTool(request.name).risk != harness.FileGeneration {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	request.args = `{"group":"cost_center","amount":"amount","sources":[{"id":"private"}]}`
	if _, err := invokeTrusted(t.Context(), request); !errors.Is(err, harness.ErrToolArguments) || calls.Load() != 1 {
		t.Fatalf("model supplied an unauthorized source override: %d %v", calls.Load(), err)
	}
}
