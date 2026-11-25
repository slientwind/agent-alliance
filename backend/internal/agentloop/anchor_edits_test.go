package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAnchorTextAndCSVPreserveOtherContent(t *testing.T) {
	ctx := t.Context()
	text, err := textAnchor([]byte("first\r\n\r\nsecond\r\n\r\n  third"), 2)
	if err != nil || len(text.values) != 1 || text.values[0] != "second" {
		t.Fatalf("incorrect paragraph: %+v, %v", text.values, err)
	}
	result, err := text.replace(ctx, []string{"new"})
	if err != nil || string(result) != "first\r\n\r\nnew\r\n\r\n  third" {
		t.Fatalf("unexpected replacement: %q, %v", result, err)
	}
	row, err := tabularAnchor([]byte("A,B\r\n\r\n\"old,1\",2\r\nkeep,\" new \""), 2, false)
	if err != nil || len(row.values) != 2 || row.values[0] != "old,1" {
		t.Fatalf("incorrect row: %+v, %v", row.values, err)
	}
	result, err = row.replace(ctx, []string{"new", "-3"})
	if err != nil || string(result) != "A,B\r\n\r\nnew,-3\r\nkeep,\" new \"" {
		t.Fatalf("unexpected row replacement: %q, %v", result, err)
	}
	if _, err := row.replace(ctx, []string{"=1+2", "-3"}); err == nil {
		t.Fatal("active table content was accepted")
	}
}

