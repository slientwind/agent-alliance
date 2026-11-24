package channels

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSlackProjectThreadAndSharedWaitPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_SLACK_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_SLACK_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_slack_test") {
		t.Fatal("use a scratch database named alliance_slack_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 36*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("e3", 32))
	if err != nil {
		t.Fatal(err)
	}
	const bot = "scratch-slack-bot"
	replies := make(chan string, 32)
	var upstream atomic.Int32
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Channel, Text string
			Thread        string `json:"thread_ts"`
		}
		if r.Header.Get("Authorization") != "Bearer "+bot || json.NewDecoder(r.Body).Decode(&payload) != nil ||
			payload.Channel != "DDEMO" || payload.Thread == "" || payload.Text == "" {
			t.Errorf("bad outbound Slack payload: %+v", payload)
			w.WriteHeader(400)
			return
		}
		replies <- payload.Text
		if code := int(upstream.Swap(0)); code != 0 {
			w.Header().Set("Retry-After", "9")
			w.WriteHeader(code)
			return
		}
		_, _ = fmt.Fprint(w, `{"ok":true,"channel":"DDEMO","ts":"1730000000.990010"}`)
	}))
	defer api.Close()
	s := Slack{db: db, vault: vault, team: "TDEMO", app: "ADEMO", bot: bot, signing: "test-slack-signing",
		public: "https://example.invalid", endpoint: api.URL, client: api.Client()}
	secret, team, actor := web.Token(), uuid.NewString(), uuid.NewString()
	sql := func(statement string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	check := func(reason, statement string, args ...any) {
		t.Helper()
		var passed bool
		if err := db.QueryRow(ctx, statement, args...).Scan(&passed); err != nil || !passed {
			t.Fatalf("%s: passed=%t err=%v", reason, passed, err)
		}
	}
	sql("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Slack member','unused','member')", actor, actor+"@example.invalid")
	sql("insert into teams(id,name) values($1,$2)", team, team)
	sql("insert into team_members values($1,$2)", team, actor)
	sql("insert into enterprise(name,default_team) values('Slack fixture',$1) on conflict do nothing", team)
	sql("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(secret), actor)
	sql("insert into channel_links(tenant,open_id,member_id) values('other-provider','other-user',$1)", actor)

	mux := http.NewServeMux()
	g := identity.Server{DB: db}
	s.Routes(mux, g)
	projects.Server{DB: db, Vault: vault}.Routes(mux, g)
	webRequest := func(method, path, key string, value any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(value)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: secret})
		req.Header.Set("Idempotency-Key", key)
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	const bindPath = "/api/channels/slack/bind"
	code := func() string {
		t.Helper()
		res := webRequest("POST", bindPath, "", nil)
		var data struct{ Command string }
		if res.Code != 201 || json.Unmarshal(res.Body.Bytes(), &data) != nil || !strings.HasPrefix(data.Command, "绑定 ") {
			t.Fatalf("create binding code: %d %s", res.Code, res.Body)
		}
		return data.Command
	}
	signed := func(value any, key string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(value)
		stamp := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(key))
		_, _ = mac.Write(append([]byte("v0:"+stamp+":"), raw...))
		req := httptest.NewRequest("POST", "/api/channels/slack/events", bytes.NewReader(raw)).WithContext(ctx)
		req.Header.Set("X-Slack-Request-Timestamp", stamp)
		req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		return res
	}
	envelope := func(id, stamp, thread, text string) slackEnvelope {
		value := slackEnvelope{Type: "event_callback", Team: s.team, App: s.app, ID: id}
		value.Event.Type, value.Event.ChannelType, value.Event.Channel, value.Event.User = "message", "im", "DDEMO", "UDEMO"
		value.Event.TS, value.Event.Thread, value.Event.Text = stamp, thread, text
		return value
	}
	accept := func(value slackEnvelope) {
		t.Helper()
		if res := signed(value, s.signing); res.Code != 200 {
			t.Fatalf("incoming %s: %d %s", value.ID, res.Code, res.Body)
		}
	}
	process := func() {
		t.Helper()
		if err := s.processNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(want string) string {
		t.Helper()
		if err := s.deliverNext(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-replies:
			if !strings.Contains(got, want) {
				t.Fatalf("Slack reply %q did not contain %q", got, want)
			}
			return got
		default:
			t.Fatalf("no Slack reply containing %q", want)
			return ""
		}
	}

	challenge := slackEnvelope{Type: "url_verification", Challenge: "local test challenge"}
	if res := signed(challenge, "wrong-key"); res.Code != 403 {
		t.Fatalf("unsigned scope accepted: %d", res.Code)
	}
	if res := signed(challenge, s.signing); res.Code != 200 || !strings.Contains(res.Body.String(), challenge.Challenge) {
		t.Fatalf("Slack challenge: %d %s", res.Code, res.Body)
	}
	valid := envelope("EvGroup", "1730000000.000010", "", "新建 错误渠道")
	valid.Event.ChannelType, valid.Event.Channel = "channel", "CDEMO"
	accept(valid)
	valid = envelope("EvShared", "1730000000.000011", "", "新建 外部账号")
	valid.Shared = true
	accept(valid)
	valid = envelope("EvOther", "1730000000.000012", "", "新建 另一工作区")
	valid.Team = "TOTHER"
	if res := signed(valid, s.signing); res.Code != 403 {
		t.Fatalf("foreign workspace accepted: %d", res.Code)
	}
	check("ignored event filters", "select count(*)=0 from im_events where connection=$1", s.connection())

	stale, fresh := code(), code()
	accept(envelope("EvStale", "1730000000.000020", "", stale))
	process()
	deliver("绑定码已失效")
	accepted := envelope("EvBind", "1730000000.000021", "", fresh)
	accept(accepted)
	accept(accepted)
	accepted.ID = "EvSameMessage"
	accept(accepted)
	check("binding only after processing and payload encrypted before acknowledgement", `select payload is not null and binding_id is null and state='received'
         and position($2::bytea in payload)=0 from im_events where (connection,event_id)=($1,'EvBind')`, s.connection(), []byte(fresh))
	process()
	if got := deliver("Slack 身份已绑定"); strings.Contains(got, fresh) {
		t.Fatal("binding code appeared in the reply")
	}
	check("binding code consumed and message deduplicated", `select (select count(*)=1 from im_bindings where connection=$1)
      and (select count(*)=0 from im_binding_codes where connection=$1)
      and (select count(*)=1 from im_events where connection=$1 and message_id='1730000000.000021')`, s.connection())

	const thread = "1730000000.000030"
	accept(envelope("EvCreate", thread, "", "新建 采购报价核对"))
	process()
	var project string
	if err := db.QueryRow(ctx, "select project_id from im_threads where thread_id=$1", thread).Scan(&project); err != nil {
		t.Fatal(err)
	}
	deliver("https://example.invalid/#" + project)
	const note = "第二家供应商的报价仍待核对。"
	msg := envelope("EvNote", "1730000000.000031", thread, "补充 "+note)
	accept(msg)
	accept(msg)
	process()
	upstream.Store(429)
	deliver("消息已加入原项目对话")
	check("rate limiting only retries the reply", `select state='ready' and attempts=1 and next_at>now() and payload is null
        from im_events where (connection,event_id)=($1,'EvNote')`, s.connection())
	sql("update im_events set next_at=now() where (connection,event_id)=($1,'EvNote')", s.connection())
	deliver("消息已加入原项目对话")
	accept(envelope("EvOtherThread", "1730000000.000039", "", "补充 未绑定线程"))
	process()
	deliver("切换 项目ID")
	check("one message in the original project and none from the other thread", `select count(*)=2 and count(*) filter(where body=$2)=1
       from messages where project_id=$1`, project, note)

	agent, run := uuid.NewString(), uuid.NewString()
	sql("insert into agents(id,owner,team_id) values($1,$2,$3)", agent, actor, team)
	sql("insert into agent_revisions(agent_id,revision,name,instructions,tool,created_by) values($1,1,'Fixture','指定文件','{\"operation\":\"docx\"}',$2)", agent, actor)
	sql("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'报价核对',$2)", project, actor)
	sql("insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,1,1,$2,1)", project, agent)
	sql("update projects set plan_version=1,stage='running' where id=$1", project)
	sql(`insert into runs(id,project_id,plan_version,actor,charge_team,status,max_calls,request_key,request_hash)
         values($1,$2,1,$3,$4,'running',1,$5,'fixture')`, run, project, actor, team, run)
	var wait int
	if err := db.QueryRow(ctx, `insert into step_dispatch_waits(run_id,ordinal,plan_version,agent_id,agent_revision,reason,note)
         values($1,1,1,$2,1,'resource_denied','私密诊断信息') returning wait_revision`, run, agent).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	accept(envelope("EvTodos", "1730000000.000040", thread, "待办"))
	process()
	action := fmt.Sprintf("复查 %s 1 %d 1", run, wait)
	if reply := deliver(action); strings.Contains(reply, "私密诊断信息") {
		t.Fatal("raw diagnostics were shared in Slack")
	}
	accept(envelope("EvAction", "1730000000.000041", thread, action))
	completed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		completed <- webRequest("POST", "/api/runs/"+run+"/step-dispatch/recheck", "web-competing-step", projects.DispatchRecheckInput{Step: 1, Expected: 1, WaitRevision: wait})
	}()
	process()
	webResult := <-completed
	// Whichever channel obtained the run lock first can confirm the wait.
	if webResult.Code == 202 {
		deliver("步骤待办已变化")
	} else if webResult.Code == 409 {
		deliver("已提交步骤复查")
	} else {
		t.Fatalf("unexpected result of simultaneous web recheck: %d %s", webResult.Code, webResult.Body)
	}
	check("only one control for the original run", `select (select count(*)=1 from controls where run_id=$1 and wake_ordinal=1)
        and not exists(select from step_dispatch_waits where run_id=$1)
        and not exists(select from invocations where run_id=$1)`, run)
	accept(envelope("EvStaleAction", "1730000000.000042", thread, action))
	process()
	deliver("步骤待办已变化")
	accept(envelope("EvAmbiguous", "1730000000.000043", thread, "进度"))
	process()
	upstream.Store(503)
	deliver("采购报价核对")
	check("unknown delivery does not enqueue a replay", "select state='unknown' and attempts=1 from im_events where event_id='EvAmbiguous'")

	accept(envelope("EvBeforeUnlink", "1730000000.000044", thread, "进度"))
	process()
	accept(envelope("EvQueuedAtUnlink", "1730000000.000045", thread, "补充 解除绑定后不能写入"))
	if res := webRequest("DELETE", bindPath, "", nil); res.Code != 204 {
		t.Fatalf("unlink: %d %s", res.Code, res.Body)
	}
	process()
	for range 2 {
		if err := s.deliverNext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(replies) != 0 {
		t.Fatal("a reply escaped after unlinking")
	}
	check("unlink suppressed ready and received work and left Feishu alone", `select
       (select count(*)=2 from im_events where event_id in ('EvBeforeUnlink','EvQueuedAtUnlink') and state='suppressed' and payload is null)
       and (select count(*)=2 from messages where project_id=$1)
       and not exists(select from im_threads where project_id=$1)
       and exists(select from channel_links where tenant='other-provider' and member_id=$2)`, project, actor)

	// Enter through the same recurring worker used by the deployed API.
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerStopped := make(chan struct{})
	go func() {
		defer close(workerStopped)
		s.Serve(workerCtx)
	}()
	defer func() { stopWorker(); <-workerStopped }()
	accept(envelope("EvRebind", "1730000000.000046", "", code()))
	select {
	case reply := <-replies:
		if !strings.Contains(reply, "Slack 身份已绑定") {
			t.Fatalf("asynchronous binding reply: %q", reply)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("the Slack background worker did not deliver a reply")
	}
	check("rebinding never inherits the old thread mapping", `select
        exists(select from im_bindings where (connection,member_id)=($1,$2))
        and not exists(select from im_threads where project_id=$3)`, s.connection(), actor, project)
}
