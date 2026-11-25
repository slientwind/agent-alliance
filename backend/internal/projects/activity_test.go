package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConversationActivityFiltersDetailsAndFollowsRevocation(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("requires a scratch PostgreSQL database with pgvector")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "alliance_lookup_test") {
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
	owner, reader, team, project, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, member := range []string{owner, reader} {
		exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'Fixture','','member')", member, member+"@example.test")
	}
	exec("insert into teams(id,name) values($1,$2)", team, team)
	exec("insert into projects(id,owner,team_id,final_reviewer,title,plan_version) values($1,$2,$3,$2,'Fixture',1)", project, owner, team)
	exec("insert into project_memberships(project_id,member_id,can_review) values($1,$2,true),($1,$3,false)", project, owner, reader)
	exec("insert into plans(project_id,version,brief,confirmed_by) values($1,1,'private goal',$2)", project, owner)
	exec("insert into runs(id,project_id,plan_version,actor,status,charge_team,request_key,request_hash,max_calls) values($1,$2,1,$3,'running',$4,'fixture',$5,1)", run, project, owner, team, web.Hash("fixture"))
	exec("insert into general_agent_runs(run_id,tier) values($1,'default')", run)
	exec("insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools',$2)", run, []byte("hidden reasoning"))
	exec("insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint,state,observation_cipher) values($1,1,1,'read_sources','private-id',$2,'fixture','completed',$3)",
		run, []byte("private arguments"), []byte("private output"))
	request := httptest.NewRequestWithContext(ctx, "GET", "/activity", nil)
	request.SetPathValue("id", project)
	request.SetPathValue("run", run)
	w := httptest.NewRecorder()
	server := Server{DB: db}
	if err := server.activity(w, request, identity.Member{ID: reader}); err != nil {
		t.Fatal(err)
	}
	out := w.Body.String()
	if !strings.Contains(out, `"category":"sources"`) || !strings.Contains(out, `"category":"thinking"`) ||
		strings.Contains(out, "private") || strings.Contains(out, "hidden") || strings.Contains(out, "read_sources") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected activity: %s", out)
	}
	var initial, updated int64
	if err := db.QueryRow(ctx, "select seq from project_session_heads where project_id=$1", project).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	exec("update general_agent_actions set state='rejected' where run_id=$1", run)
	if err := db.QueryRow(ctx, "select seq from project_session_heads where project_id=$1", project).Scan(&updated); err != nil || updated != initial+1 {
		t.Fatalf("action state did not wake the session: %d -> %d, %v", initial, updated, err)
	}
	exec("delete from project_memberships where project_id=$1 and member_id=$2", project, reader)
	if err := server.activity(httptest.NewRecorder(), request, identity.Member{ID: reader}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked access returned %v", err)
	}
}
