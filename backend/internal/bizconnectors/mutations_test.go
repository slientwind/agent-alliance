package bizconnectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"alliance/internal/database"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestSystemMutationNeedsApprovalAndReconcilesWithoutResending(t *testing.T) {
	db := connectorTestDB(t)
	space, actors := connectorSpace(t, db)
	ctx := t.Context()
	vault, err := resources.NewVault(strings.Repeat("b3", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var team string
	if err := db.QueryRow(ctx, "select team_id from spaces where id=$1", space).Scan(&team); err != nil {
		t.Fatal(err)
	}
	project, run, connector, policyID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$4,'Review change',1)", project, actors[0].ID, team, actors[1].ID)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,true)", project, actors[0].ID, actors[1].ID)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Write status',$2)", project, actors[0].ID)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,'running',$4,$1::uuid::text,$5,4)`, run, project, actors[0].ID, team, web.Hash(run))
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	exec("insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools','fixture')", run)
	exec(`insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint) values
	 ($1,1,1,'propose_business_write','one','fixture','fixture-1'),($1,1,2,'propose_business_write','two','fixture','fixture-2')`, run)
	methods := []Operation{
		{Name: "original", Description: "Original", Method: "GET", Path: "/orders/{record}", Risk: "read", Parameters: map[string]Parameter{"record": {Type: "string", Location: "path", Required: true}}},
		{Name: "change", Description: "Change", Method: "PATCH", Path: "/orders/{record}", Risk: "write", Parameters: map[string]Parameter{
			"record": {Type: "string", Location: "path", Required: true}, "status": {Type: "string", Location: "body", Required: true},
		}},
		{Name: "receipt", Description: "Receipt", Method: "GET", Path: "/receipts/{key}", Risk: "read", Parameters: map[string]Parameter{"key": {Type: "string", Location: "path", Required: true}}},
	}
	exec(`insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,operations,maintainer)
	 values($1,$2,'erp','openapi_rest','openapi','Write fixture','https://erp.example.test','service',$3,$4)`, connector, space, methods, actors[0].ID)
	spec := MutationSpec{PreviewOperation: "original", WriteOperation: "change", ReconcileOperation: "receipt", RecordParam: "record", ReconcileParam: "key",
		ReconcileKeyField: "idempotency_key", ReceiptField: "reference", RevisionField: "etag", FieldLabels: map[string]string{"status": "审批状态"}}
	var writes atomic.Int32
	var acceptedKey atomic.Pointer[string]
	var correctReceipt atomic.Bool
	s := Server{DB: db, Vault: vault, HTTP: businessFixture(func(r *http.Request) (*http.Response, error) {
		var result string
		switch {
		case r.Method == "GET" && r.URL.Path == "/orders/order-A":
			result = `{"status":"draft","etag":"\"order-v17\""}`
		case r.Method == "PATCH" && r.URL.Path == "/orders/order-A":
			writes.Add(1)
			key := r.Header.Get("Idempotency-Key")
			body, _ := io.ReadAll(r.Body)
			if !validID(key) || r.Header.Get("If-Match") != `"order-v17"` || string(body) != `{"status":"released"}` {
				t.Errorf("write discarded unique key, original version or reviewed body: %q %s", r.Header.Get("If-Match"), body)
			}
			acceptedKey.Store(&key)
			// The fixture accepted the transfer but the transport lost the reply.
			return nil, context.DeadlineExceeded
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/receipts/"):
			key := "unrelated"
			if correctReceipt.Load() {
				key = strings.TrimPrefix(r.URL.Path, "/receipts/")
			}
			result = `{"idempotency_key":"` + key + `","reference":"ERP-RECEIPT-347"}`
		default:
			t.Errorf("unexpected write request: %s %s", r.Method, r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result))}, nil
	})}
	configure, _ := json.Marshal(MutationPolicy{Connector: connector, Label: "Release purchase order", Spec: spec, Enabled: true})
	r := httptest.NewRequest("PUT", "/", strings.NewReader(string(configure))).WithContext(ctx)
	r.SetPathValue("space", space)
	r.SetPathValue("policy", policyID)
	if err := s.configureMutationPolicy(httptest.NewRecorder(), r, actors[0]); err != nil {
		t.Fatal(err)
	}
	input := MutationRequest{Policy: policyID, Record: "order-A", Values: map[string]any{"status": "released"}}
	ref := MutationRef{Run: run, Turn: 1, Position: 1}
	preview, err := s.PrepareMutation(ctx, ref, input)
	if err != nil || preview.Status != "pending" || writes.Load() != 0 {
		t.Fatalf("write before approval: %+v %v", preview, err)
	}
	r = httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	r.SetPathValue("id", project)
	w := httptest.NewRecorder()
	if err := s.listMutations(w, r, actors[1]); err != nil || !strings.Contains(w.Body.String(), `"canDecide":true`) ||
		!strings.Contains(w.Body.String(), `"before":{"status":"draft"}`) || !strings.Contains(w.Body.String(), `"after":{"status":"released"}`) {
		t.Fatalf("invalid confirmation preview: %s %v", w.Body, err)
	}
	if duplicate, err := s.PrepareMutation(ctx, MutationRef{Run: run, Turn: 1, Position: 2}, input); err != nil ||
		duplicate.ID != preview.ID || duplicate.Status != "duplicate" {
		t.Fatalf("new tool call forked the same mutation: %+v %v", duplicate, err)
	}
	decision := func(actor, via string, accepted bool) error {
		return database.WithTx(ctx, db, func(tx pgx.Tx) error {
			return DecideMutation(ctx, tx, project, preview.ID, actor, via, 1, accepted)
		})
	}
	if err := decision(actors[0].ID, "web", true); err == nil {
		t.Fatal("requester bypassed final reviewer")
	}
	if err := decision(actors[1].ID, "feishu", true); err != nil {
		t.Fatal(err)
	}
	if err := decision(actors[1].ID, "web", true); err != nil {
		t.Fatalf("second tap in web did not resolve the same approval: %v", err)
	}
	if err := decision(actors[1].ID, "web", false); err == nil {
		t.Fatal("opposite replay overwrote the decision")
	}
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			if _, err := s.StepMutation(ctx, ref); err != nil {
				t.Errorf("concurrent mutation attempt: %v", err)
			}
		})
	}
	group.Wait()
	if writes.Load() != 1 || acceptedKey.Load() == nil {
		t.Fatalf("actual write count=%d", writes.Load())
	}
	for _, confirmed := range []bool{false, true} {
		correctReceipt.Store(confirmed)
		exec("update pending_system_mutations set next_check_at=clock_timestamp() where id=$1", preview.ID)
		if busy, err := s.reconcileMutation(ctx); !busy || err != nil {
			t.Fatalf("reconciliation failed: %v %v", busy, err)
		}
		out, err := s.StepMutation(ctx, ref)
		if err != nil || (out.Receipt != "") != confirmed || confirmed && (out.Receipt != "ERP-RECEIPT-347" || out.Status != "succeeded") {
			t.Fatalf("unexpected receipt state: %+v %v", out, err)
		}
	}
	if writes.Load() != 1 {
		t.Fatalf("reconciliation re-sent the write %d times", writes.Load())
	}
}
