package identity

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

func TestDepartureTransfersPendingWorkAndPrivateAssetsAtomically(t *testing.T) {
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
	admin, leaving, successor := uuid.NewString(), uuid.NewString(), uuid.NewString()
	a, b, p1, p2 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	body, hashErr := bcrypt.GenerateFromPassword([]byte("fixture-admin-password"), bcrypt.MinCost)
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	exec(`insert into members(id,email,display_name,password_hash,role)
	  values($1,$2,'Admin',$7,'admin'),($3,$4,'Leaving','unused','member'),($5,$6,'Successor','unused','member')`,
		admin, admin+"@example.test", leaving, leaving+"@example.test", successor, successor+"@example.test", string(body))
	exec("insert into teams(id,name) values($1,$1::uuid::text),($2,$2::uuid::text)", a, b)
	exec("insert into team_members(team_id,member_id) values($1,$3),($2,$3),($1,$4),($2,$4),($1,$5)", a, b, admin, leaving, successor)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,stage,plan_version) values($1,$3,$4,$3,'Owned','review',1),($2,$5,$6,$3,'Reviewed','review',1)",
		p1, p2, leaving, a, admin, b)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$3,true),($2,$3,true),($2,$4,true) on conflict do nothing", p1, p2, leaving, admin)
	for _, project := range []string{p1, p2} {
		exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Fixture',$2)", project, leaving)
	}
	run, artifact, feedback, comment := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'completed',$4,$1::uuid::text,$5,1)",
		run, p2, leaving, b, web.Hash("fixture"))
	exec("insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,1,'Official result')", artifact, p2, run)
	exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,'Fixture','Fixture')", artifact)
	exec(`insert into feedback(id,artifact_id,base_version,author,reviewer,kind,position,body,state)
	 values($1,$2,1,$3,$4,'change','paragraph 1','Fixture change','required')`, feedback, artifact, admin, leaving)
	exec(`insert into artifact_comments(id,artifact_id,version,author_id,anchor_kind,anchor_index,body)
	 values($1,$2,1,$3,'paragraph',1,'Fixture')`, comment, artifact, admin)
	exec("insert into artifact_comment_mentions(comment_id,member_id) values($1,$2)", comment, leaving)
	agent, draft, release, automation := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into agents(id,owner,team_id) values($1,$2,$3)", agent, leaving, a)
	exec("insert into studio_drafts(id,owner,team_id,spec) values($1,$2,$3,'{}')", draft, leaving, b)
	exec(`insert into studio_releases(id,draft_id,revision,owner,team_id,agent_scope,state,spec,dependencies,digest)
	 values($1,$2,1,$3,$4,'personal','published','{}','{}',repeat('8',64))`, release, draft, leaving, b)
	exec(`insert into automation_schedules(id,owner,team_id,title,goal_cipher,tier,once_at,time_zone)
	 values($1,$2,$3,'Automatic review',$4,'default',now()+interval '1 day','UTC')`, automation, leaving, a, []byte("fixture"))
	exec("insert into im_bindings(connection,external_user,member_id) values('ding-fixture',$1::uuid::text,$1)", leaving)
	exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,$3)", web.Hash("old-session-"+leaving), leaving, time.Now().Add(time.Hour))

	server := Server{DB: db}
	adminUser := Member{ID: admin}
	preview := func() string {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/departure-handoff?q=Successor", nil)
		r.SetPathValue("id", leaving)
		if err := server.departurePreview(w, r, adminUser); err != nil {
			t.Fatal(err)
		}
		return w.Body.String()
	}
	if output := preview(); strings.Contains(output, successor) || !strings.Contains(output, `"projects":2`) {
		t.Fatalf("ineligible team or missing scope: %s", output)
	}
	change := func(expected int) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"Role": "member", "Enabled": false, "Reason": "Approved handoff",
			"Employment": "depart", "Password": "fixture-admin-password", "Successor": successor,
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/member", strings.NewReader(string(raw)))
		r.SetPathValue("id", leaving)
		w := httptest.NewRecorder()
		err = server.updateMember(w, r, adminUser)
		var issue web.Problem
		if errors.As(err, &issue) && issue.Status == expected {
			return
		}
		if err != nil || w.Code != expected {
			t.Fatalf("departure returned %d, want %d: %s, %v", w.Code, expected, w.Body, err)
		}
	}
	change(409)
	var stillOwner string
	if err := db.QueryRow(ctx, "select owner from projects where id=$1", p1).Scan(&stillOwner); err != nil || stillOwner != leaving {
		t.Fatalf("partial transfer escaped: %s, %v", stillOwner, err)
	}
	exec("insert into team_members(team_id,member_id) values($1,$2)", b, successor)
	if output := preview(); !strings.Contains(output, successor) {
		t.Fatalf("eligible successor missing: %s", output)
	}
	change(204)
	var owned, reviews, assets, receipts, unreleasedEdges int
	var oldActive, privatePublished bool
	err = db.QueryRow(ctx, `select
	 (select count(*) from projects where id=$3 and owner=$2),
	 (select count(*) from projects where id in($3,$4) and final_reviewer=$2)
	  +(select count(*) from feedback where id=$5 and reviewer=$2 and revision=2)
	  +(select count(*) from artifact_comment_mentions where (comment_id,member_id)=($6,$2) and transferred_at is null),
	 (select count(*) from agents where id=$7 and owner=$2)
	  +(select count(*) from studio_drafts where id=$8 and owner=$1 and privacy_state='sealed_private')
	  +(select count(*) from automation_schedules where id=$10 and owner=$2 and state='active'),
	 (select count(*) from staff_departure_batches where departing=$1 and successor=$2),
	 (select count(*) from team_members where member_id=$1)+(select count(*) from sessions where member_id=$1)
	  +(select count(*) from im_bindings where member_id=$1),
	 (select enabled from members where id=$1),studio_release_usable($9,$2)`,
		leaving, successor, p1, p2, feedback, comment, agent, draft, release, automation).
		Scan(&owned, &reviews, &assets, &receipts, &unreleasedEdges, &oldActive, &privatePublished)
	if err != nil || owned != 1 || reviews != 4 || assets != 3 || receipts != 1 || unreleasedEdges != 0 || oldActive || privatePublished {
		t.Fatalf("batch did not settle: owned=%d review=%d assets=%d receipts=%d access=%d oldActive=%t publication=%t err=%v",
			owned, reviews, assets, receipts, unreleasedEdges, oldActive, privatePublished, err)
	}
	var author string
	if err := db.QueryRow(ctx, "select owner from studio_releases where id=$1", release).Scan(&author); err != nil || author != leaving {
		t.Fatalf("publication provenance changed: %s, %v", author, err)
	}
}

