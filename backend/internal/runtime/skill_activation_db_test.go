package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/services"
	"alliance/internal/skills"
	"alliance/internal/web"
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

func TestSkillActivationPostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	prompts := make(chan string, 16)
	release := make(chan struct{})
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) < 2 {
			http.Error(w, "invalid model messages", http.StatusBadRequest)
			return
		}
		prompts <- input.Messages[0].Content
		if strings.Contains(input.Messages[1].Content, "停用途中") {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"已核对"},"finish_reason":"stop"}],"usage":{"total_tokens":15}}`)
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)
	root := t.TempDir()
	t.Setenv("OBJECT_ROOT", root)
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	worker.Catalog.Routes(routes, auth)
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	services.Server{DB: db}.Routes(routes, auth)
	seed, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var actor, team, model string
	err = db.QueryRow(ctx, `select r.actor,r.charge_team,a.resource_id from runs r join nodes n
        on(n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1) join agent_revisions a
        on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision) where r.id=$1`, seed.Run).Scan(&actor, &team, &model)
	if err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '4 minutes')", web.Hash(token), actor)
	call := func(method, path string, input any, want int) []byte {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", uuid.NewString())
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: %d instead of %d: %s", method, path, res.Code, want, res.Body)
		}
		return res.Body.Bytes()
	}
	readRef := func(raw []byte) skills.Ref {
		t.Helper()
		var ref skills.Ref
		if err := json.Unmarshal(raw, &ref); err != nil || ref.ID == "" {
			t.Fatalf("missing id: %s %v", raw, err)
		}
		return ref
	}
	publish := func(name, marker string, terms []string, deps []skills.Ref, expected int) skills.Ref {
		t.Helper()
		spec, err := json.Marshal(map[string]any{"activationTerms": terms, "dependencies": deps})
		if err != nil {
			t.Fatal(err)
		}
		raw := call("POST", "/api/skills", map[string]any{"team": team, "expected": expected,
			"archive": packageForExecution(t, map[string]string{
				"SKILL.md":      "---\nname: " + name + "\ndescription: 本团队受控流程\n---\n" + marker,
				"manifest.json": string(spec),
			}),
		}, 201)
		return readRef(raw)
	}
	base := publish("shared-procedure", "依赖流程摘要", nil, nil, 0)
	style := publish("always-style", "固定团队格式", nil, nil, 0)
	quote := publish("quote-proof", "报价修订一的正文", []string{"供应商报价"}, []skills.Ref{base}, 0)
	ledger := publish("ledger-review", "对账修订一的正文", []string{"月度对账"}, []skills.Ref{base}, 0)
	draft := readRef(call("POST", "/api/agents", map[string]any{"name": "按需核对助手", "instructions": "按确认目标核对。",
		"resource": model, "resourceRevision": 1, "skills": []skills.Ref{style, quote, ledger}}, 201))
	agentPath := "/api/agents/" + draft.ID
	assertPrompt := func(want, absent []string) {
		t.Helper()
		select {
		case prompt := <-prompts:
			for _, marker := range want {
				if strings.Count(prompt, marker) != 1 {
					t.Fatalf("required content %q missing or duplicated: %s", marker, prompt)
				}
			}
			for _, marker := range absent {
				if strings.Contains(prompt, marker) {
					t.Fatalf("dormant content %q was loaded: %s", marker, prompt)
				}
			}
		case <-ctx.Done():
			t.Fatal("provider did not receive the expected request")
		}
	}
	trial := func(input string) string {
		t.Helper()
		raw := call("POST", agentPath+"/validate", map[string]any{"revision": 1, "input": input,
			"expected": "已核对", "authorizeOneCall": true}, 200)
		var result struct{ ID, Status string }
		if err := json.Unmarshal(raw, &result); err != nil || result.Status != "passed" || result.ID == "" {
			t.Fatalf("trial did not pass: %s %v", raw, err)
		}
		return result.ID
	}
	trial("整理会议记录")
	assertPrompt([]string{"固定团队格式"}, []string{"报价修订一", "对账修订一", "依赖流程摘要"})
	call("POST", agentPath+"/publish", map[string]int{"revision": 1}, 409)
	trial("核对供应商报价")
	assertPrompt([]string{"报价修订一", "依赖流程摘要", "固定团队格式"}, []string{"对账修订一"})
	call("POST", agentPath+"/publish", map[string]int{"revision": 1}, 409)
	ledgerProof := trial("用 @LEDGER-REVIEW 核对数据")
	assertPrompt([]string{"对账修订一", "依赖流程摘要", "固定团队格式"}, []string{"报价修订一"})
	call("POST", agentPath+"/publish", map[string]int{"revision": 1}, 204)
	call("POST", agentPath+"/evidence/"+ledgerProof+"/withdraw", map[string]string{"reason": "需要使用更全面的对账样例"}, 204)
	var verified bool
	if err := db.QueryRow(ctx, "select agent_verified($1,1)", draft.ID).Scan(&verified); err != nil || verified {
		t.Fatalf("withdrawing only the conditional trial left the Agent verified: %v %v", verified, err)
	}
	trial("补做月度对账样例")
	assertPrompt([]string{"对账修订一", "依赖流程摘要"}, []string{"报价修订一"})
	publish("quote-proof", "新版采购正文", []string{"采购单"}, nil, 1)
	service := readRef(call("POST", "/api/services", map[string]string{
		"name": "按需 Skill 协作服务", "agent": draft.ID, "instructions": "可以办理供应商报价、月度对账与会议记录。",
	}, 201))
	launch := func(input string) (Step, string) {
		t.Helper()
		raw := call("POST", "/api/services/"+service.ID+"/chat", map[string]any{
			"version": 1, "input": input, "authorizeCalls": 1,
			"attachment": services.Attachment{Name: "note.txt", Data: []byte("附件正文提到供应商报价和 @ledger-review。")},
		}, 202)
		var result struct{ ID, Project string }
		if err := json.Unmarshal(raw, &result); err != nil || result.Project == "" || result.ID == "" {
			t.Fatalf("missing service run: %s %v", raw, err)
		}
		return Step{Run: result.ID, Ordinal: 1}, result.Project
	}
	var object string
	if err := db.QueryRow(ctx, "select object_hash from skill_files where (skill_id,revision,path)=($1,1,'SKILL.md')",
		quote.ID).Scan(&object); err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(root, object)
	original, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(objectPath, []byte("corrupt dormant fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ordinary, ordinaryProject := launch("整理会议记录")
	if result, err := worker.ExecuteStep(ctx, ordinary); err != nil || result.Wait || result.Stop {
		t.Fatalf("dormant Skill was opened during an unrelated service task: %+v %v", result, err)
	}
	assertPrompt([]string{"固定团队格式"}, []string{"报价修订一", "对账修订一", "依赖流程摘要"})
	needsQuote, _ := launch("核对供应商报价")
	if result, err := worker.ExecuteStep(ctx, needsQuote); err != nil || !result.Wait {
		t.Fatalf("corrupt selected Skill should block: %+v %v", result, err)
	}
	var cost int
	if err := db.QueryRow(ctx, "select count(*) from spend where run_id=$1", needsQuote.Run).Scan(&cost); err != nil || cost != 0 {
		t.Fatalf("blocked Skill attempted dispatch: %d %v", cost, err)
	}
	if err = os.WriteFile(objectPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	active, activeProject := launch("停用途中，核对供应商报价")
	done := make(chan struct{})
	var outcome Result
	var executeErr error
	go func() {
		defer close(done)
		outcome, executeErr = worker.ExecuteStep(ctx, active)
	}()
	assertPrompt([]string{"报价修订一", "依赖流程摘要", "固定团队格式"}, []string{"对账修订一", "新版采购正文"})
	before := call("GET", "/api/projects/"+activeProject, nil, 200)
	if !bytes.Contains(before, []byte(`"name": "quote-proof"`)) && !bytes.Contains(before, []byte(`"name":"quote-proof"`)) {
		t.Fatal("selected Skill metadata was not visible in the project")
	}
	call("POST", "/api/skills/"+quote.ID+"/revisions/1/revoke", map[string]string{"reason": "版本停用试验"}, 204)
	close(release)
	<-done
	if executeErr != nil || !outcome.Wait {
		t.Fatalf("late reply after Skill was revoked: %+v %v", outcome, executeErr)
	}
	var usable, charged, candidate bool
	err = db.QueryRow(ctx, `select run_working_set_usable($1,1,1),
       exists(select from spend where run_id=$1),exists(select from artifacts where run_id=$1)`, active.Run).Scan(&usable, &charged, &candidate)
	if err != nil || usable || !charged || candidate {
		t.Fatalf("revocation did not preserve cost and stop candidate: %v %v %v %v", usable, charged, candidate, err)
	}
	ordinaryBody := call("GET", "/api/projects/"+ordinaryProject, nil, 200)
	if !bytes.Contains(ordinaryBody, []byte("always-style")) {
		t.Fatal("revocation affected a past call that only loaded the unrelated, required Skill")
	}
}
