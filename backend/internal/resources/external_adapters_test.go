package resources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adapterAllowlist struct{}

func (adapterAllowlist) QueryRow(context.Context, string, ...any) pgx.Row { return adapterAllowlist{} }
func (adapterAllowlist) Scan(out ...any) error {
	*out[0].(*[]string) = []string{"127.0.0.1/32"}
	return nil
}

func TestExternalChatTemplatesSendTrustedTextAndStreamResults(t *testing.T) {
	const brief = "读取公开采购规则"
	const answer = "公开结果"
	run, actor := uuid.NewString(), uuid.NewString()
	type fixture struct {
		template, model, contentType, response string
		body                                   map[string]any
	}
	openAI := "data: {\"choices\":[{\"delta\":{\"content\":\"公开\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"结果\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	messages := []any{map[string]any{"role": "user", "content": brief}}
	cases := []fixture{
		{template: "openai", model: "assistant", contentType: "text/event-stream", response: openAI,
			body: map[string]any{"model": "assistant", "messages": messages, "stream": true}},
		{template: "fastgpt", model: "fixture-app", contentType: "text/event-stream", response: openAI,
			body: map[string]any{"appId": "fixture-app", "messages": messages, "stream": true, "detail": false}},
		{template: "dify", contentType: "text/event-stream",
			response: "data: {\"event\":\"agent_message\",\"answer\":\"公开\"}\n\n" +
				"data: {\"event\":\"agent_message\",\"answer\":\"结果\"}\n\n" +
				"data: {\"event\":\"message\",\"answer\":\"公开结果\"}\n\n" +
				"data: {\"event\":\"message_end\"}\n\n",
			body: map[string]any{"inputs": map[string]any{}, "query": brief, "response_mode": "streaming", "user": actor, "auto_generate_name": false}},
		{template: "coze", model: "fixture-bot", contentType: "text/event-stream",
			response: "event: conversation.message.delta\ndata: {\"id\":\"one\",\"role\":\"assistant\",\"type\":\"answer\",\"content_type\":\"text\",\"content\":\"公开\"}\n\n" +
				"event: conversation.message.delta\ndata: {\"id\":\"one\",\"role\":\"assistant\",\"type\":\"answer\",\"content_type\":\"text\",\"content\":\"结果\"}\n\n" +
				"event: conversation.message.completed\ndata: {\"id\":\"one\",\"role\":\"assistant\",\"type\":\"answer\",\"content_type\":\"text\",\"content\":\"公开结果\"}\n\n" +
				"event: conversation.chat.completed\ndata: {}\n\n",
			body: map[string]any{"bot_id": "fixture-bot", "user_id": actor, "stream": true, "auto_save_history": false,
				"additional_messages": []any{map[string]any{"role": "user", "content": brief, "content_type": "text"}}}},
		{template: "langgraph", model: "fixture-graph", contentType: "text/event-stream",
			response: "event: metadata\ndata: {\"run_id\":\"graph-run\"}\n\n" +
				"event: values\ndata: {\"messages\":[{\"type\":\"ai\",\"content\":\"公开\"}]}\n\n" +
				"event: values\ndata: {\"messages\":[{\"type\":\"ai\",\"content\":\"公开结果\"}]}\n\n" +
				"event: end\n\n",
			body: map[string]any{"assistant_id": "fixture-graph", "input": map[string]any{"messages": messages}, "stream_mode": "values"}},
	}
	for _, tt := range cases {
		t.Run(tt.template, func(t *testing.T) {
			fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Alliance-Run-Id") != run || r.Header.Get("X-Alliance-User-Id") != actor ||
					r.Header.Get("X-Alliance-Turn-Id") != "2" || r.Header.Get("traceparent") == "" {
					t.Errorf("trusted tracing fields missing: %+v", r.Header)
				}
				if tt.template == "langgraph" {
					if r.Header.Get("X-Api-Key") != "fixture-secret" || r.Header.Get("Authorization") != "" {
						t.Error("LangGraph key used the wrong authorization header")
					}
				} else if r.Header.Get("Authorization") != "Bearer fixture-secret" {
					t.Error("external template authorization missing")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, tt.body) {
					t.Errorf("unexpected external message: got %+v, want %+v (%v)", body, tt.body, err)
				}
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = fmt.Fprint(w, tt.response)
			}))
			defer fixture.Close()
			before := providerClient
			providerClient = func() (*http.Client, error) { return fixture.Client(), nil }
			defer func() { providerClient = before }()
			model := Model{Endpoint: fixture.URL + "/external", Key: "fixture-secret", External: true, StreamText: true,
				OutputOperation: "text", Policy: defaultRemotePolicy(), ExternalTrace: ExternalTrace{RunID: run, ActorID: actor, Turn: 2},
				Adapter:  &AdapterSpec{Protocol: AdapterOpenAICompat, Template: tt.template, Model: tt.model, Stream: true},
				outbound: egress.Policy{DB: adapterAllowlist{}}}
			var streamed strings.Builder
			out, err := model.StreamTextReply(t.Context(), []Message{{Role: "system", Content: "internal-only rule"}, {Role: "user", Content: brief}},
				func(s string) { streamed.WriteString(s) })
			if err != nil || out.Text != answer || out.FinishReason != "stop" || streamed.String() != answer {
				t.Fatalf("unexpected adapter output %+v, stream=%q, err=%v", out, streamed.String(), err)
			}
		})
	}
}

