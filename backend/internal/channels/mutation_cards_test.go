package channels

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/bizconnectors"
	"alliance/internal/database"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFeishuBusinessApprovalCardRequiresCurrentPrivateBinding(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx := t.Context()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("d2", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	tenant, member, team, project, run, connector, policyID, mutationID := uuid.NewString(), uuid.NewString(), uuid.NewString(),
		uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','Reviewer','','member')", member)
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, member)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Mutation approval',1)", project, member, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, member)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Change order',$2)", project, member)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,'running',$4,$1::uuid::text,$5,3)`, run, project, member, team, web.Hash(run))
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	exec("insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools','fixture')", run)
	exec(`insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint)
	 values($1,1,1,'propose_business_write','step','fixture','fixture')`, run)
	exec("insert into channel_links(tenant,open_id,member_id) values($1,'ou_mutation_fixture',$2)", tenant, member)
	exec(`insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,operations,maintainer)
	 select $1,space_id,'erp','openapi_rest','openapi','Orders','https://store.example.test','service',
	 '[{"name":"before","method":"GET","path":"/orders/{id}","risk":"read","parameters":{"id":{"type":"string","location":"path","required":true}}},
	   {"name":"change","method":"PATCH","path":"/orders/{id}","risk":"write","parameters":{"id":{"type":"string","location":"path","required":true},"status":{"type":"string","location":"body","required":true}}},
	   {"name":"reconcile","method":"GET","path":"/receipts/{key}","risk":"read","parameters":{"key":{"type":"string","location":"path","required":true}}}]',$3
	 from projects where id=$2`, connector, project, member)
	exec(`insert into business_mutation_policies(id,connector_id,space_id,label,spec,maintainer)
	 select $1,$2,space_id,'Release order',
	 '{"previewOperation":"before","writeOperation":"change","reconcileOperation":"reconcile","recordParam":"id","reconcileParam":"key",
	   "reconcileKeyField":"key","receiptField":"number","revisionField":"version","fields":{"status":"状态"}}',$4 from projects where id=$3`, policyID, connector, project, member)
	body, err := vault.Seal(`{"record":"A5","before":{"status":"draft"},"after":{"status":"released"},"recordVersion":"17"}`, "business-mutation/"+mutationID)
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into pending_system_mutations(id,run_id,turn,position,space_id,connector_id,policy_id,policy_revision,connector_revision,
	 approver_id,action_name,payload_cipher,proposal_fingerprint)
	 select $1,$2,1,1,space_id,$3,$4,1,1,$5,'Release order',$6,$8 from projects where id=$7`,
		mutationID, run, connector, policyID, member, body, project, web.Hash(mutationID))
	f := Feishu{DB: db, Vault: vault, Public: "https://alliance.example.test", Tenant: tenant}
	calls := 0
	if busy, err := f.notifyMutation(ctx, func(_ context.Context, ticket mutationTicket, content string) (string, error) {
		calls++
		if ticket.ID != mutationID || ticket.Recipient != "ou_mutation_fixture" || !strings.Contains(content, "draft → released") ||
			!strings.Contains(content, "确认写回") {
			t.Fatalf("invalid private mutation card: %+v %s", ticket, content)
		}
		return "om_review_fixture", nil
	}); err != nil || !busy || calls != 1 {
		t.Fatalf("confirmed card handoff: %d %v %v", calls, busy, err)
	}
	var event cardActionEvent
	event.Host, event.Action.Tag, event.Operator.Tenant = "im_message", "button", tenant
	event.Context.Message = "om_review_fixture"
	event.Action.Value.Mutation, event.Action.Value.Expected = mutationID, 1
	approved := true
	event.Action.Value.Accept = &approved
	click := func(sender string, kind string) {
		t.Helper()
		event.Operator.Open = sender
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx)
		if err := f.cardAction(w, r, event); err != nil || !strings.Contains(w.Body.String(), `"type":"`+kind+`"`) {
			t.Fatalf("card callback response: %s %v", w.Body, err)
		}
	}
	click("other-sender", "warning")
	click("ou_mutation_fixture", "success")
	click("ou_mutation_fixture", "success")
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		return bizconnectors.DecideMutation(ctx, tx, project, mutationID, member, "web", 1, true)
	}); err != nil {
		t.Fatal(err)
	}
	var status, via string
	var version int
	if err := db.QueryRow(ctx, "select status,decision_channel,revision from pending_system_mutations where id=$1", mutationID).
		Scan(&status, &via, &version); err != nil || status != "approved" || via != "feishu" || version != 2 {
		t.Fatalf("callback updated approval multiple times: %s %s %d %v", status, via, version, err)
	}
}
