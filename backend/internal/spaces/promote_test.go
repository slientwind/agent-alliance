package spaces

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/studio"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTeamPromotionCopiesOnlyTheSelectedEditionAndConfiguration(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database")
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	owner, teammate, guest, admin, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := make(map[string]string)
	for id, role := range map[string]string{owner: "member", teammate: "member", guest: "member", admin: "admin"} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','unused',$3)", id, id+"@example.test", role)
		tokens[id] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[id]), id)
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3)", team, owner, teammate)
	var personal, target string
	if err := db.QueryRow(ctx, "select id from spaces where kind='personal' and owner_member_id=$1", owner).Scan(&personal); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "select id from spaces where kind='team' and team_id=$1", team).Scan(&target); err != nil {
		t.Fatal(err)
	}
	exec("insert into space_members(space_id,member_id,role) values($1,$2,'guest')", target, guest)

	project, run, artifact, draft, release, source := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`insert into projects(id,owner,team_id,final_reviewer,title,space_id,plan_version)
	 values($1,$2,$3,$2,'hidden title',$4,1)`, project, owner, team, personal)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, owner)
	exec("insert into messages(project_id,author,kind,body) values($1,$2,'user','private-chat-marker')", project, owner)
	exec("insert into plans(project_id,version,brief) values($1,1,'hidden planning input')", project)
	exec(`insert into runs(id,project_id,actor,charge_team,plan_version,status,request_key,request_hash,max_calls)
	 values($1,$2,$3,$4,1,'completed',$1::uuid::text,digest('private','sha256'),1)`, run, project, owner, team)
	exec("insert into artifacts(id,project_id,run_id,ordinal,name,current_version) values($1,$2,$3,1,'采购核对',2)", artifact, project, run)
	exec(`insert into artifact_versions(artifact_id,version,body,reason) values
	 ($1,1,'selected edition','private feedback'),($1,2,'unselected newer edition','later feedback')`, artifact)
	objects := knowledge.Objects{Root: t.TempDir()}
	const fileBytes = "selected document file"
	if err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
		hash, _, err := objects.Put(ctx, tx, strings.NewReader(fileBytes), 4096)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into artifact_files values($1,1,$2,'selected.txt','text/plain')", artifact, hash)
		if err != nil {
			return err
		}
		sourceID, err := objects.NewTextSource(ctx, tx, project, owner, "source.txt", "private source")
		if err != nil {
			return err
		}
		source = sourceID
		_, err = tx.Exec(ctx, "insert into plan_sources(project_id,plan_version,source_id,source_version) values($1,1,$2,1)", project, source)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	spec := studio.Spec{Name: "采购核对助手", Instructions: "Read the submitted amounts", Tier: "default"}
	exec("insert into studio_drafts(id,owner,team_id,spec) values($1,$2,$3,$4)", draft, owner, team, spec)
	exec(`insert into studio_releases(id,draft_id,revision,owner,team_id,agent_scope,state,spec,dependencies,digest)
	  values($1,$2,1,$3,$4,'personal','published',$5,'{"credential":"private-binding-marker"}',repeat('a',64))`, release, draft, owner, team, spec)
	exec(`insert into studio_turns(draft_id,request_key,request_hash,expected,input_cipher,state,response)
		values($1,'trial',digest('fixture','sha256'),1,decode('aabb','hex'),'completed','{"text":"private-studio-marker"}')`, draft)

	mux := http.NewServeMux()
	auth := identity.Server{DB: db}
	Server{DB: db, Objects: objects}.Routes(mux, auth)
	studio.Server{DB: db}.Routes(mux, auth)
	do := func(actor, method, path, key, body string, status int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[actor]})
		r.Header.Set("Idempotency-Key", key)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, r)
		if response.Code != status {
			t.Fatalf("%s %s gave %d, expected %d: %s", method, path, response.Code, status, response.Body.String())
		}
		return response.Body.String()
	}
	idFrom := func(raw string) string {
		t.Helper()
		var result struct{ ID string }
		if err := json.Unmarshal([]byte(raw), &result); err != nil || result.ID == "" {
			t.Fatalf("missing publication receipt: %s", raw)
		}
		return result.ID
	}
	promotePath := "/api/spaces/" + personal + "/promote"
	payload := `{"destination":"` + target + `","kind":"artifact","item":"` + artifact + `","version":1}`
	do(teammate, "POST", promotePath, "other-publisher", payload, 404)
	do(owner, "POST", promotePath, "", payload, 422)
	asset := idFrom(do(owner, "POST", promotePath, "artifact-1", payload, 201))
	if id := idFrom(do(owner, "POST", promotePath, "artifact-1", payload, 200)); id != asset {
		t.Fatal("retry created a duplicate publication")
	}
	do(owner, "POST", promotePath, "artifact-1", strings.Replace(payload, `"version":1`, `"version":2`, 1), 409)
	path := "/api/spaces/" + target + "/publications/" + asset
	body := do(teammate, "GET", path, "", "", 200)
	for _, excluded := range []string{project, artifact, "unselected newer edition", "private feedback", "private-chat-marker"} {
		if strings.Contains(body, excluded) {
			t.Fatalf("publication included private origin material: %s", excluded)
		}
	}
	if !strings.Contains(body, "selected edition") || do(teammate, "GET", path+"/file", "", "", 200) != fileBytes {
		t.Fatal("selected text or exact file not copied")
	}
	do(admin, "GET", path+"/file", "", "", 404)
	do(guest, "GET", path, "", "", 404)
	// A private or removed knowledge pin makes both new and existing team snapshots unavailable.
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	exec("insert into general_kb_evidence(run_id,turn,document_id,document_version,chunk_ordinal) values($1,1,gen_random_uuid(),1,1)", run)
	do(teammate, "GET", path, "", "", 404)
	do(teammate, "GET", path+"/file", "", "", 404)
	do(owner, "POST", promotePath, "with-private-knowledge", payload, 404)
	exec("delete from general_kb_evidence where run_id=$1", run)

	payload = `{"destination":"` + target + `","kind":"agent","item":"` + release + `","version":1}`
	agent := idFrom(do(owner, "POST", promotePath, "agent-1", payload, 201))
	agentPath := "/api/spaces/" + target + "/publications/" + agent
	body = do(teammate, "GET", agentPath, "", "", 200)
	if !strings.Contains(body, spec.Instructions) || strings.Contains(body, draft) || strings.Contains(body, "private-binding-marker") {
		t.Fatalf("unexpected agent configuration in publication: %s", body)
	}
	copy := idFrom(do(teammate, "POST", agentPath+"/draft", "", "", 201))
	if same := idFrom(do(teammate, "POST", agentPath+"/draft", "", "", 200)); same != copy {
		t.Fatal("adoption allocated more than one studio draft")
	}
	body = do(teammate, "GET", "/api/studio/"+copy, "", "", 200)
	if !strings.Contains(body, spec.Instructions) || strings.Contains(body, "private-studio-marker") {
		t.Fatalf("draft carried another Studio history: %s", body)
	}
	exec("update sources set revoked_at=now() where id=$1", source)
	do(teammate, "GET", path, "", "", 404)
	do(teammate, "GET", path+"/file", "", "", 404)
	do(owner, "POST", promotePath, "withdrawn-input", strings.Replace(payload,
		`"kind":"agent","item":"`+release+`"`, `"kind":"artifact","item":"`+artifact+`"`, 1), 404)

	var remaining string
	if err := db.QueryRow(context.Background(), "select body from messages where project_id=$1", project).Scan(&remaining); err != nil ||
		remaining != "private-chat-marker" {
		t.Fatalf("promoting a result altered the private conversation: %s %v", remaining, err)
	}
}