func TestExternalAdaptersRejectUnfinishedStreams(t *testing.T) {
	tests := []struct {
		spec string
		body string
	}{
		{spec: "dify", body: "data: {\"event\":\"message\",\"answer\":\"partial\"}\n\n"},
		{spec: "dify", body: "data: {\"event\":\"workflow_started\"}\n\ndata: {\"event\":\"message\",\"answer\":\"partial\"}\n\n" +
			"data: {\"event\":\"message_end\"}\n\ndata: {\"event\":\"workflow_finished\",\"data\":{\"status\":\"failed\"}}\n\n"},
		{spec: "coze", body: "event: conversation.message.delta\ndata: {\"id\":\"one\",\"role\":\"assistant\",\"type\":\"answer\",\"content_type\":\"text\",\"content\":\"partial\"}\n\n"},
		{spec: "langgraph", body: "event: values\ndata: {\"messages\":[{\"type\":\"ai\",\"content\":\"partial\"}]}\n\nevent: error\ndata: {}\n\n"},
	}
	for _, tt := range tests {
		spec := AdapterSpec{Template: tt.spec}
		if _, err := spec.decode("text/event-stream", strings.NewReader(tt.body), nil); err == nil {
			t.Errorf("%s turned an unfinished reply into a result", tt.spec)
		}
	}
	spec := AdapterSpec{Template: "dify"}
	reply, err := spec.decode("application/json", strings.NewReader(`{"answer":"blocking response"}`), nil)
	if err != nil || reply.Text != "blocking response" {
		t.Fatalf("Dify JSON fallback: %+v %v", reply, err)
	}
}

func TestAdapterCredentialRemainsOnApprovedAuthority(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		t.Error("external adapter escaped its approved destination")
	}))
	defer target.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	original := providerClient
	providerClient = func() (*http.Client, error) { return source.Client(), nil }
	defer func() { providerClient = original }()
	model := Model{Endpoint: source.URL + "/chat", Key: "fixture-secret", External: true, Policy: defaultRemotePolicy(),
		Adapter: &AdapterSpec{Protocol: AdapterOpenAICompat, Template: "openai", Model: "fixture"}, outbound: egress.Policy{DB: adapterAllowlist{}}}
	if _, err := model.Generate(t.Context(), []Message{{Role: "user", Content: "fixture"}}); err == nil || reached.Load() {
		t.Fatalf("redirect from %s was not rejected: %v", source.URL, err)
	}
}

func TestAdapterDraftPinsProtocolAndCredentialToRevision(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := NewVault(strings.Repeat("cf", 32))
	if err != nil {
		t.Fatal(err)
	}
	c := Catalog{DB: db, Vault: vault}
	actor, team := uuid.NewString(), uuid.NewString()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into teams(id,name) values($1,$1::uuid::text)", team)
	exec("insert into enterprise(name,default_team) values('Adapter fixture',$1) on conflict do nothing", team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'adapter maintainer','','member')",
		actor, actor+"@example.test")
	exec("insert into team_members(team_id,member_id) values($1,$2)", team, actor)
	exec("insert into outbound_host_allowlist(host,port) values('external.example.test',443) on conflict do nothing")
	draft := func(id, template, model, key string, expected int, keep bool) (*httptest.ResponseRecorder, error) {
		content := fmt.Sprintf(`{"id":%q,"team":%q,"expected":%d,"endpoint":"https://external.example.test/chat",
			"name":"Fixture adapter","description":"Reusable fixture","key":%q,"keepCredential":%t,
			"adapter":{"protocol":"openai_compat","template":%q,"model":%q,"stream":true}}`, id, team, expected, key, keep, template, model)
		w := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(content))
		return w, c.saveAdapter(w, req, identity.Member{ID: actor})
	}
	created, err := draft("", "dify", "", "fixture-key", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &saved); err != nil || saved.ID == "" || strings.Contains(created.Body.String(), "fixture-key") {
		t.Fatalf("unexpected create response: %s %v", created.Body, err)
	}
	first, instruction, err := c.Resolve(ctx, db, actor, saved.ID, 1)
	if err != nil || !first.External || first.Adapter == nil || first.Adapter.Template != "dify" ||
		!first.StreamText || first.Key != "fixture-key" || instruction != "Reusable fixture" {
		t.Fatalf("adapter binding was not resolved correctly: %+v %v", first.Adapter, err)
	}
	if _, err := draft(saved.ID, "langgraph", "fixture-graph", "", 1, true); err == nil {
		t.Fatal("credential was reused with changed authentication scheme")
	}
	if _, err := draft(saved.ID, "dify", "", "", 1, true); err != nil {
		t.Fatal(err)
	}
	second, _, err := c.Resolve(ctx, db, actor, saved.ID, 2)
	if err != nil || second.Adapter == nil || second.Key != "fixture-key" {
		t.Fatalf("same-endpoint edit lost its credential: %v", err)
	}
	var sealed []byte
	var cardURL string
	err = db.QueryRow(ctx, `select key_override,card->>'url' from agent_revisions where agent_id=$1 and revision=$2`,
		saved.ID, 2).Scan(&sealed, &cardURL)
	if err != nil || cardURL != "https://external.example.test/chat" || len(sealed) == 0 || strings.Contains(string(sealed), "fixture-key") {
		t.Fatalf("adapter revision persisted incorrectly: %v", err)
	}
}
