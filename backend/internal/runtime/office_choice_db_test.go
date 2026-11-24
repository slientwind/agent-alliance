package runtime

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/services"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOfficeChoicePostgres(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to an isolated PostgreSQL database with pgvector")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch PostgreSQL database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
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

	const sourceText = "仅供经授权制作步骤读取的原件正文"
	const authored = "已核对两份报价。甲方报价有效期为周五。"
	prompts := make(chan string, 16)
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct{ Messages []resources.Message }
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Messages) < 2 {
			http.Error(w, "invalid messages", http.StatusBadRequest)
			return
		}
		answer, finish := authored, "stop"
		if strings.Contains(input.Messages[0].Content, "制作同一格式") {
			prompts <- input.Messages[1].Content
			answer = `{"choice":2}`
			if strings.Contains(input.Messages[1].Content, "截断测试") {
				finish = "length"
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": answer}, "finish_reason": finish}},
			"usage":   map[string]int{"total_tokens": 18},
		})
	}))
	defer provider.Close()
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", cert)
	t.Setenv("OBJECT_ROOT", t.TempDir())
	file := resources.File{Name: "deliverable.docx", Mime: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Data: packageForExecution(t, map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<document/>"})}
	type toolRequest struct {
		Text, Operation string
		Config          map[string]string
		Sources         []knowledge.InputSource
	}
	requests := make(chan toolRequest, 16)
	connector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/extract/docx" && r.Header.Get("Authorization") == "Bearer test" {
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "ready",
				"segments": []map[string]string{{"location": "第 1 段", "text": sourceText}}})
			return
		}
		var input toolRequest
		if r.URL.Path != "/invoke" || r.Header.Get("Authorization") != "Bearer test" || json.NewDecoder(r.Body).Decode(&input) != nil {
			http.Error(w, "invalid connector request", http.StatusBadRequest)
			return
		}
		requests <- input
		_ = json.NewEncoder(w).Encode(map[string]any{"text": input.Text, "file": file})
	}))
	defer connector.Close()
	t.Setenv("CONNECTOR_URL", connector.URL)
	t.Setenv("PREVIEW_URL", connector.URL)
	t.Setenv("CONNECTOR_TOKEN", "test")

	worker := Activities{Catalog: resources.Catalog{DB: db, Vault: vault}}
	auth := identity.Server{DB: db}
	routes := http.NewServeMux()
	projects.Server{DB: db}.Routes(routes, auth)
	knowledge.Server{DB: db, Objects: knowledge.ConfiguredObjects()}.Routes(routes, auth)
	services.Server{DB: db}.Routes(routes, auth)
	step, _ := seedLookupTurn(t, ctx, worker, provider.URL)
	var owner, team, selector string
	if err := db.QueryRow(ctx, `select r.actor,r.charge_team,n.agent_id from runs r join nodes n
             on (n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,1) where r.id=$1`, step.Run).Scan(&owner, &team, &selector); err != nil {
		t.Fatal(err)
	}
	lookupTestSQL(t, ctx, db, "update members set role='admin' where id=$1", owner)
	lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", selector)
	tool := func(name, operation string) string {
		t.Helper()
		id := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into agents(id,owner,team_id) values($1,$2,$3)", id, owner, team)
		lookupTestSQL(t, ctx, db, `insert into agent_revisions(agent_id,revision,name,instructions,created_by,tool)
            values($1,1,$2,'制作文件',$3,jsonb_build_object('operation',$4::text,'config',jsonb_build_object('title',$2::text)))`, id, name, owner, operation)
		lookupTestSQL(t, ctx, db, "insert into validations(agent_id,revision,actor,status) values($1,1,$2,'passed')", id, owner)
		lookupTestSQL(t, ctx, db, "update agents set published_revision=1 where id=$1", id)
		return id
	}
	mainTool, alternative, different := tool("常规报价简报", "docx"), tool("供应商复核简报", "docx"), tool("复核工作簿", "xlsx")
	token := web.Token()
	lookupTestSQL(t, ctx, db, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '3 minutes')", web.Hash(token), owner)
	call := func(method, path string, body []byte, key string, status int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/octet-stream")
		res := httptest.NewRecorder()
		routes.ServeHTTP(res, req)
		if res.Code != status {
			t.Fatalf("%s %s: status=%d, want=%d, body=%s", method, path, res.Code, status, res.Body)
		}
		return res.Body.Bytes()
	}
	post := func(path string, input any, key string, status int) []byte {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		return call("POST", path, raw, key, status)
	}
	start := func(run string, ordinal int, wait bool) {
		t.Helper()
		result, err := worker.ExecuteStep(ctx, Step{Run: run, Ordinal: ordinal})
		if err != nil || result.Wait != wait || result.Stop {
			t.Fatalf("step %d: %+v, %v", ordinal, result, err)
		}
	}
	outcome := func(run string, picked, calls int) (control int) {
		t.Helper()
		var actualChoice, actualCalls int
		err := db.QueryRow(ctx, `select coalesce(c.manual_choice,c.choice),r.control_revision,
             (select count(*) from spend where run_id=r.id)
             from runs r join run_tool_choices c on c.run_id=r.id where r.id=$1`, run).Scan(&actualChoice, &control, &actualCalls)
		if err != nil || actualChoice != picked || actualCalls != calls {
			t.Fatalf("choice=%d/%d, calls=%d/%d err=%v", actualChoice, picked, actualCalls, calls, err)
		}
		return control
	}
	readID := func(raw []byte) string {
		t.Helper()
		var id struct{ ID string }
		if json.Unmarshal(raw, &id) != nil || id.ID == "" {
			t.Fatalf("missing id in %s", raw)
		}
		return id.ID
	}

	for _, mode := range []string{"自动选择", "截断测试", "停用后核对"} {
		t.Log(mode)
		project := uuid.NewString()
		lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$2,$3,$4)", project, owner, team, mode)
		lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", project, owner)
		lookupTestSQL(t, ctx, db, "insert into project_budgets(project_id) values($1) on conflict do nothing", project)
		path := "/api/projects/" + project
		source := readID(call("POST", path+"/sources?name=quote.txt", []byte(sourceText), "", 201))
		var view struct{ Revision int }
		if err := json.Unmarshal(call("GET", path, nil, "", 200), &view); err != nil {
			t.Fatal(err)
		}
		plan := projects.PlanInput{Expected: view.Revision, Brief: mode, Agents: []string{selector, selector, mainTool},
			AgentRevisions: []int{1, 1, 1}, Dependencies: [][]int{{}, {}, {1, 2}}, Sources: []projects.SourcePin{{ID: source, Version: 1}},
			ToolChoices: []projects.ToolChoicePin{{Step: 3, Selector: 1, Alternative: different, Revision: 1}},
		}
		post(path+"/plan", plan, "", 422)
		plan.ToolChoices[0].Alternative = alternative
		post(path+"/plan", plan, "", 204)
		run := readID(post(path+"/runs", map[string]int{"expected": view.Revision + 1, "authorizeCalls": 3}, uuid.NewString(), 202))
		prepared, err := worker.AdvanceTextStep(ctx, Step{Run: run, Ordinal: 1})
		if err != nil || prepared.TextReply == nil || prepared.Wait || prepared.Stop {
			t.Fatalf("choice reply was not recorded: %+v %v", prepared, err)
		}
		var decided bool
		if err := db.QueryRow(ctx, "select exists(select from run_tool_choices where run_id=$1)", run).Scan(&decided); err != nil || decided {
			t.Fatalf("choice applied before reply adoption: %v %v", decided, err)
		}
		if applied, err := worker.CommitTextReply(ctx, *prepared.TextReply); err != nil || applied.Wait || applied.Stop {
			t.Fatalf("choice reply was not adopted: %+v %v", applied, err)
		}
		select {
		case prompt := <-prompts:
			if !strings.Contains(prompt, "quote.txt") || !strings.Contains(prompt, "供应商复核简报") || strings.Contains(prompt, sourceText) {
				t.Fatalf("routing input exposed the wrong content: %s", prompt)
			}
		default:
			t.Fatal("selector was not invoked")
		}
		start(run, 2, false)
		if mode == "停用后核对" {
			lookupTestSQL(t, ctx, db, "update agents set emergency=true where id=$1", alternative)
			start(run, 3, true)
			outcome(run, 2, 2)
			lookupTestSQL(t, ctx, db, "update agents set emergency=false where id=$1", alternative)
			continue
		}
		if mode == "截断测试" {
			control := outcome(run, 0, 2)
			input := map[string]int{"step": 3, "choice": 2, "expected": control}
			path := "/api/runs/" + run + "/tool-choice"
			lookupTestSQL(t, ctx, db, "update runs set status='blocked' where id=$1", run)
			post(path, input, "unrelated-block", 409)
			lookupTestSQL(t, ctx, db, "update runs set status='running' where id=$1", run)
			start(run, 3, true)
			first := post(path, input, "manual-choose", 202)
			replay := post(path, input, "manual-choose", 202)
			if !bytes.Equal(first, replay) {
				t.Fatalf("manual replay diverged: %s / %s", first, replay)
			}
		}
		start(run, 3, false)
		retry, err := worker.ExecuteStep(ctx, Step{Run: run, Ordinal: 3})
		if err != nil || !retry.Stop || retry.Wait {
			t.Fatalf("completed run replay: %+v %v", retry, err)
		}
		outcome(run, 2, 3)
		select {
		case input := <-requests:
			if input.Operation != "docx" || input.Config["title"] != "供应商复核简报" || input.Text != authored ||
				len(input.Sources) != 1 || input.Sources[0].Text != sourceText {
				t.Fatalf("wrong selected tool or body: %+v", input)
			}
		default:
			t.Fatal("no production call recorded")
		}
		var lineage, fileStored bool
		err = db.QueryRow(ctx, `select exists(select from run_working_parents where run_id=$1 and ordinal=3 and parent_ordinal=1),
                 exists(select from artifacts a join artifact_versions v on v.artifact_id=a.id
                    join artifact_files f on (f.artifact_id,f.version)=(v.artifact_id,v.version)
                    where a.run_id=$1 and a.ordinal=3 and v.body=$2 and f.filename='deliverable.docx')`, run, authored).Scan(&lineage, &fileStored)
		if err != nil || !lineage || !fileStored {
			t.Fatalf("missing ancestor or original: lineage=%v file=%v err=%v", lineage, fileStored, err)
		}
		if mode == "自动选择" {
			service := readID(post("/api/services", map[string]string{"name": "复核简报服务", "project": project, "instructions": "依授权制作复核简报"}, "", 201))
			entry := call("GET", "/api/services/"+service+"/entry", nil, "", 200)
			if !bytes.Contains(entry, []byte(`"allowDocumentAttachments":true`)) || !bytes.Contains(entry, []byte("供应商复核简报")) {
				t.Fatalf("service lost document capability or alternative: %s", entry)
			}
			launch := post("/api/services/"+service+"/chat", map[string]any{
				"version": 1, "input": "制作复核简报", "authorizeCalls": 3,
				"attachment": services.Attachment{Name: "quote.docx", Data: file.Data},
			}, uuid.NewString(), 202)
			serviceRun := readID(launch)
			for ordinal := 1; ordinal <= 3; ordinal++ {
				start(serviceRun, ordinal, false)
			}
			outcome(serviceRun, 2, 3)
			<-prompts
			input := <-requests
			if input.Text != authored || input.Operation != "docx" || input.Config["title"] != "供应商复核简报" ||
				len(input.Sources) != 1 || !strings.Contains(input.Sources[0].Text, sourceText) {
				t.Fatalf("service did not retain routing/body: %+v", input)
			}
		}
	}
	if len(requests) != 0 {
		t.Fatal("replay or stopped revision dispatched an extra tool call")
	}

	const parallelGoal = "并行工具选择截断测试"
	parallel := uuid.NewString()
	lookupTestSQL(t, ctx, db, "insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$2,$3,$4)", parallel, owner, team, parallelGoal)
	lookupTestSQL(t, ctx, db, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true) on conflict do nothing", parallel, owner)
	parallelPath := "/api/projects/" + parallel
	post(parallelPath+"/plan", projects.PlanInput{Expected: 1, Brief: parallelGoal,
		Agents: []string{selector, mainTool, selector, mainTool, selector, selector}, AgentRevisions: []int{1, 1, 1, 1, 1, 1},
		Dependencies: [][]int{{}, {1}, {}, {3}, {}, {2, 4, 5}},
		ToolChoices: []projects.ToolChoicePin{
			{Step: 2, Selector: 1, Alternative: alternative, Revision: 1},
			{Step: 4, Selector: 3, Alternative: alternative, Revision: 1},
		},
	}, "", 204)
	run := readID(post(parallelPath+"/runs", map[string]int{"expected": 2, "authorizeCalls": 6}, uuid.NewString(), 202))
	if nodes, err := worker.Topology(ctx, run); err != nil || len(nodes) != 6 || !hasParallelSteps(nodes) ||
		!nodes[1].ToolChoice || !nodes[3].ToolChoice || nodes[4].ToolChoice {
		t.Fatalf("workflow topology did not identify selectable steps: %+v %v", nodes, err)
	}
	if err := worker.EnableStepToolChoices(ctx, run); err != nil {
		t.Fatal(err)
	}
	start(run, 1, false)
	start(run, 2, true)
	start(run, 2, true)
	start(run, 3, false)
	start(run, 4, true)
	start(run, 5, false)
	readRun := func(state string, waiting []int, control, totalCalls int) {
		t.Helper()
		var data struct {
			Run  struct{ Status string }
			Plan struct {
				Nodes []struct {
					ToolChoice *struct {
						CanConfirm bool
						WaitScope  string
					}
				}
			}
		}
		body := call("GET", parallelPath, nil, "", 200)
		if err := json.Unmarshal(body, &data); err != nil || data.Run.Status != state {
			t.Fatalf("incorrect project status: %v %s", err, body)
		}
		var open []int
		for i, n := range data.Plan.Nodes {
			if n.ToolChoice != nil && n.ToolChoice.CanConfirm {
				if n.ToolChoice.WaitScope != "step" {
					t.Fatalf("step %d has scope %s", i+1, n.ToolChoice.WaitScope)
				}
				open = append(open, i+1)
			}
		}
		var actualControl, actualCalls int
		err := db.QueryRow(ctx, `select control_revision,(select count(*) from spend where run_id=r.id)
          from runs r where r.id=$1`, run).Scan(&actualControl, &actualCalls)
		if !slices.Equal(open, waiting) || err != nil || actualControl != control || actualCalls != totalCalls {
			t.Fatalf("waits=%v/%v, control=%d/%d, calls=%d/%d, err=%v", open, waiting, actualControl, control, actualCalls, totalCalls, err)
		}
	}
	readRun("running", []int{2, 4}, 1, 3)
	var waitTraces, independentCandidate int
	err = db.QueryRow(ctx, `select
        (select count(*) from trace_events where run_id=$1 and code='tool_step_choice_waiting'),
        (select count(*) from artifacts where run_id=$1 and ordinal=5)`, run).Scan(&waitTraces, &independentCandidate)
	if err != nil || waitTraces != 2 || independentCandidate != 1 {
		t.Fatalf("wait delivery/retry: traces=%d independent=%d err=%v", waitTraces, independentCandidate, err)
	}

	runPath := "/api/runs/" + run
	choose := func(ordinal, choice, expected int, key string, status int) []byte {
		t.Helper()
		return post(runPath+"/tool-choice", map[string]int{"step": ordinal, "choice": choice, "expected": expected}, key, status)
	}
	post(runPath+"/control", map[string]any{"action": "pause", "expected": 1, "reason": "核对全局暂停"}, "pause-choice", 202)
	readRun("paused", nil, 2, 3)
	choose(2, 1, 2, "during-pause", 409)
	post(runPath+"/control", map[string]any{"action": "resume", "expected": 2, "reason": "核对全局恢复"}, "resume-choice", 202)
	readRun("running", []int{2, 4}, 3, 3)
	choose(2, 1, 1, "old-card", 409)
	first := choose(2, 1, 3, "choose-a", 202)
	if replay := choose(2, 1, 3, "choose-a", 202); !bytes.Equal(first, replay) {
		t.Fatalf("step confirmation changed on replay: %s / %s", first, replay)
	}
	choose(4, 2, 3, "choose-a", 409)
	readRun("running", []int{4}, 3, 3)
	start(run, 2, false)
	start(run, 4, true)
	readRun("running", []int{4}, 3, 4)
	choose(4, 2, 3, "choose-b", 202)
	var scopedWakes []int
	err = db.QueryRow(ctx, `select array_agg(wake_ordinal order by wake_ordinal) from controls
        where run_id=$1 and revision=3 and action='tool.step.choice' and sent_at is null`, run).Scan(&scopedWakes)
	if err != nil || !slices.Equal(scopedWakes, []int{2, 4}) {
		t.Fatalf("confirmation outbox: %v %v", scopedWakes, err)
	}
	start(run, 4, false)
	start(run, 6, false)
	readRun("completed", nil, 3, 6)
	if len(requests) != 2 {
		t.Fatalf("expected two connector calls, got %d", len(requests))
	}
}
