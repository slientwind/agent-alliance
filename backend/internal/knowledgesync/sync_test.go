package knowledgesync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReadOnlyDocumentProviders(t *testing.T) {
	client := providerClient{httpClient: func(_ context.Context, _ string) (*http.Client, error) {
		return &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
			body, code := "", http.StatusOK
			switch r.URL.Host + r.URL.Path {
			case "open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal":
				var credentials map[string]string
				if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil || r.Method != "POST" ||
					credentials["app_id"] != "fixture-app" || credentials["app_secret"] != "fixture-secret" {
					t.Error("Feishu authentication did not send exact credentials")
				}
				body = `{"code":0,"tenant_access_token":"tenant-fixture"}`
			case "open.feishu.cn/open-apis/docx/v1/documents/Doc1/raw_content":
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer tenant-fixture" {
					t.Error("Feishu document read had incorrect credentials")
				}
				body = `{"code":0,"data":{"content":"飞书验收文本"}}`
			case "fixture.atlassian.net/wiki/api/v2/pages/432":
				user, pass, ok := r.BasicAuth()
				if r.Method != "GET" || !ok || user != "fixture@example.test" || pass != "fixture-secret" ||
					r.URL.Query().Get("body-format") != "storage" {
					t.Error("Confluence document read had incorrect credentials or format")
				}
				body = `{"id":"432","status":"current","body":{"storage":{"value":"<h1>Confluence</h1><p>正文<script>unsafe()</script> &amp; 原文</p>"}}}`
			case "fixture.atlassian.net/wiki/api/v2/pages/543":
				code = 403
			case "mcp-gw.dingtalk.com/server/fixture":
				var request struct {
					Method string
					ID     json.RawMessage
					Params struct {
						Name      string
						Arguments map[string]string
					}
				}
				if r.URL.Query().Get("key") != "fixture-secret" || r.Method != "POST" || json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("invalid DingTalk MCP request")
				}
				switch request.Method {
				case "initialize":
					body = `{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"DingTalk","version":"fixture"}}}`
				case "notifications/initialized":
					code = 202
				case "tools/call":
					if request.Params.Name != "get_document_content" || request.Params.Arguments["nodeId"] != "Ding1" ||
						request.Params.Arguments["format"] != "markdown" || len(request.Params.Arguments) != 2 {
						t.Error("DingTalk sync attempted a different tool or document")
					}
					body = `{"jsonrpc":"2.0","id":` + string(request.ID) + `,"result":{"content":[{"type":"text","text":"{\"content\":\"钉钉验收正文 https://alidocs2.oss-cn-zhangjiakou.aliyuncs.com/res/att/a.txt?key=temporary\"}"}]}}`
				default:
					t.Errorf("unexpected DingTalk RPC %q: %v", request.Method, r.Header)
				}
			default:
				t.Errorf("unexpected provider destination %s", r.URL.Host+r.URL.Path)
			}
			return &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, StatusCode: code,
				Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})}, nil
	}}
	for _, tc := range []struct {
		in     providerRequest
		result string
		err    error
	}{
		{providerRequest{"feishu", settings{Account: "fixture-app", Document: "Doc1"}, "fixture-secret"}, "飞书验收文本", nil},
		{providerRequest{"confluence", settings{Origin: "https://fixture.atlassian.net", Account: "fixture@example.test", Document: "432"}, "fixture-secret"}, "Confluence\n\n正文 & 原文", nil},
		{providerRequest{"confluence", settings{Origin: "https://fixture.atlassian.net", Account: "fixture@example.test", Document: "543"}, "fixture-secret"}, "", errDenied},
		{providerRequest{"dingtalk", settings{Document: "Ding1"}, "https://mcp-gw.dingtalk.com/server/fixture?key=fixture-secret"}, "钉钉验收正文 [附件链接]", nil},
	} {
		if !tc.in.settings.valid(tc.in.provider) || !credentialValid(tc.in.provider, tc.in.secret) {
			t.Fatalf("fixture did not meet configuration rules: %s", tc.in.provider)
		}
		got, err := client.fetch(t.Context(), tc.in)
		if !errors.Is(err, tc.err) || got != tc.result {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.in.provider, got, err, tc.result, tc.err)
		}
	}
	for _, target := range []string{
		"https://mcp-gw.dingtalk.com.evil.test/server/fixture?key=x",
		"https://mcp-gw.dingtalk.com/server/../server/fixture?key=x",
		"https://mcp-gw.dingtalk.com/server/fixture?key=x&key=y",
		"https://mcp-gw.dingtalk.com@evil.test/server/fixture?key=x",
	} {
		if credentialValid("dingtalk", target) {
			t.Error("accepted an invalid DingTalk authority or credential")
		}
	}
}

