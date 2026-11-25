package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/services"
	"alliance/internal/web"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTextAgentChoicePostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
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
	const source = "仅限这次任务的获准原件正文"
	const upstream = "应向下一步骤传递的独立核对记录"
	const remoteAnswer = "第三方候选已核对并返回意见"
	const localAnswer = "文本 Agent 已核对并返回意见"
	prompts := make(chan string, 32)
	selectedInputs := make(chan string, 32)
	var modelCalls, remoteCalls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID, Method string
			Messages   []resources.Message
			Params     struct {
				Message struct {
					Parts []struct{ Text string }
				}
			}
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "invalid provider input", http.StatusBadRequest)
			return
		}
		if in.Method == "message/send" {
			remoteCalls.Add(1)
			var contents []string
			for _, part := range in.Params.Message.Parts {
				contents = append(contents, part.Text)
			}
			payload := strings.Join(contents, "\n")
			if !strings.Contains(payload, "验证文本操作") {
				selectedInputs <- payload
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": in.ID,
				"result": map[string]any{"kind": "message", "parts": []map[string]string{{"kind": "text", "text": remoteAnswer}}}})
			return
		}
		modelCalls.Add(1)
		if len(in.Messages) < 2 {
			http.Error(w, "missing messages", http.StatusBadRequest)
			return
		}
		system, goal := in.Messages[0].Content, in.Messages[1].Content
		answer := upstream
		switch {
		case strings.Contains(system, harness.AgentChoiceInstructions):
			prompts <- goal
			answer = `{"choice":2}`
			if strings.Contains(goal, "需要人工选择") {
				answer = `{"choice":2,"input":"不应被采用的自拟委派正文"}`
			}
		case strings.Contains(system, "文本执行者"):
			var contents []string
			for _, message := range in.Messages[1:] {
				contents = append(contents, message.Content)
			}
			selectedInputs <- strings.Join(contents, "\n")
			answer = localAnswer
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"finish_reason": "stop", "message": map[string]string{"content": answer}}},
			"usage":   map[string]int{"total_tokens": 18},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)
	lookupTestSQL(t, ctx, db, `insert into outbound_host_allowlist(host,port,trusted_networks)
	 values($1,$2::text::integer,array['127.0.0.1/32'::cidr]) on conflict do nothing`, endpoint.Hostname(), endpoint.Port())
	t.Setenv("OBJECT_ROOT", t.TempDir())
	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	routes := http.NewServeMux()
	auth := identity.Server{DB: db}
	worker.Catalog.Routes(routes, auth)
	projects.Server{DB: db, Vault: vault}.Routes(routes, auth)
	knowledge.Server{DB: db, Objects: knowledge.ConfiguredObjects()}.Routes(routes, auth)
	services.Server{DB: db}.Routes(routes, auth)
	seed, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var actor, team, selector, resource string
	if err := db.QueryRow(ctx, `select r.actor,r.charge_team,n.agent_id,ar.resource_id from runs r join nodes n
        on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) join agent_revisions ar
        on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) where r.id=$1 and n.ordinal=1`, seed.Run).
		Scan(&actor, &team, &selector, &resource); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", actor)
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", selector)
	lookupTestSQL(t, ctx, db, "insert into enterprise(name,default_team) values('Fixture',$1) on conflict do nothing", team)
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(token), actor)
	call := func(t *testing.T, method, endpoint string, body any, status int, key ...string) []byte {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, endpoint, bytes.NewReader(raw))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		requestKey := uuid.NewString()
		if len(key) != 0 {
			requestKey = key[0]
		}
		req.Header.Set("Idempotency-Key", requestKey)
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: status=%d, want=%d: %s", method, endpoint, res.Code, status, res.Body)
		}
		return res.Body.Bytes()
	}
	idOf := func(t *testing.T, data []byte) string {
		t.Helper()
		var ref struct{ ID string }
		if err := json.Unmarshal(data, &ref); err != nil || ref.ID == "" {
			t.Fatalf("missing id: %s %v", data, err)
		}
		return ref.ID
	}
	localAgent := func(name, model, instructions string) string {
		t.Helper()
		id := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", id, actor, team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,resource_id,resource_revision,created_by)
			values($1,1,$2,$3,$4,1,$5)`, id, name, instructions, model, actor)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", id, actor)
		lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", id)
		return id
	}
	prep := localAgent("收集前置材料", resource, "生成独立前置记录")
	primary := localAgent("采购条款校对", resource, "文本执行者")
	expensive := uuid.NewString()
	key, err := vault.Seal("fixture", "model/"+expensive+"/1")
	if err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "insert into model_resources(id,team_id,name,created_by) values($1,$2,'text-specialist',$3)", expensive, team, actor)
	lookupTestSQL(t, ctx, db, `insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny)
        values($1,1,$2,'fixture-specialist',$3,0.8)`, expensive, provider.URL, key)
	alternative := localAgent("供应商合同校对", expensive, "文本执行者")
	remote := idOf(t, call(t, "POST", "/api/agents/cards", map[string]any{"team": team, "card": map[string]any{
		"name": "第三方合同核对", "version": "1", "protocolVersion": "0.3.0", "url": provider.URL,
		"preferredTransport": "JSONRPC", "skills": []map[string]string{{"id": "terms", "name": "核对合同"}},
	}}, 201))
	call(t, "POST", "/api/agents/"+remote+"/validate", map[string]any{
		"revision": 1, "input": "验证文本操作", "expected": remoteAnswer, "authorizeOneCall": true,
	}, 200)
	call(t, "POST", "/api/agents/"+remote+"/publish", map[string]int{"revision": 1}, 204)

	for _, tc := range []struct {
		name, alt string
		manual    bool
		block     bool
	}{
		{"本地候选与最高估价", alternative, false, false},
		{"受控第三方分派", remote, false, false},
		{"需要人工选择", remote, true, false},
		{"所选能力已停用", remote, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := uuid.NewString()
			lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$2,$3,$4)", project, actor, team, tc.name)
			lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, actor)
			lookupTestSQL(t, ctx, db, "insert into project_budgets(project_id) values($1) on conflict do nothing", project)
			root := "/api/projects/" + project
			doc := httptest.NewRequest(http.MethodPost, root+"/sources?name=private-note.txt", strings.NewReader(source))
			doc.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
			doc.Header.Set("Content-Type", "application/octet-stream")
			docRes := httptest.NewRecorder()
			routes.ServeHTTP(docRes, doc)
			if docRes.Code != 201 {
				t.Fatalf("upload: %d %s", docRes.Code, docRes.Body)
			}
			var rev int
			if err := db.QueryRow(ctx, "select revision from projects where id=$1", project).Scan(&rev); err != nil {
				t.Fatal(err)
			}
			plan := projects.PlanInput{Expected: rev, Brief: tc.name, Agents: []string{selector, prep, primary},
				AgentRevisions: []int{1, 1, 1}, Dependencies: [][]int{{}, {}, {1, 2}},
				Sources:     []projects.SourcePin{{ID: idOf(t, docRes.Body.Bytes()), Version: 1}},
				ToolChoices: []projects.ToolChoicePin{{Step: 3, Selector: 1, Alternative: selector, Revision: 1}}}
			call(t, "POST", root+"/plan", plan, 422)
			plan.ToolChoices[0].Alternative = tc.alt
			call(t, "POST", root+"/plan", plan, 204)
			var view struct {
				Plan struct {
					Quote struct {
						Known   string
						Unknown int
						Remote  bool
					}
				}
			}
			if err := json.Unmarshal(call(t, "GET", root, nil, 200), &view); err != nil {
				t.Fatal(err)
			}
			q := view.Plan.Quote
			if tc.alt == alternative && (!strings.HasPrefix(q.Known, "1.2") || q.Unknown != 0 || q.Remote) ||
				tc.alt == remote && (!strings.HasPrefix(q.Known, "0.4") || q.Unknown != 1 || !q.Remote) {
				t.Fatalf("quote did not include one possible specialist call: %+v", q)
			}
			run := idOf(t, call(t, "POST", root+"/runs", map[string]int{"expected": rev + 1, "authorizeCalls": 3}, 202))
			if _, err := db.Exec(ctx, "insert into invocations(run_id,ordinal,status) values($1,1,'submitted')", run); err == nil ||
				!strings.Contains(err.Error(), "text Agent choice requires an updated Worker") {
				t.Fatalf("legacy dispatch was not rejected: %v", err)
			}
			if err := worker.EnableStepToolChoices(ctx, run); err != nil {
				t.Fatal(err)
			}
			startModel, startRemote := modelCalls.Load(), remoteCalls.Load()
			advance := func(ordinal int) Result {
				t.Helper()
				outcome, err := worker.AdvanceTextStep(ctx, Step{run, ordinal})
				if err == nil && outcome.TextReply != nil {
					outcome, err = worker.CommitTextReply(ctx, *outcome.TextReply)
				}
				if err != nil || outcome.Stop {
					t.Fatalf("step %d: %+v %v", ordinal, outcome, err)
				}
				return outcome
			}
			if advance(1).Wait || advance(2).Wait {
				t.Fatal("selector or independent preparation stalled")
			}
			select {
			case observed := <-prompts:
				if strings.Contains(observed, source) || strings.Contains(observed, "private-note.txt") || strings.Contains(observed, upstream) ||
					!strings.Contains(observed, tc.name) || !strings.Contains(observed, "采购条款校对") {
					t.Fatalf("wrong scope sent for selection: %s", observed)
				}
			default:
				t.Fatal("choice prompt did not reach model")
			}
			if tc.manual {
				if !advance(3).Wait {
					t.Fatal("non-conforming decision did not wait")
				}
				var control int
				if err := db.QueryRow(ctx, "select control_revision from runs where id=$1", run).Scan(&control); err != nil {
					t.Fatal(err)
				}
				choice := map[string]int{"step": 3, "choice": 2, "expected": control}
				key := uuid.NewString()
				path := "/api/runs/" + run + "/tool-choice"
				first := call(t, "POST", path, choice, 202, key)
				again := call(t, "POST", path, choice, 202, key)
				if !bytes.Equal(first, again) {
					t.Fatal("manual selection replay changed the receipt")
				}
			}
			if tc.block {
				lookupTestSQL(t, ctx, db, "update agents set emergency=true where id=$1", remote)
				if !advance(3).Wait || modelCalls.Load()-startModel != 2 || remoteCalls.Load() != startRemote {
					t.Fatal("revoked selection dispatched or fell back to a different Agent")
				}
				lookupTestSQL(t, ctx, db, "update agents set emergency=false where id=$1", remote)
				return
			}
			if advance(3).Wait {
				t.Fatal("selected specialist stalled")
			}
			select {
			case input := <-selectedInputs:
				if !strings.Contains(input, source) || !strings.Contains(input, upstream) ||
					strings.Contains(input, "本次选用：") || strings.Contains(input, "不应被采用") {
					t.Fatalf("wrong input to selected candidate: %s", input)
				}
			default:
				t.Fatal("no request to selected specialist")
			}
			var candidate, used string
			var charges int
			if err := db.QueryRow(ctx, `select v.body,n.agent_id,(select count(*) from spend where run_id=$1)
                from artifacts a join artifact_versions v on(v.artifact_id,v.version)=(a.id,a.current_version)
                join effective_run_nodes n on(n.run_id,n.ordinal)=(a.run_id,a.ordinal)
                where (a.run_id,a.ordinal)=($1,3)`, run).Scan(&candidate, &used, &charges); err != nil || used != tc.alt || charges != 3 {
				t.Fatalf("wrong selected Agent or billing: %s %d %v", used, charges, err)
			}
			if tc.alt == remote && (candidate != remoteAnswer || remoteCalls.Load()-startRemote != 1 || modelCalls.Load()-startModel != 2) ||
				tc.alt != remote && (candidate != localAnswer || remoteCalls.Load() != startRemote || modelCalls.Load()-startModel != 3) {
				t.Fatalf("dispatch targeted the wrong backend: %q", candidate)
			}
		})
	}
}
