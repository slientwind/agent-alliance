package projects

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTaskCollaborationRolesAndRevocationPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(sql string, values ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, values...); err != nil {
			t.Fatal(err)
		}
	}
	owner, reviewer, nominee, team, project := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := map[string]string{}
	for _, id := range []string{owner, reviewer, nominee} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'测试成员','unused','member')", id, id+"@example.invalid")
		token := web.Token()
		tokens[id] = token
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(token), id)
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'核对采购清单')", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, owner)
	vault, err := resources.NewVault(strings.Repeat("b1", 32))
	if err != nil {
		t.Fatal(err)
	}
	updates, err := StartSessionUpdates(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	projectChanged, releaseChanges := updates.subscribe(project)
	defer releaseChanges()
	unrelated, releaseUnrelated := updates.subscribe(uuid.NewString())
	defer releaseUnrelated()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = Touch(ctx, tx, project, "brief"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-projectChanged:
		t.Fatal("uncommitted state reached another connection")
	case <-time.After(30 * time.Millisecond):
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-projectChanged:
	case <-time.After(time.Second):
		t.Fatal("a committed project change did not wake the event stream")
	}
	select {
	case <-unrelated:
		t.Fatal("a different project received the wakeup")
	default:
	}
	mux := http.NewServeMux()
	Server{DB: db, Vault: vault, SessionUpdates: updates}.Routes(mux, identity.Server{DB: db})
	server := httptest.NewServer(mux)
	defer server.Close()
	base := "/api/projects/" + project
	request := func(method, path, user string, payload any, want int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, base+path, bytes.NewReader(body)).WithContext(ctx)
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[user]})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, want, w.Body)
		}
		return w
	}
	revision := func() int {
		t.Helper()
		var value int
		if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	expectRole := func(member, want string, canReview bool) {
		t.Helper()
		var role string
		var mayReview bool
		err := db.QueryRow(ctx, "select role,can_review from task_participants where project_id=$1 and member_id=$2", project, member).Scan(&role, &mayReview)
		if err != nil || role != want || mayReview != canReview {
			t.Fatalf("role = %s, can review = %t, err = %v", role, mayReview, err)
		}
	}
	invite := func(user, member, role string, want int) *httptest.ResponseRecorder {
		return request(http.MethodPost, "/participants", user, map[string]any{
			"expected": revision(), "email": member + "@example.invalid", "role": role, "reason": "测试分工",
		}, want)
	}
	expectRole(owner, "owner", true)
	request(http.MethodGet, "", reviewer, nil, 404)
	invite(owner, reviewer, "owner", 422)
	invite(owner, reviewer, "contributor", 204)
	expectRole(reviewer, "contributor", false)
	invite(reviewer, nominee, "reviewer", 403)
	invite(owner, reviewer, "reviewer", 204)
	expectRole(reviewer, "reviewer", true)

	var offer struct{ ID string }
	response := invite(owner, nominee, "approver", 202)
	if err = json.Unmarshal(response.Body.Bytes(), &offer); err != nil || offer.ID == "" {
		t.Fatalf("invalid review offer: %s", response.Body)
	}
	expectRole(nominee, "contributor", false)
	request(http.MethodPost, "/handoffs/"+offer.ID, nominee, map[string]any{"expected": revision(), "action": "accept"}, 204)
	expectRole(nominee, "approver", true)

	run, artifact, feedbackID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec("insert into plans(project_id,version,brief) values($1,1,'核对')", project)
	exec("insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,$4,$5,digest('test','sha256'),1)", run, project, owner, team, run)
	exec("insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,1,'结果')", artifact, project, run)
	exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,'项目结果','生成')", artifact)
	exec("insert into feedback(id,artifact_id,base_version,author,reviewer,kind,position,body,state) values($1,$2,1,$3,$3,'change','','待复核','required')", feedbackID, artifact, nominee)
	exec("insert into trace_grants values($1,$2,'structure',now()+interval '1 hour')", run, nominee)
	revoke := func(member, actor string, expected, want int) {
		request(http.MethodDelete, "/participants/"+member, actor, map[string]int{"expected": expected}, want)
	}
	revoke(owner, owner, revision(), 409)
	revoke(reviewer, nominee, revision(), 403)
	revoke(nominee, owner, revision()-1, 409)

	streamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+base+"/session-events", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamReq.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[nominee]})
	stream, err := server.Client().Do(streamReq)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	events := bufio.NewScanner(stream.Body)
	waitForEvent := func(event string) {
		t.Helper()
		for events.Scan() {
			if events.Text() == "event: "+event {
				return
			}
		}
		t.Fatalf("missing %s from SSE stream, HTTP %d: %v", event, stream.StatusCode, events.Err())
	}
	waitForEvent("session.snapshot")
	source := uuid.NewString()
	exec("insert into sources(id,project_id,name,created_by) values($1,$2,'采购表.csv',$3)", source, project, owner)
	waitForEvent("sources.changed")
	exec("insert into objects(hash,size_bytes) values(repeat('8',64),0) on conflict do nothing")
	exec("insert into source_versions(source_id,version,object_hash,media_type,author) values($1,1,repeat('8',64),'text/csv',$2)", source, owner)
	exec("update source_versions set extraction_state='ready',extracted='完成提取' where source_id=$1 and version=1", source)
	waitForEvent("sources.changed")
	revoke(nominee, owner, revision(), 204)
	waitForEvent("access-ended")
	request(http.MethodGet, "", nominee, nil, 404)
	request(http.MethodPost, "/messages", nominee, map[string]string{"body": "继续查看"}, 404)
	var approver, assignee string
	var trace, recorded bool
	err = db.QueryRow(ctx, `select p.final_reviewer,f.reviewer,exists(select from trace_grants where run_id=$4 and member_id=$3),
	    exists(select from feedback_events where feedback_id=f.id and revision=f.revision and action='transfer')
	    from projects p cross join feedback f where p.id=$1 and f.id=$2`, project, feedbackID, nominee, run).Scan(&approver, &assignee, &trace, &recorded)
	if err != nil || approver != owner || assignee != owner || trace || !recorded {
		t.Fatalf("approval returned %t, feedback reassigned %t, trace access %t, transfer recorded %t, err = %v",
			approver == owner, assignee == owner, trace, recorded, err)
	}
	invite(owner, nominee, "contributor", 204)
	expectRole(nominee, "contributor", false)
	t.Run("versioned lightweight comments outside review", func(t *testing.T) {
		subscription, err := server.Client().Do(streamReq.Clone(ctx))
		if err != nil {
			t.Fatal(err)
		}
		defer subscription.Body.Close()
		events = bufio.NewScanner(subscription.Body)
		waitForEvent("session.snapshot")
		path := "/artifacts/" + artifact + "/comments"
		payload := map[string]any{"expected_version": 1, "anchor": commentAnchor{Kind: "paragraph", Index: 1}, "body": "请核对这一段"}
		created := request(http.MethodPost, path, nominee, payload, 201)
		var comment artifactComment
		if err := json.Unmarshal(created.Body.Bytes(), &comment); err != nil || comment.Revision != 1 || comment.Author != nominee {
			t.Fatalf("unexpected comment: %s", created.Body)
		}
		waitForEvent("comments.changed")
		request(http.MethodGet, path+"?version=1", reviewer, nil, 200)
		request(http.MethodPatch, path+"/"+comment.ID, reviewer, map[string]any{"resolved": true, "expected_revision": 1}, 204)
		waitForEvent("comments.changed")
		request(http.MethodPatch, path+"/"+comment.ID, nominee, map[string]any{"resolved": false, "expected_revision": 1}, 409)
		resolved := request(http.MethodGet, path+"?version=1", nominee, nil, 200)
		var page struct{ Comments []artifactComment }
		if err := json.Unmarshal(resolved.Body.Bytes(), &page); err != nil || len(page.Comments) != 1 ||
			page.Comments[0].ResolvedAt == nil || page.Comments[0].Revision != 2 || page.Comments[0].Anchor.Index != 1 {
			t.Fatalf("unexpected resolved comment: %s", resolved.Body)
		}
		exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,2,'更新文本','修订')", artifact)
		exec("update artifacts set current_version=2 where id=$1", artifact)
		request(http.MethodPost, path, nominee, payload, 409)
		latest := request(http.MethodGet, path+"?version=2", nominee, nil, 200)
		if !strings.Contains(latest.Body.String(), `"comments":[]`) {
			t.Fatalf("new version inherited old anchors: %s", latest.Body)
		}
		revoke(nominee, owner, revision(), 204)
		request(http.MethodGet, path+"?version=1", nominee, nil, 404)
		request(http.MethodPatch, path+"/"+comment.ID, nominee, map[string]any{"resolved": false, "expected_revision": 2}, 404)
	})
	t.Run("optimistic version guard for revisions and approvals", func(t *testing.T) {
		exec("update projects set stage='review' where id=$1", project)
		path := "/artifacts/" + artifact
		request(http.MethodPost, path+"/revise", reviewer, map[string]any{"expected_version": 2, "body": "同事的修订", "reason": "核对后调整"}, 204)
		request(http.MethodPost, path+"/revise", owner, map[string]any{"expected_version": 2, "body": "旧版草稿", "reason": "并发修改"}, 409)
		request(http.MethodPost, path+"/review", owner, map[string]int{"expected_version": 2}, 409)
		request(http.MethodPost, path+"/review", owner, map[string]int{"expected_version": 3, "version": 2}, 422)
		current := request(http.MethodGet, path+"/versions?version=3", reviewer, nil, 200)
		if !strings.Contains(current.Body.String(), "同事的修订") || strings.Contains(current.Body.String(), "旧版草稿") {
			t.Fatalf("stale edit changed the current version: %s", current.Body)
		}
		request(http.MethodPost, path+"/revise", owner, map[string]any{"version": 3, "body": "核对新版后的修订", "reason": "已核对"}, 204)
	})
}