func TestSyncPublishesExactVersionsAndDiscardsRevokedWork(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("ac", 32))
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	actor, other, team, project, id := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, member := range []string{actor, other} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','admin')", member, member+"@example.test")
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, actor, other)
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'Fixture project')", project, actor, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,false)", project, actor, other)
	t.Cleanup(func() {
		// This fixture uses generated project ids. Only the scheduled input row needs removal.
		db, err := database.Open(context.Background(), dsn)
		if err == nil {
			defer db.Close()
			_, _ = db.Exec(context.Background(), "delete from enterprise_knowledge_sources where id=$1", id)
		}
	})
	action := func(f identity.Action, user string, status int, body any) string {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPut, "/", strings.NewReader(string(raw)))
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		err = f(w, req, identity.Member{ID: user})
		var problem web.Problem
		if errors.As(err, &problem) && problem.Status == status {
			return problem.Message
		}
		if err != nil || w.Code != status {
			t.Fatalf("response %d: %v %s; expected %d", w.Code, err, w.Body, status)
		}
		return w.Body.String()
	}
	secret := "provider-fixture-secret"
	enabled := true
	in := connectionInput{Name: "同步说明", Provider: "feishu", Settings: settings{Account: "FixtureApp", Document: "DocExact"},
		Project: project, Secret: &secret, Enabled: &enabled}
	value := "first revision"
	var resolved error
	var whileFetching func()
	s := Server{DB: db, Vault: vault, Objects: knowledge.Objects{Root: t.TempDir()}, fetch: func(_ context.Context, request providerRequest) (string, error) {
		if request.secret != secret || request.settings.Document != "DocExact" {
			t.Error("sync received another document or credential")
		}
		if whileFetching != nil {
			whileFetching()
		}
		return value, resolved
	}}
	action(s.configure, other, 404, in)
	action(s.configure, actor, 200, in)
	action(s.configure, actor, 409, in)
	if got := action(s.list, actor, 200, nil); strings.Contains(got, secret) || !strings.Contains(got, `"hasCredential":true`) {
		t.Fatalf("credential surfaced in configuration: %s", got)
	}
	var cipher []byte
	if err := db.QueryRow(ctx, "select encrypted_credentials from enterprise_knowledge_sources where id=$1", id).Scan(&cipher); err != nil ||
		strings.Contains(string(cipher), secret) {
		t.Fatalf("credential not protected: %v", err)
	}
	if got, err := vault.Open(cipher, sourceScope(id)); err != nil || got != secret {
		t.Fatal("stored credential did not round trip")
	}
	run := func() {
		t.Helper()
		if ran, err := s.syncOne(ctx); err != nil || !ran {
			t.Fatalf("sync not processed: %v %v", ran, err)
		}
	}
	inspect := func(state string, version int) (string, string) {
		t.Helper()
		var gotState, source, verdict string
		var ver int
		err := db.QueryRow(ctx, `select status,coalesce(published_version,0),coalesce(source_id::text,''),verdict
			from enterprise_knowledge_sources where id=$1`, id).Scan(&gotState, &ver, &source, &verdict)
		if err != nil || gotState != state || ver != version {
			t.Fatalf("unexpected sync state %s v%d %s %v", gotState, ver, verdict, err)
		}
		return source, verdict
	}
	run()
	source, _ := inspect("ready", 1)
	action(s.queue, actor, 202, nil)
	run()
	inspect("ready", 1)
	value = "updated document text"
	action(s.queue, actor, 202, nil)
	run()
	inspect("ready", 2)
	var text string
	if err := db.QueryRow(ctx, "select extracted from source_versions where source_id=$1 and version=2", source).Scan(&text); err != nil || text != value {
		t.Fatalf("local version is incorrect: %q, %v", text, err)
	}
	resolved = errDenied
	action(s.queue, actor, 202, nil)
	run()
	if _, verdict := inspect("unavailable", 0); verdict != "access" {
		t.Fatalf("permission loss verdict: %s", verdict)
	}
	var revoked bool
	if err := db.QueryRow(ctx, "select revoked_at is not null from sources where id=$1", source).Scan(&revoked); err != nil || !revoked {
		t.Fatal("upstream revocation left the local document visible")
	}
	resolved = nil
	action(s.queue, actor, 202, nil)
	whileFetching = func() {
		off := false
		in.Expected, in.Secret, in.Enabled = 1, nil, &off
		action(s.configure, actor, 200, in)
	}
	run()
	inspect("disabled", 0)
	var count int
	if err := db.QueryRow(ctx, "select count(*) from sources where project_id=$1", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("a stale provider result created a new source")
	}
}