func TestCommentMentionsAndBoundedAnchorWorkflowPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a4", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	project, team, artifact, sourceRun, provider, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	owner, reviewer, outsider := uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := map[string]string{}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	for _, person := range []string{owner, reviewer, outsider} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", person, person+"@example.test")
		exec("insert into team_members(team_id,member_id) values($1,$2)", team, person)
		tokens[person] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[person]), person)
	}
	exec("insert into projects(id,owner,team_id,final_reviewer,title,stage,plan_version) values($1,$2,$3,$4,'Fixture','review',1)", project, owner, team, reviewer)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,true)", project, owner, reviewer)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Original request',$2)", project, owner)
	exec("insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'completed',$4,'anchor-fixture',$5,1)", sourceRun, project, owner, team, web.Hash("source"))
	exec("insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,1,'Fixture report')", artifact, project, sourceRun)
	exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,$2,'Fixture')", artifact, "hidden-prefix\n\nchange-me\n\nhidden-suffix")
	exec("insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://fixture.example.test/v1')", provider)
	exec("insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens) values($1,$2,'fixture',array['text','tool_call'],16384,4096)", model, provider)
	exec("insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny) values($1,now(),0,0,0,0)", model)
	exec("insert into model_tier_routes(tier,model_id,priority) values('default',$1,32767)", model)
	defer db.Exec(context.Background(), "delete from model_tier_routes where model_id=$1", model)
	exec("insert into channel_links(tenant,open_id,member_id) values($1,'fixt-feishu',$2)", team, reviewer)
	exec("insert into im_bindings(connection,external_user,member_id) values($1,'fixt-ding',$2)", team, reviewer)
	api := http.NewServeMux()
	general := Server{DB: db, Vault: vault}
	projects.Server{DB: db, Vault: vault, FeishuMentions: team, DingMentions: team, QueueAnchorEdit: general.QueueAnchorEdit}.
		Routes(api, identity.Server{DB: db})
	commentsPath := "/api/projects/" + project + "/artifacts/" + artifact + "/comments"
	request := func(method, path, member, key, body string, status int) string {
		t.Helper()
		in := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		in.Header.Set("Idempotency-Key", key)
		in.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[member]})
		out := httptest.NewRecorder()
		api.ServeHTTP(out, in)
		if out.Code != status {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, out.Code, status, out.Body)
		}
		return out.Body.String()
	}
	payload := func(version string, mentions []string, agent bool) string {
		encoded, err := json.Marshal(map[string]any{"expected_version": json.RawMessage(version), "anchor": map[string]any{"kind": "paragraph", "index": 2}, "body": "Revise this", "agent": agent, "mentions": mentions})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	request("POST", commentsPath, owner, "invalid", payload("1", []string{outsider}, false), 409)
	var reply struct {
		ID       string
		AgentRun struct{ ID string }
	}
	body := payload("1", []string{reviewer, reviewer}, true)
	created := request("POST", commentsPath, owner, "create", body, 201)
	if err := json.Unmarshal([]byte(created), &reply); err != nil || reply.ID == "" || reply.AgentRun.ID == "" {
		t.Fatalf("unexpected response: %s, %v", created, err)
	}
	duplicate := request("POST", commentsPath, owner, "create", body, 200)
	if duplicate != created {
		t.Fatalf("retry returned a different comment: %s", duplicate)
	}
	var deliveries int
	if err := db.QueryRow(ctx, "select count(*) from artifact_comment_deliveries where comment_id=$1", reply.ID).Scan(&deliveries); err != nil || deliveries != 2 {
		t.Fatalf("expected two pinned deliveries: %d, %v", deliveries, err)
	}
	if inbox := request("GET", "/api/projects/review-inbox?project="+project, reviewer, "", "", 200); !strings.Contains(inbox, reply.ID) {
		t.Fatalf("missing mentioned comment: %s", inbox)
	}
	calls := 0
	revokeAfterCall := false
	a := Activities{DB: db, Vault: vault, Gateway: testGateway(func(_ context.Context, actor llmgateway.CallIdentity, in llmgateway.Request) (llmgateway.RoutedResult, error) {
		calls++
		if len(in.Tools) != 0 || len(in.Messages) != 2 || strings.Contains(in.Messages[1].Content, "hidden-") ||
			actor.TeamID != team || !strings.Contains(in.Messages[1].Content, "Revise this") {
			t.Errorf("model received unscoped inputs or tools: %v", in)
		}
		if revokeAfterCall {
			exec("delete from team_members where team_id=$1 and member_id=$2", team, actor.MemberID)
		}
		return llmgateway.RoutedResult{Result: llmgateway.Result{Reply: resources.Reply{Text: `{"parts":["revised"]}`}}}, nil
	})}
	advance := func(run string) {
		t.Helper()
		result, err := a.AdvanceTurn(ctx, TurnRef{RunID: run, Turn: 1})
		if err != nil || !result.Done {
			t.Fatalf("anchor did not finish: %+v, %v", result, err)
		}
	}
	advance(reply.AgentRun.ID)
	advance(reply.AgentRun.ID)
	var revised string
	if err := db.QueryRow(ctx, "select v.body from artifacts a join artifact_versions v on (v.artifact_id,v.version)=(a.id,a.current_version) where a.id=$1", artifact).
		Scan(&revised); err != nil || revised != "hidden-prefix\n\nrevised\n\nhidden-suffix" || calls != 1 {
		t.Fatalf("unexpected current version %q, model calls %d, error %v", revised, calls, err)
	}
	focused := request("GET", commentsPath+"?version=1&focus="+reply.ID, reviewer, "", "", 200)
	if !strings.Contains(focused, `"version":2`) || !strings.Contains(focused, `"status":"completed"`) {
		t.Fatalf("old anchor did not expose the completed edit: %s", focused)
	}
	old := reply.ID
	created = request("POST", commentsPath, reviewer, "stale", payload("2", nil, true), 201)
	if err := json.Unmarshal([]byte(created), &reply); err != nil {
		t.Fatal(err)
	}
	revokeAfterCall = true
	advance(reply.AgentRun.ID)
	var state string
	var version int
	if err := db.QueryRow(ctx, "select r.status,a.current_version from runs r cross join artifacts a where r.id=$1 and a.id=$2", reply.AgentRun.ID, artifact).
		Scan(&state, &version); err != nil || state != "failed" || version != 2 || calls != 2 {
		t.Fatalf("revoked run changed the artifact: %s, v%d, %d calls, %v", state, version, calls, err)
	}
	request("PATCH", commentsPath+"/"+old, owner, "", `{"expected_revision":1,"resolved":true}`, 204)
	if inbox := request("GET", "/api/projects/review-inbox?project="+project, reviewer, "", "", 200); strings.Contains(inbox, old) {
		t.Fatalf("resolved mention remained in inbox: %s", inbox)
	}
}
