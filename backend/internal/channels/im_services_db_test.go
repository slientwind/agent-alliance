package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/planner"
	"alliance/internal/resources"
	"alliance/internal/services"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIMServiceLaunchPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_IM_SERVICE_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_IM_SERVICE_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_im_service_test") {
		t.Fatal("use a scratch database named alliance_im_service_test*")
	}
	t.Setenv("OBJECT_ROOT", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a9", 32))
	if err != nil {
		t.Fatal(err)
	}
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	check := func(reason, query string, args ...any) {
		t.Helper()
		var ok bool
		if err := db.QueryRow(ctx, query, args...).Scan(&ok); err != nil || !ok {
			t.Fatalf("%s: %t, %v", reason, ok, err)
		}
	}
	actor, outsider, team := uuid.NewString(), uuid.NewString(), uuid.NewString()
	session, otherSession := web.Token(), web.Token()
	sql(`insert into members(id,email,display_name,password_hash,role)
 values($1,$2,'负责人','unused','member'),($3,$4,'其他成员','unused','member')`, actor, actor+"@example.invalid", outsider, outsider+"@example.invalid")
	sql("insert into teams(id,name) values($1,$2)", team, "工作团队 "+team[:8])
	sql("insert into team_members values($1,$2)", team, actor)
	sql(`insert into sessions(token_hash,member_id,expires_at)
 values($1,$2,now()+interval '10 minutes'),($3,$4,now()+interval '10 minutes')`, web.Hash(session), actor, web.Hash(otherSession), outsider)
	model, agent, textService, lookupService := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	key, err := vault.Seal("fixture", "model/"+model+"/1")
	if err != nil {
		t.Fatal(err)
	}
	sql("insert into model_resources(id,name,team_id,created_by) values($1,'Model',$2,$3)", model, team, actor)
	sql(`insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny)
 values($1,1,'https://example.invalid/api','fixture',$2,0.2)`, model, key)
	sql("insert into agents(id,owner,team_id) values($1,$2,$3)", agent, actor, team)
	sql(`insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
 values($1,1,'核对员','核对工作输入',$2,1,$3)`, agent, model, actor)
	sql("insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", agent, actor)
	sql("update agents set published_revision=1 where id=$1", agent)
	sql(`insert into services(id,team_id,name,owner) values($1,$2,'归纳材料',$3),($4,$2,'查证材料',$3)`, textService, team, actor, lookupService)
	sql(`insert into service_versions(service_id,version,instructions,reviewer) values($1,1,'归纳要点',$2),($3,1,'检查依据',$2)`, textService, actor, lookupService)
	sql(`insert into service_nodes(service_id,version,ordinal,agent_id,agent_revision,lookup_phase)
 values($1,1,1,$3,1,''),($2,1,1,$3,1,'search'),($2,1,2,$3,1,'answer')`, textService, lookupService, agent)
	sql("insert into service_edges values($1,1,2,1)", lookupService)

	slack := imChannel{db: db, vault: vault, key: "slack/" + uuid.NewString(), provider: "slack", name: "Slack", conversation: "线程", public: "https://example.invalid"}
	ding := imChannel{db: db, vault: vault, key: "dingtalk/" + uuid.NewString(), provider: "dingtalk", name: "钉钉", conversation: "单聊", public: "https://example.invalid"}
	binding, dingBinding := uuid.NewString(), uuid.NewString()
	sql(`insert into im_bindings(id,connection,external_user,member_id)
 values($1,$2,'member',$3),($4,$5,'member',$3),($6,$2,'other',$7)`, binding, slack.key, actor, dingBinding, ding.key, uuid.NewString(), outsider)

	mux := http.NewServeMux()
	services.Server{DB: db, Planner: planner.Server{Catalog: resources.Catalog{DB: db, Vault: vault}}}.Routes(mux, identity.Server{DB: db})
	call := func(method, target, token string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, target, bytes.NewReader(raw)).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	expect := func(res *httptest.ResponseRecorder, status int, content string) {
		t.Helper()
		if res.Code != status || !strings.Contains(res.Body.String(), content) {
			t.Fatalf("response = %d %s, want %d and %q", res.Code, res.Body.String(), status, content)
		}
	}
	send := func(s imChannel, sender, thread, text string) {
		t.Helper()
		id := uuid.NewString()
		msg := imMessage{event: id, message: id, chat: "private", thread: thread, sender: sender, text: text}
		if err := s.accept(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(s imChannel) (text, link string) {
		t.Helper()
		err := s.deliverNext(ctx, func(_ context.Context, _ imJob, body string) (string, time.Duration) {
			text = body
			return "sent", 0
		}, func(body, target string) string {
			link = target
			return body
		})
		if err != nil || text == "" {
			t.Fatalf("missing reply: %q %v", text, err)
		}
		return text, link
	}
	command := func(s imChannel, sender, thread, text string) (string, string) {
		t.Helper()
		send(s, sender, thread, text)
		if err := s.processNext(ctx); err != nil {
			t.Fatal(err)
		}
		return deliver(s)
	}
	preview := func(s imChannel, thread, service, goal string) (string, string, string) {
		t.Helper()
		body, link := command(s, "member", thread, "发起 "+service+" "+goal)
		u, err := url.Parse(link)
		if err != nil || u.Query().Get("service") != service || !uuidPattern.MatchString(u.Query().Get("channelLaunch")) ||
			!strings.Contains(body, goal) || !strings.Contains(body, "最终确认人：负责人") {
			t.Fatalf("preview: %q %q %v", body, link, err)
		}
		id := u.Query().Get("channelLaunch")
		return id, "/api/services/" + service + "/channel-launches/" + id, body
	}

	text, _ := command(slack, "other", "own", "服务")
	if strings.Contains(text, textService) || strings.Contains(text, lookupService) {
		t.Fatalf("catalog leaked outside the team: %q", text)
	}
	text, _ = command(slack, "member", "root", "服务 归纳")
	if !strings.Contains(text, textService) || strings.Contains(text, lookupService) {
		t.Fatalf("catalog filter: %q", text)
	}

	const textGoal = "将报价差异整理成内部简报"
	id, entry, text := preview(slack, "root", textService, textGoal)
	if !strings.Contains(text, "确认 "+id) || !strings.Contains(text, "最多授权 1 次") {
		t.Fatal(text)
	}
	check("preview is encrypted and has no business effect", `select
 (select goal is not null and position(convert_to($2,'UTF8') in goal)=0 and run_id is null from im_service_launches where id=$1)
 and not exists(select from runs where actor=$3)
 and not exists(select from im_events where connection=$4 and (note like '%'||$2||'%' or reply_argument is not null))`, id, textGoal, actor, slack.key)
	expect(call("GET", entry, otherSession, nil), 404, "无权访问")
	expect(call("GET", strings.Replace(entry, textService, lookupService, 1), session, nil), 404, "无权访问")
	expect(call("GET", entry, session, nil), 200, textGoal)
	expect(call("POST", entry, session, map[string]any{"acknowledged": false}), 422, "调用授权")
	expect(call("POST", entry, session, map[string]any{"acknowledged": true, "input": "改写工作目标"}), 422, "请求格式")
	for _, scope := range []struct {
		channel imChannel
		thread  string
	}{{slack, "another"}, {ding, "root"}} {
		message, _ := command(scope.channel, "member", scope.thread, "确认 "+id)
		if !strings.Contains(message, "不属于当前会话") {
			t.Fatalf("cross-conversation confirmation: %q", message)
		}
	}

	// Start two competing entry points. The preview lock owns the first
	// acceptance, and both callers must receive the same project.
	send(slack, "member", "root", "确认 "+id)
	processed := make(chan error, 1)
	go func() { processed <- slack.processNext(ctx) }()
	res := call("POST", entry, session, map[string]any{"acknowledged": true})
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	expect(res, 202, "project")
	var result services.ChannelLaunchReceipt
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil || result.Project == "" {
		t.Fatalf("receipt: %+v %v", result, err)
	}
	_, link := deliver(slack)
	if link != "https://example.invalid/#"+result.Project {
		t.Fatalf("competing channel obtained %q, want project %s", link, result.Project)
	}
	check("exactly one task, authorized input and durable dispatch", `select
 (select count(*)=1 from runs where actor=$1)
 and exists(select from runs r join outbox o on o.run_id=r.id join messages m on m.project_id=r.project_id
 join im_service_launches p on p.run_id=r.id join im_threads t on (t.binding_id,t.chat_id,t.thread_id)=(p.binding_id,p.chat_id,p.thread_id)
 where r.id=$2 and r.project_id=t.project_id and p.goal is null and r.max_calls=1 and r.service_version=1 and m.body=$3)
 and not exists(select from invocations where run_id=$2)`, actor, result.ID, textGoal)

	sql("update services set routing_revision=routing_revision+1 where id=$1", textService)
	expect(call("GET", entry, session, nil), 200, result.Project)
	old, oldEntry, _ := preview(slack, "root", textService, "旧版预览")
	sql("update services set routing_revision=routing_revision+1 where id=$1", textService)
	expect(call("POST", oldEntry, session, map[string]any{"acknowledged": true}), 409, "发布状态已变化")
	_, _, _ = preview(slack, "root", textService, "重新发起的预览")
	expect(call("GET", oldEntry, session, nil), 404, "无权访问")
	check("stale preview produced no task", "select not exists(select from im_service_launches where id=$1)", old)

	lookupID, lookupEntry, lookupMessage := preview(ding, "private", lookupService, "查证报价依据")
	if !strings.Contains(lookupMessage, "网页附上原件") || strings.Contains(lookupMessage, "确认 "+lookupID) {
		t.Fatalf("required originals preview: %q", lookupMessage)
	}
	message, _ := command(ding, "member", "private", "确认 "+lookupID)
	if !strings.Contains(message, "网页附上原件") {
		t.Fatal(message)
	}
	expect(call("POST", lookupEntry, session, map[string]any{"acknowledged": true}), 422, "需要一份可读资料")
	original := []byte("报价在周五前有效。")
	submit := map[string]any{"acknowledged": true, "awaitLookupReceipt": true, "attachment": services.Attachment{Name: "报价.txt", Data: original}}
	res = call("POST", lookupEntry, session, submit)
	expect(res, 202, "project")
	var lookupResult services.ChannelLaunchReceipt
	if err := json.Unmarshal(res.Body.Bytes(), &lookupResult); err != nil || lookupResult.Project == "" {
		t.Fatalf("lookup receipt: %+v %v", lookupResult, err)
	}
	expect(call("POST", lookupEntry, session, submit), 202, lookupResult.Project)
	message, link = command(ding, "member", "private", "进度")
	if !strings.Contains(message, "查证材料") || !strings.HasSuffix(link, lookupResult.Project) {
		t.Fatalf("web acceptance did not link DingTalk: %q %q", message, link)
	}
	check("web original, call limit and continuation pinned", `select exists(select from runs r join outbox o on o.run_id=r.id
 join plan_sources p on (p.project_id,p.plan_version)=(r.project_id,r.plan_version)
 join source_versions v on (v.source_id,v.version)=(p.source_id,p.source_version)
 where r.id=$1 and r.max_calls=2 and r.await_lookup_receipt and v.extracted=$2)
 and (select count(*)=2 from runs where actor=$3)`, lookupResult.ID, string(original), actor)

	expired, expiredEntry, _ := preview(ding, "private", textService, "过期工作")
	sql("update im_service_launches set expires_at=clock_timestamp()-interval '1 minute' where id=$1", expired)
	expect(call("GET", expiredEntry, session, nil), 410, "已过期")
	if err := ding.expireJobs(ctx); err != nil {
		t.Fatal(err)
	}
	check("expired goal purged", "select not exists(select from im_service_launches where id=$1)", expired)
	_, unlinkedEntry, _ := preview(ding, "private", textService, "绑定撤回后的工作")
	sql("delete from im_bindings where id=$1", dingBinding)
	expect(call("GET", unlinkedEntry, session, nil), 404, "无权访问")
	check("unlink does not erase accepted work", "select exists(select from runs where id=$1)", lookupResult.ID)
}