func TestTeamTransferScopesPreviewAndHandoff(t *testing.T) {
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
	admin, member, next, outsider := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	team, retained, handed, unrelated := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	hash, err := bcrypt.GenerateFromPassword([]byte("transfer-fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into members(id,email,display_name,password_hash,role)
	 values($1,$1::uuid::text||'@example.test','Admin',$5,'admin'),($2,$2::uuid::text||'@example.test','Employee','','member'),
	 ($3,$3::uuid::text||'@example.test','Team successor','','member'),($4,$4::uuid::text||'@example.test','Other successor','','member')`, admin, member, next, outsider, string(hash))
	exec(`insert into teams(id,name) values($1,$1::uuid::text),($2,$2::uuid::text)`, team, retained)
	exec(`insert into team_members(team_id,member_id) values($1,$3),($1,$4),($2,$3),($2,$5)`, team, retained, member, next, outsider)
	exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$3,$4,$3,'Handoff'),($2,$3,$5,$3,'Retained')`, handed, unrelated, member, team, retained)
	exec(`insert into project_memberships(project_id,member_id,can_review) values ($1,$3,true),($2,$3,true)`, handed, unrelated, member)
	private, privateKB, privateDraft, sharedKB, conn, event := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var personalSpace, teamSpace string
	if err := db.QueryRow(ctx, "select id from spaces where owner_member_id=$1", member).Scan(&personalSpace); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "select id from spaces where team_id=$1", team).Scan(&teamSpace); err != nil {
		t.Fatal(err)
	}
	exec("insert into projects(id,owner,team_id,final_reviewer,title,space_id) values($1,$2,$3,$2,'Private session',$4)", private, member, retained, personalSpace)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", private, member)
	exec("insert into studio_drafts(id,owner,team_id,spec) values($1,$2,$3,'{\"name\":\"Private draft\"}')", privateDraft, member, retained)
	exec(`insert into kb_collections(id,space_id,name,sync_provider,acl_mode,maintainer) values
	 ($1,$2,'Private documents','manual','space',$5),($3,$4,'Team guides','manual','space',$5)`, privateKB, personalSpace, sharedKB, teamSpace, member)
	exec(`insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,maintainer)
	 values($1,$2,'internal','openapi_rest','openapi','Business','https://fixture.example.test','obo',$3)`, conn, teamSpace, member)
	exec("insert into business_connector_bindings(connector_id,space_id,owner_id) values($1,$2,$3)", conn, teamSpace, member)
	department := uuid.NewString()
	exec("insert into departments(id,name) values($1,$1::uuid::text)", department)
	exec("update members set department_id=$2 where id=$1", member, department)
	exec(`insert into business_obo_identities(connector_id,member_id,department_id,issuer_subject,employee_no,dept_code,verified_by)
	 values($1,$2,$3,'employee','employee','department',$4)`, conn, member, department, admin)
	exec(`insert into business_obo_grants(connector_id,member_id,connector_revision,identity_revision,token_cipher,expires_at)
	 values($1,$2,1,1,'fixture',clock_timestamp()+interval '1 hour')`, conn, member)
	exec(`insert into event_triggers(id,space_id,connector_id,name,webhook_token_hash,hmac_cipher,filter_cel,dedup_cel,cooldown_seconds,maintainer,notify_targets)
	 values($1,$2,$3,'Business event',gen_random_bytes(32),'fixture','true','event.id',60,$4,$5)`,
		event, teamSpace, conn, member, []map[string]string{{"channel": "feishu", "memberId": member}})
	batch := uuid.NewString()
	exec(`insert into event_trigger_batches(id,trigger_id,trigger_revision,ready_at,sample_cipher)
	 values($1,$2,1,clock_timestamp()+interval '1 hour','fixture')`, batch, event)
	exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,clock_timestamp()+interval '1 hour')", web.Hash(member), member)
	run, policy, approval := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Review pending write',$2)", handed, member)
	exec("update projects set plan_version=1 where id=$1", handed)
	exec(`insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,'running',$4,$1::uuid::text,$5,4)`, run, handed, next, team, web.Hash(run))
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	exec("insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools','fixture')", run)
	exec(`insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint)
	 values($1,1,1,'propose_business_write','pending','fixture','fixture')`, run)
	exec("insert into business_mutation_policies(id,connector_id,space_id,label,spec,maintainer) values($1,$2,$3,'Approval','{}',$4)", policy, conn, teamSpace, member)
	exec(`insert into pending_system_mutations(id,run_id,turn,position,space_id,connector_id,policy_id,policy_revision,connector_revision,
	 approver_id,action_name,payload_cipher,proposal_fingerprint,status,approved_by,decision_channel,decided_at)
	 values($1,$2,1,1,$3,$4,$5,1,1,$6,'Business approval','fixture',$7,'approved',$6,'web',clock_timestamp())`,
		approval, run, teamSpace, conn, policy, member, web.Hash(approval))
	server := Server{DB: db}
	w := httptest.NewRecorder()
	r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/departure-handoff?mode=transfer&teamId="+team, nil)
	r.SetPathValue("id", member)
	if err := server.departurePreview(w, r, Member{ID: admin}); err != nil || w.Code != 200 {
		t.Fatalf("preview status %d: %v", w.Code, err)
	}
	var preview struct {
		Projects              int
		Mode, AccountRevision string
		Successors            []struct{ ID string }
		Personal              struct{ Projects, Drafts, Collections, OBOGrants int }
		Shared                struct{ Collections, BusinessBindings, Events, Approvals int }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil || preview.Mode != "transfer" || preview.Projects != 1 ||
		len(preview.Successors) != 1 || preview.Successors[0].ID != next || preview.AccountRevision == "" ||
		preview.Personal.Projects != 1 || preview.Personal.Drafts != 1 || preview.Personal.Collections != 1 || preview.Personal.OBOGrants != 1 ||
		preview.Shared.Collections != 1 || preview.Shared.BusinessBindings != 1 || preview.Shared.Events != 1 || preview.Shared.Approvals != 1 {
		t.Fatalf("unexpected scoped preview: %s (%v)", w.Body, err)
	}
	patch := func(successor, revision string, want int) {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"role": "member", "enabled": true, "employment": "transfer", "teamId": team, "successor": successor,
			"expected": revision, "reason": "Moved to another team", "password": "transfer-fixture-password",
		})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequestWithContext(ctx, http.MethodPatch, "/member", strings.NewReader(string(body)))
		r.SetPathValue("id", member)
		w := httptest.NewRecorder()
		err = server.updateMember(w, r, Member{ID: admin})
		var issue web.Problem
		if errors.As(err, &issue) && issue.Status == want {
			return
		}
		if err != nil || w.Code != want {
			t.Fatalf("team transfer status %d, want %d: %v", w.Code, want, err)
		}
	}
	patch(outsider, preview.AccountRevision, 409)
	patch(next, "0", 409)
	patch(next, preview.AccountRevision, 204)
	patch(next, preview.AccountRevision, 409)
	var held, moved, remaining, receipt bool
	err = db.QueryRow(ctx, `select
	 exists(select from members where id=$1 and enabled and departed_at is null),
	 exists(select from projects where id=$3 and owner=$2 and final_reviewer=$2)
	  and not exists(select from project_members where (project_id,member_id)=($3,$1))
	  and not exists(select from team_members where (team_id,member_id)=($5,$1)),
	 exists(select from projects where id=$4 and owner=$1 and final_reviewer=$1)
	  and exists(select from project_members where (project_id,member_id)=($4,$1)),
	 exists(select from staff_departure_batches where departing=$1 and successor=$2 and transferred->>'teamId'=$5::uuid::text)`,
		member, next, handed, unrelated, team).Scan(&held, &moved, &remaining, &receipt)
	if err != nil || !held || !moved || !remaining || !receipt {
		t.Fatalf("scoped transfer account=%t moved=%t retained=%t receipt=%t: %v", held, moved, remaining, receipt, err)
	}
	var sealed, transferred, reapproved bool
	err = db.QueryRow(ctx, `select
	  (select privacy_state='sealed_private' from projects where id=$2) and not project_space_member($2,$1)
	  and (select privacy_state='sealed_private' and not enabled from kb_collections where id=$3)
	  and (select privacy_state='sealed_private' and owner=$1 from studio_drafts where id=$4)
	  and not exists(select from business_obo_grants where member_id=$1)
	  and not exists(select from sessions where member_id=$1),
	  (select maintainer=$5 from kb_collections where id=$6)
	  and (select owner_id=$5 from business_connector_bindings where connector_id=$7)
	  and (select t.maintainer=$5 and b.trigger_revision=t.revision and t.notify_targets->0->>'memberId'=$5::uuid::text
	   from event_triggers t join event_trigger_batches b on b.trigger_id=t.id where b.id=$8),
	  (select approver_id=$5 and status='pending' and approved_by is null from pending_system_mutations where id=$9)`,
		member, private, privateKB, privateDraft, next, sharedKB, conn, batch, approval).Scan(&sealed, &transferred, &reapproved)
	if err != nil || !sealed || !transferred || !reapproved {
		t.Fatalf("asset handoff result: private sealed=%t shared transfer=%t approval reset=%t %v", sealed, transferred, reapproved, err)
	}
}
