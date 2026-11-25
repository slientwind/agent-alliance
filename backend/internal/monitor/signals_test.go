package monitor

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQualitySignalRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"runId":"missing","kind":"reply_copied"}`, 422},
		{`{"runId":"00000000-0000-0000-0000-000000000000","kind":"reply_copied"}`, 422},
		{`{"runId":"244e7a33-b16d-4b44-9aa2-dccf8c08b8fb","kind":"review_approved"}`, 422},
		{`{"runId":"244e7a33-b16d-4b44-9aa2-dccf8c08b8fb","kind":"reply_copied","actor":"other"}`, 422},
		{`{"kind":"` + strings.Repeat("x", 4096) + `"}`, 413},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/telemetry/signals", strings.NewReader(tc.body))
		err := (Server{}).qualitySignal(httptest.NewRecorder(), r, identity.Member{})
		var problem web.Problem
		if !errors.As(err, &problem) || problem.Status != tc.code {
			t.Errorf("got %v, want status %d", err, tc.code)
		}
	}
}

func TestQualitySignalRequiresCurrentMembershipAndIsIdempotent(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := database.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	team, actor, collaborator, project, run := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into teams(id,name) values($1,$2)`, team, "quality test-"+team)
	for _, member := range []string{actor, collaborator} {
		exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'quality test','','member')`, member, member+"@example.test")
	}
	exec(`insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'quality test')`, project, actor, team)
	exec(`insert into plans(project_id,version,brief) values($1,1,'quality test')`, project)
	exec(`insert into runs(id,project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls)
	 values($1,$2,1,$3,$4,'quality test','\x00',1)`, run, project, actor, team)
	exec(`insert into project_members(project_id,member_id) values($1,$2)`, project, actor)

	check := func(member, kind string, want error) {
		t.Helper()
		if err := recordQualitySignal(ctx, tx, qualitySignalInput{RunID: run, Kind: kind}, member); !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", kind, err, want)
		}
	}
	check(collaborator, "reply_copied", pgx.ErrNoRows)
	exec(`insert into project_members(project_id,member_id) values($1,$2)`, project, collaborator)
	check(actor, "user_cancelled_midway", pgx.ErrNoRows)
	check(actor, "reply_copied", nil)
	check(collaborator, "artifact_downloaded", nil)
	check(collaborator, "artifact_downloaded", nil)
	exec(`delete from project_members where project_id=$1 and member_id=$2`, project, collaborator)
	check(collaborator, "artifact_downloaded", pgx.ErrNoRows)
	exec(`update runs set status='cancelled' where id=$1`, run)
	check(actor, "user_cancelled_midway", nil)
	var count int
	if err := tx.QueryRow(ctx, `select count(*) from run_quality_signals where run_id=$1`, run).Scan(&count); err != nil || count != 3 {
		t.Fatalf("recorded %d signals, want 3: %v", count, err)
	}
}
