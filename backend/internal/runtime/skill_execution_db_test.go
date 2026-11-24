package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSkillPackPostgresExecution(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault, err := resources.NewVault(strings.Repeat("a1", 32))
	if err != nil {
		t.Fatal(err)
	}
	prompts := make(chan string, 8)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Messages []resources.Message }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) == 0 {
			http.Error(w, "missing messages", http.StatusBadRequest)
			return
		}
		prompts <- req.Messages[0].Content
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已核对"},"finish_reason":"stop"}],"usage":{"total_tokens":18}}`)
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	objectRoot := t.TempDir()
	t.Setenv("OBJECT_ROOT", objectRoot)
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	worker.Catalog.Routes(routes, identity.Server{DB: db})
	step, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	fetchAccount := func(run string) (string, string, string, string, string) {
		var project, actor, team, model string
		if err := db.QueryRow(ctx, `select r.project_id,r.actor,r.charge_team,ar.resource_id
            from runs r join nodes n on (n.project_id,n.plan_version)=(r.project_id,r.plan_version) and n.ordinal=2
            join agent_revisions ar on (ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, run).
			Scan(&project, &actor, &team, &model); err != nil {
			t.Fatal(err)
		}
		lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
		token := web.Token()
		lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '2 minutes')", web.Hash(token), actor)
		return project, actor, team, model, token
	}
	project, actor, team, model, token := fetchAccount(step.Run)
	foreign, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	_, _, otherTeam, otherModel, otherToken := fetchAccount(foreign.Run)
	type receipt struct {
		ID, Digest string
		Revision   int
	}
	publish := func(name, body string, files map[string]string, expected, status int) receipt {
		if files == nil {
			files = make(map[string]string)
		}
		files["SKILL.md"] = "---\nname: " + name + "\ndescription: 核对本团队记录\n---\n" + body
		res := skillTestRequest(t, routes, token, "POST", "/api/skills", map[string]any{
			"team": team, "expected": expected, "archive": packageForExecution(t, files),
		}, status)
		var published receipt
		if status == http.StatusCreated {
			if err := json.Unmarshal(res, &published); err != nil || published.ID == "" || len(published.Digest) != 64 {
				t.Fatalf("invalid publication: %s", res)
			}
		}
		return published
	}
	if body := skillTestRequest(t, routes, token, "GET", "/api/skills", nil, http.StatusOK); strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("unexpected empty catalog: %s", body)
	}
	dependency := publish("base-method", "基础核对流程", nil, 0, 201)
	manifest, _ := json.Marshal(map[string]any{
		"dependencies": []map[string]any{{"id": dependency.ID, "revision": dependency.Revision}},
		"contextFiles": []string{"references/format.md"},
	})
	pack := publish("quote-review", "第一版检查清单", map[string]string{
		"manifest.json": string(manifest), "references/format.md": "确认有效期",
		"resources/unused.txt": "禁止出现在模型上下文",
	}, 0, 201)
	publish("quote-review", "过期发布请求", nil, 0, 409)
	revised := publish("quote-review", "新版检查清单", nil, 1, 201)
	if revised.ID != pack.ID || revised.Revision != 2 || revised.Digest == pack.Digest {
		t.Fatalf("unexpected revised identity: %+v %+v", pack, revised)
	}

	draft := func(resource string) map[string]any {
		return map[string]any{"name": "核对助手", "instructions": "团队任务职责", "resource": resource, "resourceRevision": 1,
			"skills": []map[string]any{{"id": pack.ID, "revision": pack.Revision}}}
	}
	skillTestRequest(t, routes, otherToken, "POST", "/api/skills", map[string]any{"team": team,
		"archive": packageForExecution(t, map[string]string{"SKILL.md": "---\nname: foreign-pack\ndescription: test\n---\nblocked"})}, 403)
	skillTestRequest(t, routes, otherToken, "POST", "/api/agents", draft(otherModel), 409)
	if body := skillTestRequest(t, routes, otherToken, "GET", "/api/skills", nil, 200); strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("foreign catalog exposes team %s: %s", otherTeam, body)
	}
	body := skillTestRequest(t, routes, token, "POST", "/api/agents", draft(model), 201)
	var agent receipt
	if err := json.Unmarshal(body, &agent); err != nil || agent.ID == "" || agent.Revision != 1 {
		t.Fatalf("invalid Agent draft: %s", body)
	}
	agentPath := "/api/agents/" + agent.ID
	checkPrompt := func() {
		t.Helper()
		select {
		case prompt := <-prompts:
			for _, text := range []string{"团队任务职责", "基础核对流程", "第一版检查清单", "确认有效期"} {
				if !strings.Contains(prompt, text) {
					t.Fatalf("missing %q in %q", text, prompt)
				}
			}
			if strings.Index(prompt, "基础核对流程") >= strings.Index(prompt, "第一版检查清单") ||
				strings.Contains(prompt, "新版检查清单") || strings.Contains(prompt, "禁止出现在模型上下文") {
				t.Fatalf("incorrect binding or load order: %s", prompt)
			}
		default:
			t.Fatal("model was not called")
		}
	}
	skillTestRequest(t, routes, token, "POST", agentPath+"/validate", map[string]any{
		"revision": 1, "input": "核对报价", "expected": "已核对", "authorizeOneCall": true,
	}, 200)
	checkPrompt()
	skillTestRequest(t, routes, token, "POST", agentPath+"/publish", map[string]int{"revision": 1}, 204)
	skillTestRequest(t, routes, token, "POST", agentPath+"/instructions", map[string]any{"expected": 1, "instructions": "团队任务职责与新版目标"}, 201)
	_, nextPrompt, err := worker.Catalog.Resolve(ctx, db, actor, agent.ID, 2)
	if err != nil || !strings.Contains(nextPrompt, "第一版检查清单") || strings.Contains(nextPrompt, "新版检查清单") {
		t.Fatalf("ordinary draft edit lost its pinned Skill: %s %v", nextPrompt, err)
	}
	lookupTestSQL(t, ctx, db, "update nodes set agent_id=$1,agent_revision=1,lookup_phase='' where project_id=$2 and ordinal=$3", agent.ID, project, step.Ordinal)
	if outcome, err := worker.ExecuteStep(ctx, step); err != nil || outcome.Wait {
		t.Fatalf("normal execution: %+v %v", outcome, err)
	}
	checkPrompt()
	var object string
	if err := db.QueryRow(ctx, "select object_hash from skill_files where skill_id=$1 and revision=1 and path='SKILL.md'", dependency.ID).Scan(&object); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(objectRoot, object), []byte("corrupted fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := worker.Catalog.Resolve(ctx, db, actor, agent.ID, 1); err == nil || !strings.Contains(err.Error(), "内容校验失败") {
		t.Fatalf("corrupt package was not rejected: %v", err)
	}
	skillTestRequest(t, routes, token, "POST", "/api/skills/"+dependency.ID+"/revisions/1/revoke", map[string]string{"reason": "团队流程已废止"}, 204)
	pending := Step{uuid.NewString(), step.Ordinal}
	lookupTestSQL(t, ctx, db, `insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls)
        select $2,project_id,plan_version,actor,'queued',charge_team,$3,request_hash,max_calls from runs where id=$1`, step.Run, pending.Run, pending.Run)
	if outcome, err := worker.ExecuteStep(ctx, pending); err != nil || !outcome.Wait {
		t.Fatalf("revoked dependency must block before dispatch: %+v %v", outcome, err)
	}
	var available bool
	var charges int
	err = db.QueryRow(ctx, "select agent_revision_ready($1,1),(select count(*) from spend where run_id=$2)", agent.ID, pending.Run).Scan(&available, &charges)
	if err != nil || available || charges != 0 || len(prompts) != 0 {
		t.Fatalf("revocation left access=%t, reservations=%d, requests=%d, err=%v", available, charges, len(prompts), err)
	}
}

func packageForExecution(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, body := range files {
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(file, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func skillTestRequest(t *testing.T, routes http.Handler, token, method, url string, data any, want int) []byte {
	t.Helper()
	body, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, url, bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
	res := httptest.NewRecorder()
	routes.ServeHTTP(res, req)
	if res.Code != want {
		t.Fatalf("%s %s: status=%d, want=%d, body=%s", method, url, res.Code, want, res.Body)
	}
	return res.Body.Bytes()
}
