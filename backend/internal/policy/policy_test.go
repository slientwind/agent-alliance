package policy

import (
	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/web"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectorArgumentsRejectCredentialsAndKeepBusinessNumbers(t *testing.T) {
	if err := JSONFields([]byte(`{"publicNumber":9007199254740993,"key":"column name","data":[{"rows":4}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"filters":[{"client_secret":"example"}]}`, `{"x":"Authorization: Bearer fixture-token"}`} {
		if err := JSONFields([]byte(raw)); !errors.Is(err, ErrDenied) {
			t.Fatalf("argument containing a credential passed: %v", err)
		}
	}
	if got := TextProjection("授权记录开头", 3); got != "授权记〔节选〕" {
		t.Fatalf("invalid projection %q", got)
	}
}

func TestPolicyAuditsACLAndOutboundDataAtTheExecutionBoundary(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	person, team, project, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", person, person+"@example.test")
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, person)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Fixture',1)", project, person, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, person)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Fixture',$2)", project, person)
	exec("insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'running',$4,'fixture',$5,1)",
		run, project, person, team, web.Hash("fixture"))
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	host := "policy-fixture.example.test"
	exec("insert into outbound_host_allowlist(host,port) values($1,443) on conflict do nothing", host)
	p := Policy{DB: db}
	request := Request{Purpose: Search, Run: run, Destination: "https://" + host + "/private/path?identifier=hidden-segment", Approved: true,
		Fields: []Field{{"query", "search for public guidance", Public}, {"source", "private-fixture-statement", Restricted}, {"authorization", "credential-fixture", Public}}}
	check := func(in Request, allowed bool, reason string) Decision {
		t.Helper()
		got, err := p.Decide(ctx, in)
		if err != nil || got.Allowed != allowed || got.Reason != reason {
			t.Fatalf("policy=%+v, err=%v", got, err)
		}
		return got
	}
	projected := check(request, true, "allowed")
	if len(projected.Fields) != 1 || projected.Fields[0].Name != "query" {
		t.Fatalf("unexpected released fields: %+v", projected)
	}
	request.RequireAll = true
	check(request, false, "data_scope_exceeded")
	request.RequireAll = false
	request.Approved = false
	check(request, false, "public_query_required")
	request.Approved = true
	request.Destination = "https://unconfigured.example.test"
	check(request, false, "destination_not_allowed")
	free := int64(0)
	tool := Request{Purpose: Tool, Run: run, ToolGate: &harness.ToolGate{Risk: harness.SystemWrite, Authorized: true, QuotaAvailable: true,
		EstimatedCost: &free, BatchSize: 1}}
	got := check(tool, true, "system_write")
	if got.Autonomy.Level != harness.L2 || got.Autonomy.Action != "review" {
		t.Fatalf("write approval was lost: %+v", got)
	}
	exec("delete from project_memberships where project_id=$1 and member_id=$2", project, person)
	check(tool, false, "access_ended")
	var count int
	var audit string
	if err := db.QueryRow(ctx, `select count(*),string_agg(detail::text,' ') from audit_events where action='policy.decide' and subject=$1`, run).Scan(&count, &audit); err != nil || count != 6 || strings.Contains(audit, "private-fixture") || strings.Contains(audit, "credential-fixture") || strings.Contains(audit, "hidden-segment") {
		t.Fatalf("decision audit did not stay bounded: rows=%d, %s, %v", count, audit, err)
	}
}
