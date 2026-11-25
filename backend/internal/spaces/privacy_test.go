package spaces

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/monitor"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPersonalSpaceIsolatedFromSharedProjectsAndAdminPostgres(t *testing.T) {
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
	owner, admin, coworker, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := make(map[string]string)
	for id, role := range map[string]string{owner: "member", admin: "admin", coworker: "member"} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'测试用户','unused',$3)", id, id+"@example.test", role)
		tokens[id] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[id]), id)
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into enterprise(id,name,default_team) values(true,'Space privacy fixture',$1) on conflict do nothing", team)
	exec("insert into team_members(team_id,member_id) values($1,$2),($1,$3),($1,$4)", team, owner, admin, coworker)
	var ids struct{ personal, shared, commons string }
	for kind, out := range map[string]*string{"personal": &ids.personal, "team": &ids.shared, "commons": &ids.commons} {
		if err := db.QueryRow(ctx, `select id from spaces where kind=$1 and
			(owner_member_id=$2 or team_id=$3 or kind='commons')`, kind, owner, team).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	auth := identity.Server{DB: db}
	Server{DB: db}.Routes(mux, auth)
	projects.Server{DB: db}.Routes(mux, auth)
	knowledge.Server{DB: db, Objects: knowledge.Objects{Root: t.TempDir()}}.Routes(mux, auth)
	monitor.Server{DB: db}.Routes(mux, auth)
	do := func(actor, method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[actor]})
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, r)
		if response.Code != status {
			t.Fatalf("%s %s gave %d, expected %d: %s", method, path, response.Code, status, response.Body.String())
		}
		return response.Body.String()
	}
	getID := func(raw string) string {
		t.Helper()
		var data struct{ ID string }
		if err := json.Unmarshal([]byte(raw), &data); err != nil || data.ID == "" {
			t.Fatalf("missing id: %s", raw)
		}
		return data.ID
	}
	const goal = "Fixture personal conversation"
	const evidence = "Fixture private original body"
	private := getID(do(owner, "POST", "/api/projects", `{"goal":"`+goal+`"}`, 201))
	shared := getID(do(owner, "POST", "/api/projects", `{"goal":"Shared fixture","space":"`+ids.shared+`"}`, 201))
	do(coworker, "POST", "/api/projects", `{"goal":"Not allowed","space":"`+ids.personal+`"}`, 404)
	do(owner, "POST", "/api/projects", `{"goal":"Not a workspace","space":"`+ids.commons+`"}`, 404)
	if list := do(owner, "GET", "/api/projects?space="+ids.personal, "", 200); !strings.Contains(list, private) || strings.Contains(list, shared) {
		t.Fatalf("scope filter returned the wrong projects: %s", list)
	}
	if list := do(admin, "GET", "/api/projects?space="+ids.personal, "", 200); strings.Contains(list, private) {
		t.Fatal("another administrator could list private work")
	}
	privatePath := "/api/projects/" + private
	if body := do(owner, "GET", privatePath, "", 200); !strings.Contains(body, goal) || !strings.Contains(body, ids.personal) {
		t.Fatal("owner could not read the private project")
	}
	do(admin, "GET", privatePath, "", 404)
	source := getID(do(owner, "POST", privatePath+"/sources?name=fixture.txt", evidence, 201))
	sourcePath := privatePath + "/sources/" + source + "/content"
	if body := do(owner, "GET", sourcePath, "", 200); body != evidence {
		t.Fatal("owner could not read the original")
	}
	do(admin, "GET", sourcePath, "", 404)
	do(owner, "POST", privatePath+"/participants", `{"member":"`+coworker+`","role":"contributor","expected":1}`, 409)
	_, err = db.Exec(ctx, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", private, admin)
	var violation *pgconn.PgError
	if !errors.As(err, &violation) || violation.Code != "23514" {
		t.Fatalf("storage accepted a cross-owner private participant: %v", err)
	}
	_, err = db.Exec(ctx, "update projects set space_id=$2 where id=$1", private, ids.shared)
	if !errors.As(err, &violation) || violation.Code != "23514" {
		t.Fatalf("storage accepted a private-project move: %v", err)
	}
	do(owner, "POST", "/api/projects/"+shared+"/participants", `{"member":"`+coworker+`","role":"contributor","expected":1}`, 204)
	do(coworker, "GET", "/api/projects/"+shared, "", 200)
	exec("delete from team_members where team_id=$1 and member_id=$2", team, coworker)
	do(coworker, "GET", "/api/projects/"+shared, "", 404)

	run := uuid.NewString()
	exec("insert into plans(project_id,version,brief) values($1,0,$2)", private, goal)
	exec(`insert into runs(id,project_id,actor,charge_team,plan_version,status,request_key,request_hash,max_calls)
		values($1,$2,$3,$4,0,'queued',$1::uuid::text,digest('private','sha256'),1)`, run, private, owner, team)
	queue := do(admin, "GET", "/api/admin/monitor/queue?team="+team, "", 200)
	if strings.Contains(queue, goal) || strings.Contains(queue, private) || !strings.Contains(queue, run) {
		t.Fatalf("monitor did not preserve private-run metadata boundaries: %s", queue)
	}
	do(admin, "GET", "/api/admin/monitor/trace/"+run, "", 404)
	do(admin, "POST", "/api/project-recoveries/"+private, `{"email":"`+admin+`@example.test","reason":"Fixture","grantAccess":true,"hours":1,"expected":1}`, 404)
	var copies int
	if err := db.QueryRow(ctx, "select count(*) from spaces where kind='personal' and owner_member_id=$1", owner).Scan(&copies); err != nil || copies != 1 {
		t.Fatalf("owner got %d personal spaces: %v", copies, err)
	}
	exec("update members set enabled=false where id=$1", owner)
	do(owner, "GET", sourcePath, "", 401)
	exec("update members set enabled=true where id=$1", owner)
	do(owner, "GET", sourcePath, "", 200)
}
