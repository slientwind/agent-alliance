package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

type dingTestTransport func(*http.Request) (*http.Response, error)

func (f dingTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDingTalkStreamProjectPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_DINGTALK_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_DINGTALK_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_dingtalk_test") {
		t.Fatal("use a scratch database named alliance_dingtalk_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("b7", 32))
	if err != nil {
		t.Fatal(err)
	}
	const app, corp, secret = "dingAppFixture", "dingCorpFixture", "only-local-credential"
	const target = "https://oapi.dingtalk.com/robot/sendBySession?session=reply-credential-fixture"
	var opens atomic.Int32
	connections := make(chan *websocket.Conn, 3)
	replies := make(chan string, 16)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/gateway/connections/open":
			var params struct {
				ClientID, ClientSecret string
				Subscriptions          []struct{ Type, Topic string }
			}
			if json.NewDecoder(r.Body).Decode(&params) != nil || r.Host != "api.dingtalk.com" || params.ClientID != app ||
				params.ClientSecret != secret || len(params.Subscriptions) != 1 || params.Subscriptions[0].Type != "CALLBACK" ||
				params.Subscriptions[0].Topic != dingBotTopic {
				t.Error("registration did not match the configured application and bot subscription")
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"endpoint": "wss://wss-open-connection.dingtalk.com:443/connect", "ticket": fmt.Sprint("fixture-ticket-", opens.Add(1)),
			})
		case "/connect":
			if r.URL.Query().Get("ticket") != fmt.Sprint("fixture-ticket-", opens.Load()) {
				t.Error("client did not use the newly issued Stream ticket")
			}
			ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			connections <- ws
		case "/robot/sendBySession":
			var payload struct {
				Type string `json:"msgtype"`
				Text struct{ Content string }
			}
			if r.Host != "oapi.dingtalk.com" || r.URL.Query().Get("session") != "reply-credential-fixture" ||
				r.Header.Get("Authorization") != "" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Type != "text" {
				t.Error("invalid session reply")
				w.WriteHeader(400)
				return
			}
			replies <- payload.Text.Content
			_, _ = fmt.Fprint(w, `{"errcode":0,"errmsg":"ok"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport)
	d := DingTalk{imChannel: imChannel{db: db, vault: vault, provider: "dingtalk", name: "钉钉", conversation: "钉钉单聊",
		key: "dingtalk/" + corp + "/" + app, public: "https://alliance.example.invalid"}, corp: corp, app: app, robot: app, secret: secret,
		client: newIMHTTPClient(), dialer: websocket.Dialer{HandshakeTimeout: 2 * time.Second}}
	d.client.Transport = dingTestTransport(func(req *http.Request) (*http.Response, error) {
		r := req.Clone(req.Context())
		r.Host = req.URL.Host
		r.URL.Host = upstream.Listener.Addr().String()
		return transport.RoundTrip(r)
	})
	d.dialer.TLSClientConfig = transport.TLSClientConfig.Clone()
	d.dialer.TLSClientConfig.ServerName = upstream.Certificate().DNSNames[0]
	d.dialer.NetDialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}

	sql := func(statement string, params ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, statement, params...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(reason, statement string, params ...any) {
		t.Helper()
		var passed bool
		if err := db.QueryRow(ctx, statement, params...).Scan(&passed); err != nil || !passed {
			t.Fatalf("%s: %t %v", reason, passed, err)
		}
	}
	actor, team, browserCode := uuid.NewString(), uuid.NewString(), web.Token()
	sql("insert into members(id,email,display_name,password_hash,role) values($1,$2,'DingTalk fixture','unused','member')", actor, actor+"@example.invalid")
	sql("insert into teams(id,name) values($1,$2)", team, team)
	sql("insert into team_members values($1,$2)", team, actor)
	sql("insert into enterprise(name,default_team) values('DingTalk fixture',$1) on conflict do nothing", team)
	sql("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(browserCode), actor)
	const otherKey = "slack/TSAME/ASAME"
	const otherCode = "external-channel-fixture-code"
	sql("insert into im_binding_codes values($1,$2,$3,now()+interval '10 minutes')", otherKey, actor, web.Hash(otherCode))
	mux := http.NewServeMux()
	d.Routes(mux, identity.Server{DB: db})
	req := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/channels/dingtalk/bind", bytes.NewReader(nil)).WithContext(ctx)
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: browserCode})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	var code struct{ Command string }
	if res := req("POST"); res.Code != 201 || json.Unmarshal(res.Body.Bytes(), &code) != nil || !strings.HasPrefix(code.Command, "绑定 ") {
		t.Fatalf("unexpected binding endpoint response: %d", res.Code)
	}

	streamCtx, stop := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { defer close(stopped); d.listen(streamCtx) }()
	defer func() { stop(); <-stopped }()
	connect := func() *websocket.Conn {
		t.Helper()
		select {
		case ws := <-connections:
			t.Cleanup(func() { _ = ws.Close() })
			return ws
		case <-time.After(6 * time.Second):
			t.Fatal("DingTalk Stream did not connect")
			return nil
		}
	}
	ws := connect()
	roundtrip := func(frame dingStreamFrame) dingStreamAck {
		t.Helper()
		if err := ws.WriteJSON(frame); err != nil {
			t.Fatal(err)
		}
		_ = ws.SetReadDeadline(time.Now().Add(4 * time.Second))
		var ack dingStreamAck
		if err := ws.ReadJSON(&ack); err != nil || ack.Code != 200 || ack.Headers["messageId"] != frame.Headers.MessageID {
			t.Fatalf("Stream frame %s: acknowledgement=%+v err=%v", frame.Headers.MessageID, ack, err)
		}
		return ack
	}
	var ping dingStreamFrame
	ping.Type, ping.Headers.Topic, ping.Headers.MessageID, ping.Data = "SYSTEM", "ping", "fixture-heartbeat", `{"opaque":"heartbeat-value"}`
	if ack := roundtrip(ping); ack.Data != ping.Data {
		t.Fatalf("heartbeat response: %+v", ack)
	}
	const chat = "cidFixture=="
	callback := func(event, message, text string, edit func(*dingRobotMessage)) {
		t.Helper()
		value := dingRobotMessage{MessageID: message, MessageType: "text", ConversationID: chat, ConversationType: "1",
			Sender: "staff-fixture", SenderCorp: corp, BotCorp: corp, Robot: app, ReplyURL: target, ReplyExpiry: time.Now().Add(time.Hour).UnixMilli()}
		value.Text.Content = text
		if edit != nil {
			edit(&value)
		}
		data, _ := json.Marshal(value)
		frame := dingStreamFrame{Version: "1.0", Type: "CALLBACK", Data: string(data)}
		frame.Headers.MessageID, frame.Headers.Topic, frame.Headers.ContentType = event, dingBotTopic, "application/json"
		roundtrip(frame)
	}
	poll := func(want string) {
		t.Helper()
		if err := d.processNext(ctx); err != nil {
			t.Fatal(err)
		}
		if err := d.deliverNext(ctx, d.postReply, dingFormat); err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(replies) != 0 {
				t.Fatal("unauthorized or expired reply was sent")
			}
			return
		}
		select {
		case reply := <-replies:
			if !strings.Contains(reply, want) || strings.Contains(reply, "reply-credential-fixture") {
				t.Fatalf("unexpected DingTalk reply: %q", reply)
			}
		default:
			t.Fatalf("no DingTalk reply containing %q", want)
		}
	}
	callback("ignored-corp", "corp-bad", "新建 错误企业", func(msg *dingRobotMessage) { msg.SenderCorp = "wrong-corp" })
	callback("ignored-group", "group-bad", "新建 群聊", func(msg *dingRobotMessage) { msg.ConversationType = "2" })
	callback("ignored-target", "target-bad", "新建 错误回复地址", func(msg *dingRobotMessage) { msg.ReplyURL = "https://example.invalid/internal" })
	check("filtered messages never enter the inbox", "select count(*)=0 from im_events where connection=$1", d.key)
	// A queue failure must not be acknowledged as a durable receipt.
	var lost dingStreamFrame
	lost.Type, lost.Version, lost.Headers.MessageID, lost.Headers.Topic = "CALLBACK", "1.0", "queue-failure", dingBotTopic
	lost.Data = fmt.Sprintf(`{"msgId":"db-failure","msgtype":"text","conversationId":%q,"conversationType":"1","senderStaffId":"staff-fixture","senderCorpId":%q,"chatbotCorpId":%q,"robotCode":%q,"sessionWebhook":%q,"sessionWebhookExpiredTime":%d,"text":{"content":"进度"}}`,
		chat, corp, corp, app, target, time.Now().Add(time.Minute).UnixMilli())
	failedCtx, cancelFailed := context.WithCancel(ctx)
	cancelFailed()
	if ack := d.handleStreamFrame(failedCtx, lost); ack == nil || ack.Code != 500 {
		t.Fatalf("queue failure must return 500: %+v", ack)
	}
	check("failed intake did not save an event", "select count(*)=0 from im_events where connection=$1", d.key)
	callback("wrong-code", "wrong-fixture", "绑定 "+otherCode, nil)
	poll("绑定码已失效")
	callback("binding", "bind-msg", code.Command, nil)
	check("both credentials encrypted before acknowledgement and no pre-commit binding", `select state='received'
      and payload is not null and reply_target is not null and binding_id is null
      and position($2::bytea in reply_target)=0 and position($3::bytea in payload)=0
      from im_events where(event_id,connection)=('binding',$1)`, d.key, []byte(target), []byte(code.Command))
	poll("钉钉 身份已绑定")
	callback("first-draft", "project-msg", "新建 核对培训反馈", nil)
	poll("打开企业网页中的原任务")
	var project string
	if err := db.QueryRow(ctx, `select t.project_id from im_threads t join im_bindings b on b.id=t.binding_id
       where (b.connection,t.chat_id,t.thread_id)=($1,$2,$2)`, d.key, chat).Scan(&project); err != nil {
		t.Fatal(err)
	}
	callback("followup", "same-followup", "补充 第一期需要独立归并意见", nil)
	callback("followup-retry", "same-followup", "补充 第一期需要独立归并意见", nil)
	poll("已加入原项目对话")
	poll("")
	check("follow-up used the same project and was processed once", "select count(*)=2 and count(*) filter(where body='第一期需要独立归并意见')=1 from messages where project_id=$1", project)

	agent, run := uuid.NewString(), uuid.NewString()
	sql("insert into agents(id,owner,team_id) values($1,$2,$3)", agent, actor, team)
	sql(`insert into agent_revisions(agent_id,revision,name,instructions,tool,created_by) values($1,1,'Fixture','任务','{"operation":"docx"}',$2)`, agent, actor)
	sql("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'培训反馈',$2)", project, actor)
	sql("insert into nodes(project_id,plan_version,ordinal,agent_id,agent_revision) values($1,1,1,$2,1)", project, agent)
	sql("update projects set plan_version=1,stage='running' where id=$1", project)
	sql(`insert into runs(id,project_id,plan_version,actor,charge_team,status,max_calls,request_key,request_hash)
        values($1,$2,1,$3,$4,'running',1,$5,'fixture')`, run, project, actor, team, run)
	var wait int
	if err := db.QueryRow(ctx, `insert into step_dispatch_waits(run_id,ordinal,plan_version,agent_id,agent_revision,reason,note)
       values($1,1,1,$2,1,'resource_denied','diagnostic') returning wait_revision`, run, agent).Scan(&wait); err != nil {
		t.Fatal(err)
	}
	action := fmt.Sprintf("复查 %s 1 %d 1", run, wait)
	callback("todos", "todos-msg", "待办", nil)
	poll(action)
	callback("recheck", "recheck-msg", action, nil)
	poll("已提交步骤复查")
	callback("late-recheck", "late-recheck-msg", action, nil)
	poll("步骤待办已变化")
	check("one shared business control and no extra model call", `select (select count(*)=1 from controls where run_id=$1 and wake_ordinal=1)
       and not exists(select from invocations where run_id=$1)`, run)

	callback("expire-before-command", "outdated-msg", "补充 过期消息", nil)
	sql("update im_events set reply_expires_at=now()-interval '1 second' where (connection,event_id)=($1,'expire-before-command')", d.key)
	if err := d.expireJobs(ctx); err != nil {
		t.Fatal(err)
	}
	poll("")
	check("expired session erased its URL and skipped the write", `select (select state='suppressed' and reply_target is null and payload is null
        from im_events where (connection,event_id)=($1,'expire-before-command')) and (select count(*)=2 from messages where project_id=$2)`, d.key, project)
	callback("progress-before-unlink", "progress-msg", "进度", nil)
	if err := d.processNext(ctx); err != nil {
		t.Fatal(err)
	}
	if res := req("DELETE"); res.Code != 204 {
		t.Fatalf("unexpected unlink response: %d", res.Code)
	}
	poll("")
	check("unlink suppressed prepared reply, preserving the other channel", `select (select state='suppressed' and reply_target is null from im_events
      where(connection,event_id)=($1,'progress-before-unlink')) and exists(select from im_binding_codes where (connection,member_id)=($2,$3))`, d.key, otherKey, actor)

	ping.Headers.Topic = "disconnect"
	if err := ws.WriteJSON(ping); err != nil {
		t.Fatal(err)
	}
	ws = connect()
	ping.Headers.Topic = "ping"
	roundtrip(ping)
	if opens.Load() != 2 {
		t.Fatal("disconnect did not acquire one new registration ticket")
	}
}
