package planner

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPlanningConversationPostgres(t *testing.T) {
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
	vault, err := resources.NewVault(strings.Repeat("be", 32))
	if err != nil {
		t.Fatal(err)
	}
	type observed struct {
		body string
		err  error
	}
	prompts := make(chan observed, 8)
	var action atomic.Value
	action.Store("unexpected")
	source := uuid.NewString()
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Messages []resources.Message }
		err := json.NewDecoder(r.Body).Decode(&request)
		operation := action.Load().(string)
		if err == nil && operation == "revoke" {
			_, err = db.Exec(r.Context(), "update sources set revoked_at=now() where id=$1", source)
		}
		var prompt string
		for _, m := range request.Messages {
			if m.Role == "user" {
				prompt = m.Content
			}
		}
		prompts <- observed{body: prompt, err: err}
		kind := "question"
		if operation == "unselected" {
			kind = "answer"
		}
		answer := fmt.Sprintf(`{"kind":%q,"text":"规划答复-%s","agents":[],"missing":[]}`, kind, operation)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"finish_reason": "stop", "message": map[string]string{"content": answer},
		}}})
	}))
	defer provider.Close()
	ca := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVIDER_CA_FILE", ca)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	actor, team, project, other, model, parent := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	key, err := vault.Seal("fixture", "model/"+model+"/1")
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'Review','unused','member')`, actor, actor+"@example.invalid")
	exec(`insert into teams(id,name) values($1,$2)`, team, team)
	exec(`insert into team_members values($1,$2)`, team, actor)
	exec(`insert into model_resources(id,name,team_id,created_by) values($1,'Fixture',$2,$3)`, model, team, actor)
	exec(`insert into model_revisions(resource_id,revision,endpoint,model,encrypted_key,estimate_cny) values($1,1,$2,'fixture',$3,0.2)`, model, provider.URL, key)
	exec(`insert into projects(id,owner,final_reviewer,team_id,title) values($1,$2,$2,$3,'Main'),($4,$2,$2,$3,'Other')`, project, actor, team, other)
	exec(`insert into project_members(project_id,member_id) values($1,$2),($3,$2) on conflict do nothing`, project, actor, other)
	const goal, latest, prior, unrelated = "开场目标：核对交付时限", "最新要求：本周核对 PO-4721 和北湾合同续签。", "先前采用源资料的答复", "另一个项目的独立消息 PO-4721"
	const oldRecord, otherRecord = "旧交付地点是西门仓库，对应 PO-4721.", "北湾合同续签须经采购复核。"
	const outsideExcerpt = "这段文字离摘录位置很远"
	exec(`insert into messages(project_id,author,kind,body) values($1,$2,'user',$3),($4,$2,'user',$5)`, project, actor, goal, other, unrelated)
	var oldSeq int64
	if err := db.QueryRow(ctx, `insert into messages(project_id,author,kind,body) values($1,$2,'user',$3) returning seq`,
		project, actor, strings.Repeat("较早背景。", 140)+oldRecord+strings.Repeat("其他细节。", 100)+outsideExcerpt).Scan(&oldSeq); err != nil {
		t.Fatal(err)
	}
	exec(`insert into messages(project_id,author,kind,body) values($1,$2,'user',$3),($1,$2,'user',$4),($1,$2,'assistant',$5)`, project, actor,
		strings.Repeat("过早的背景。", 3500)+otherRecord, "PO-47210 的另一订单；PO-4721-ALT 是另一个编号。", "PO-4721 的模型旧答复不得作为相关历史")
	exec(`insert into messages(project_id,author,kind,body) values($1,$2,'user',$3)`, project, actor,
		strings.Repeat("序", 8192-len("PO-4721"))+"PO-47210 属于窗口边界测试。"+strings.Repeat("尾", 10000))
	exec(`insert into messages(project_id,author,kind,body) select $1,$2,'user',repeat('历史输入',1200) from generate_series(1,44)`, project, actor)
	hash := hex.EncodeToString(web.Hash(source))
	exec(`insert into objects(hash,size_bytes) values($1,6)`, hash)
	exec(`insert into sources(id,project_id,name,created_by) values($1,$2,'原资料.txt',$3)`, source, project, actor)
	exec(`insert into source_versions(source_id,version,object_hash,media_type,extracted,author,filename,extraction_state) values($1,1,$2,'text/plain','依据',$3,'原资料.txt','ready')`, source, hash, actor)
	pins := []inputPin{{ID: source, Version: 1}}
	rawPins, err := encodeInputs(pins)
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into planning_requests(id,project_id,actor,request_key,project_revision,resource_id,resource_revision,status,input_sources) values($1,$2,$3,'old',1,$4,1,'ready',$5)`, parent, project, actor, model, rawPins)
	exec(`insert into messages(project_id,author,kind,body,planning_request) values($1,$2,'assistant',$3,$4),($1,$2,'user',$5,null)`, project, actor, prior, parent, latest)
	server := Server{Catalog: resources.Catalog{DB: db, Vault: vault}}
	member := identity.Member{ID: actor}
	suggest := func(name string, revision int, inputs []inputPin) (id, status string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"expected": revision, "resource": model, "revision": 1, "authorizeOneCall": true, "sources": inputs})
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/projects/"+project+"/suggestions", bytes.NewReader(body)).WithContext(ctx)
		r.SetPathValue("id", project)
		r.Header.Set("Idempotency-Key", name)
		w := httptest.NewRecorder()
		if err := server.suggest(w, r, member); err != nil {
			t.Fatal(err)
		}
		var reply struct{ ID, Status string }
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != 200 {
			t.Fatalf("planner response: %s %v", w.Body, err)
		}
		return reply.ID, reply.Status
	}
	readPrompt := func() string {
		t.Helper()
		select {
		case prompt := <-prompts:
			if prompt.err != nil {
				t.Fatal(prompt.err)
			}
			return prompt.body
		default:
			t.Fatal("the planner did not call the provider")
			return ""
		}
	}
	action.Store("pinned")
	first, status := suggest("pinned", 1, pins)
	if status != "ready" {
		t.Fatalf("initial planner status: %q", status)
	}
	prompt := readPrompt()
	for _, s := range []string{goal, latest, prior, oldRecord, otherRecord} {
		if !strings.Contains(prompt, s) {
			t.Fatalf("missing authorized context %q", s)
		}
	}
	for _, excluded := range []string{unrelated, outsideExcerpt, "另一订单", "另一个编号", "窗口边界测试", "模型旧答复不得作为", strings.Repeat("过早的背景。", 100)} {
		if strings.Contains(prompt, excluded) {
			t.Fatalf("unrelated history entered the prompt: %q", excluded)
		}
	}
	var compacted, includesParent, reachedLatest bool
	err = db.QueryRow(ctx, `select q.context_compacted,
 exists(select from planning_context_requests(q.id) x(id) where x.id=$2),
 exists(select from planning_context_messages c join messages m on m.seq=c.message_seq
   where c.request_id=q.id and m.seq=q.context_through and c.body_bytes=octet_length(m.body) and m.body=$3)
 from planning_requests q where q.id=$1`, first, parent, latest).Scan(&compacted, &includesParent, &reachedLatest)
	if err != nil || !compacted || !includesParent || !reachedLatest {
		t.Fatalf("missing recorded context coverage: %v", err)
	}
	var sourceStart int
	var sourceExcerpt string
	err = db.QueryRow(ctx, `select c.body_start,convert_from(substring(convert_to(m.body,'UTF8')
         from c.body_start+1 for c.body_bytes),'UTF8')
         from planning_context_messages c join messages m on m.seq=c.message_seq
         where c.request_id=$1 and c.message_seq=$2 and c.recalled`, first, oldSeq).Scan(&sourceStart, &sourceExcerpt)
	if err != nil || sourceStart < 1 || !strings.Contains(sourceExcerpt, oldRecord) || strings.Contains(sourceExcerpt, outsideExcerpt) {
		t.Fatalf("incorrect recalled span: source=%d excerpt=%q err=%v", sourceStart, sourceExcerpt, err)
	}
	action.Store("unselected")
	independent, status := suggest("no-source", 2, nil)
	if p := readPrompt(); status != "ready" || strings.Contains(p, prior) || strings.Contains(p, "规划答复-pinned") {
		t.Fatalf("an unselected source influenced a later planning prompt")
	}
	action.Store("revoke")
	_, status = suggest("revoke", 3, pins)
	if p := readPrompt(); status != "stale" || !strings.Contains(p, prior) {
		t.Fatal("a source revoked during planning should invalidate the response")
	}
	action.Store("unexpected")
	replayed, status := suggest("pinned", 1, pins)
	if replayed != first || status != "stale" || len(prompts) != 0 {
		t.Fatalf("replay returned invalidated context or dispatched again: %q %q", replayed, status)
	}
	token := uuid.NewString()
	exec(`insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '1 minute')`, web.Hash(token), actor)
	routes := http.NewServeMux()
	projects.Server{DB: db}.Routes(routes, identity.Server{DB: db})
	r := httptest.NewRequest("GET", "/api/projects/"+project, nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), prior) || strings.Contains(w.Body.String(), "规划答复-pinned") || !strings.Contains(w.Body.String(), "规划答复-unselected") {
		t.Fatalf("project snapshot exposed revoked planning history or lost the independent answer (HTTP %d)", w.Code)
	}
	exec("update messages set body=replace(body,$2,'调整了未采用的部分') where seq=$1", oldSeq, outsideExcerpt)
	var state string
	if err := db.QueryRow(ctx, "select current_status from current_planning_requests where id=$1", independent).Scan(&state); err != nil || state != "ready" {
		t.Fatalf("outside the recalled span: status=%s err=%v", state, err)
	}
	exec("update messages set body=replace(body,$2,'PO-4721 的交付地点已更正为南门。') where seq=$1", oldSeq, oldRecord)
	replayed, status = suggest("no-source", 2, nil)
	if replayed != independent || status != "stale" || len(prompts) != 0 {
		t.Fatalf("changed excerpt was not invalidated on replay: %s %s", replayed, status)
	}
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), "规划答复-unselected") || strings.Contains(w.Body.String(), oldRecord) {
		t.Fatal("changed excerpt or its derived answer remained visible")
	}
}
