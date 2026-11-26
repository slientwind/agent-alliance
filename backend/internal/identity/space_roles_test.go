package identity

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"alliance/internal/database"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTeamSpaceRoleChangesRequireAnActiveAdministrator(t *testing.T) {
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
	admin, member, outsider, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	tokens := map[string]string{}
	for id, role := range map[string]string{admin: "admin", member: "member", outsider: "member"} {
		exec(`insert into members(id,email,display_name,password_hash,role) values($1,$1::uuid::text||'@example.test','Fixture','unused',$2)`, id, role)
		tokens[id] = web.Token()
		exec(`insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')`, web.Hash(tokens[id]), id)
	}
	exec(`insert into teams(id,name) values($1,$1::uuid::text)`, team)
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, admin)
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, member)
	mux := http.NewServeMux()
	(Server{DB: db}).orgRoutes(mux)
	base := "/api/admin/teams/" + team + "/space-roles"
	call := func(actor, method, path, body string, expected int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "aa_session", Value: tokens[actor]})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != expected {
			t.Fatalf("%s %s got %d, expected %d: %s", method, path, w.Code, expected, w.Body.String())
		}
		return w.Body.String()
	}
	const promote = `{"previous":"member","role":"space_admin","reason":"轮值授权"}`
	const demote = `{"previous":"space_admin","role":"member","reason":"轮值结束"}`
	call(member, "GET", "", "", 403)
	call(admin, "PUT", "/"+outsider, promote, 404)
	call(admin, "PUT", "/"+admin, demote, 409)
	call(admin, "PUT", "/"+member, promote, 204)
	call(admin, "PUT", "/"+member, promote, 409)
	call(admin, "PUT", "/"+admin, demote, 204)
	call(admin, "PUT", "/"+member, demote, 409)
	roles := call(admin, "GET", "", "", 200)
	if !strings.Contains(roles, `"id":"`+member+`","name":"Fixture","role":"space_admin"`) || strings.Contains(roles, outsider) {
		t.Fatalf("unexpected team space roles: %s", roles)
	}
}
