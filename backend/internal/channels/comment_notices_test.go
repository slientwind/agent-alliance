package channels

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type commentTransport func(*http.Request) (*http.Response, error)

func (f commentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDingCommentNoticeUsesBoundAccountAndGenericCard(t *testing.T) {
	link := commentLink("https://work.example.test", "project-id", "comment-id")
	if link != "https://work.example.test/chat/project-id?task=mention-comment-id" || commentLink("http://localhost", "p", "c") != "" {
		t.Fatalf("invalid comment link %q", link)
	}
	calls := 0
	d := DingTalk{app: "app-fixture", secret: "secret-fixture", robot: "robot-fixture", client: &http.Client{
		Transport: commentTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			result := `{"accessToken":"opaque-fixture"}`
			if r.URL.Host != "api.dingtalk.com" || r.Method != "POST" {
				t.Errorf("unexpected destination: %s %s", r.Method, r.URL)
			}
			if calls == 2 {
				var payload struct {
					RobotCode, MsgKey, MsgParam string
					UserIds                     []string
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil ||
					payload.RobotCode != "robot-fixture" || payload.MsgKey != "sampleActionCard" || len(payload.UserIds) != 1 ||
					payload.UserIds[0] != "authorized-fixture" || !strings.Contains(payload.MsgParam, link) ||
					strings.Contains(payload.MsgParam, "secret-fixture") || r.Header.Get("x-acs-dingtalk-access-token") != "opaque-fixture" {
					t.Errorf("unsafe or incomplete card: %+v, %v", payload, err)
				}
				result = `{"processQueryKey":"fixture-receipt"}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
		}),
	}}
	if got := d.commentNotice(t.Context(), "authorized-fixture", link, "job"); got.state != "sent" || got.receipt != "fixture-receipt" || calls != 2 {
		t.Fatalf("unexpected receipt %+v and %d attempts", got, calls)
	}
}

func TestCommentNoticesStopOnBindingChangePostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
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
	person, team, project, run, artifact, comment := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	feishuBinding, dingBinding := uuid.NewString(), uuid.NewString()
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", person, person+"@example.test")
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Fixture',1)", project, person, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, person)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'Fixture',$2)", project, person)
	exec("insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'completed',$4,'fixture',$5,1)", run, project, person, team, web.Hash("fixture"))
	exec("insert into artifacts(id,project_id,run_id,ordinal,name) values($1,$2,$3,1,'Fixture')", artifact, project, run)
	exec("insert into artifact_versions(artifact_id,version,body,reason) values($1,1,'Fixture','Fixture')", artifact)
	exec("insert into artifact_comments(id,artifact_id,version,author_id,anchor_kind,anchor_index,body) values($1,$2,1,$3,'paragraph',1,'test-private')", comment, artifact, person)
	exec("insert into artifact_comment_mentions values($1,$2)", comment, person)
	exec("insert into channel_links(tenant,open_id,member_id,binding_id) values($1,'old-feishu',$2,$3)", team, person, feishuBinding)
	exec("insert into im_bindings(connection,external_user,member_id,id) values($1,'exact-ding',$2,$3)", team, person, dingBinding)
	exec("insert into artifact_comment_deliveries(comment_id,member_id,channel,connection,binding) values($1,$2,'feishu',$3,$4),($1,$2,'dingtalk',$3,$5)", comment, person, team, feishuBinding, dingBinding)
	exec("delete from channel_links where tenant=$1 and member_id=$2", team, person)
	exec("insert into channel_links(tenant,open_id,member_id) values($1,'new-feishu',$2)", team, person)
	sent := 0
	sender := func(_ context.Context, target, link, key string) commentResult {
		sent++
		if target != "exact-ding" || link != "https://work.example.test/chat/"+project+"?task=mention-"+comment || key == "" {
			t.Errorf("bad delivery: %s %s %s", target, link, key)
		}
		return commentResult{"sent", "receipt-fixture"}
	}
	for i := 0; i < 3; i++ {
		if err := deliverCommentNotice(ctx, db, team, team, "https://work.example.test", "https://work.example.test", sender, sender); err != nil {
			t.Fatal(err)
		}
	}
	var revoked, recorded int
	err = db.QueryRow(ctx, `select count(*) filter(where channel='feishu' and state='revoked'),
	 count(*) filter(where channel='dingtalk' and state='sent' and receipt='receipt-fixture') from artifact_comment_deliveries where comment_id=$1`, comment).
		Scan(&revoked, &recorded)
	if err != nil || sent != 1 || revoked != 1 || recorded != 1 {
		t.Fatalf("calls %d, revoked %d, recorded %d: %v", sent, revoked, recorded, err)
	}
}
