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
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSpaceSettingsValidateBoundsAndVersion(t *testing.T) {
	input := settingsInput{Name: "  采购空间 ", Instructions: "  采用核定口径 \n", Expected: 2}
	if err := input.validate(); err != nil || input.Name != "采购空间" || input.Instructions != "采用核定口径" {
		t.Fatalf("valid settings rejected: %+v %v", input, err)
	}
	for _, input := range []settingsInput{
		{Name: " ", Expected: 1},
		{Name: "采购", Expected: 0},
		{Name: "采购", Description: strings.Repeat("文", 1001), Expected: 1},
		{Name: "采购", Instructions: "ignore\x00suffix", Expected: 1},
	} {
		var problem web.Problem
		if !errors.As(input.validate(), &problem) || problem.Status != http.StatusUnprocessableEntity {
			t.Errorf("invalid settings accepted: version %d", input.Expected)
		}
	}
}

func TestSpaceModelAndScopedRoutesPostgres(t *testing.T) {
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
	reject := func(code, sql string, args ...any) {
		t.Helper()
		_, err := db.Exec(ctx, sql, args...)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != code {
			t.Fatalf("expected constraint %s, got %v", code, err)
		}
	}

	owner, outsider, guest, team := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	var private, shared string
	tokens := make(map[string]string)
	for id, role := range map[string]string{owner: "member", outsider: "admin", guest: "member"} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','unused',$3)", id, id+"@example.test", role)
		tokens[id] = web.Token()
		exec("insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '5 minutes')", web.Hash(tokens[id]), id)
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	if err := db.QueryRow(ctx, "select id from spaces where kind='personal' and owner_member_id=$1", owner).Scan(&private); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, "select id from spaces where kind='team' and team_id=$1", team).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	exec("update spaces set name='私人工作',custom_instructions='PRIVATE-FIXTURE' where id=$1", private)
	exec("update spaces set name='采购团队',custom_instructions='TEAM-INTERNAL',default_template_id=$2 where id=$1", shared, uuid.NewString())
	exec("insert into space_members(space_id,member_id,role) values($1,$2,'space_admin'),($1,$3,'guest')", shared, owner, guest)
	reject("23514", "insert into spaces(kind,slug,name) values('personal',$1,'不合规')", uuid.NewString())
	reject("23505", "insert into spaces(kind,owner_member_id,slug,name) values('personal',$1,$2,'重复')", owner, uuid.NewString())
	reject("23505", "insert into spaces(kind,slug,name) values('commons',$1,'重复')", uuid.NewString())
	reject("23503", "insert into space_members(space_id,member_id,role) values($1,$2,'guest')", private, outsider)

	mux := http.NewServeMux()
	Server{DB: db}.Routes(mux, identity.Server{DB: db})
	do := func(actor, method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		if token := tokens[actor]; token != "" {
			r.AddCookie(&http.Cookie{Name: "aa_session", Value: token})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s status %d, want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w.Body.String()
	}
	const base = "/api/spaces"
	do("", "GET", base, "", 401)
	if body := do(owner, "GET", base, "", 200); !strings.Contains(body, "PRIVATE-FIXTURE") ||
		!strings.Contains(body, "TEAM-INTERNAL") || !strings.Contains(body, "全企公共底座") {
		t.Fatalf("owner scope incomplete: %s", body)
	}
	if body := do(outsider, "GET", base, "", 200); strings.Contains(body, private) || strings.Contains(body, shared) {
		t.Fatal("enterprise administrator saw an ungranted space")
	}
	do(outsider, "GET", base+"/"+private, "", 404)
	do(outsider, "GET", base+"/"+shared, "", 404)
	var item Space
	if body := do(guest, "GET", base+"/"+shared, "", 200); json.Unmarshal([]byte(body), &item) != nil ||
		item.Role != "guest" || item.Instructions != "" || item.DefaultTemplateID != nil {
		t.Fatalf("guest was given inappropriate space details: %s", body)
	}

	update := `{"name":"新采购空间","description":"采购组","customInstructions":"INSTRUCTION-NOT-IN-AUDIT","expectedVersion":1}`
	do(guest, "PUT", base+"/"+shared, update, 403)
	do(outsider, "PUT", base+"/"+private, update, 404)
	do(owner, "PUT", base+"/"+shared, update, 200)
	do(owner, "PUT", base+"/"+shared, update, 409)
	var logged string
	if err := db.QueryRow(ctx, "select detail::text from audit_events where action='space.configure' and subject=$1", shared).Scan(&logged); err != nil ||
		strings.Contains(logged, "INSTRUCTION-NOT-IN-AUDIT") {
		t.Fatalf("unexpected audit record %q %v", logged, err)
	}
	exec("delete from space_members where space_id=$1 and member_id=$2", shared, guest)
	do(guest, "GET", base+"/"+shared, "", 404)
	exec("update spaces set archived_at=now() where id=$1", private)
	do(owner, "GET", base+"/"+private, "", 404)
}
