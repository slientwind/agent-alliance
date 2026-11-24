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
	const goal, latest, prior, unrelated = "开场目标：核对交付时限", "最新要求：优先核对本周。", "先前采用源资料的答复", "另一个项目的独立消息"
	exec(`insert into messages(project_id,author,kind,body) values($1,$2,'user',$3),($4,$2,'user',$5)`, project, actor, goal, other, unrelated)
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
	for _, s := range []string{goal, latest, prior} {
		if !strings.Contains(prompt, s) {
			t.Fatalf("missing authorized context %q", s)
		}
	}
	if strings.Contains(prompt, unrelated) {
		t.Fatal("another project's message entered the prompt")
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
	action.Store("unselected")
	_, status = suggest("no-source", 2, nil)
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
}
