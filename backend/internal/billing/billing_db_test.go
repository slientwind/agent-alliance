package billing

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReserveUsesTransactionalBudgetTotals(t *testing.T) {
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

	team, actor, project, model := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into teams(id,name) values($1,$2)", team, "billing-test-"+team)
	exec("insert into members(id,email,display_name,password_hash,role) values($1,$2,'billing test','','member')", actor, actor+"@example.test")
	exec("insert into projects(id,owner,team_id,final_reviewer,title) values($1,$2,$3,$2,'billing test')", project, actor, team)
	exec("insert into model_resources(id,name,team_id,created_by) values($1,'billing-test',$2,$3)", model, team, actor)
	var resource string
	if err := tx.QueryRow(ctx, "select id from resource_budgets where model_id=$1", model).Scan(&resource); err != nil {
		t.Fatal(err)
	}
	exec("insert into budgets(team_id,limit_cny) values($1,1)", team)
	exec("update project_budgets set limit_cny=1 where project_id=$1", project)
	exec("update resource_budgets set limit_cny=1,call_limit=2 where id=$1", resource)

	checkTotals := func(amount string, count int) {
		t.Helper()
		var match bool
		err := tx.QueryRow(ctx, `select t.used_cny=$4::numeric and p.used_cny=$4::numeric
		 and r.used_cny=$4::numeric and r.used_calls=$5
		 from budgets t,project_budgets p,resource_budgets r where t.team_id=$1 and p.project_id=$2 and r.id=$3`,
			team, project, resource, amount, count).Scan(&match)
		if err != nil || !match {
			t.Fatalf("budget totals should be %s across all scopes with %d calls: %v", amount, count, err)
		}
	}
	key := uuid.NewString()
	reserve := func(suffix, estimate string) error {
		return Reserve(ctx, tx, Operation{Key: key + suffix, Kind: "model", Actor: actor, Team: team, Project: project,
			Resource: resource, SourceRevision: 1, SourceName: "test", Estimate: &estimate})
	}
	checkConflict := func(err error, text string) {
		t.Helper()
		var problem web.Problem
		if !errors.As(err, &problem) || problem.Status != 409 || !strings.Contains(problem.Message, text) {
			t.Fatalf("expected a 409 containing %q, got %v", text, err)
		}
	}
	for range 2 {
		if err := reserve("first", "0.8"); err != nil {
			t.Fatal(err)
		}
	}
	checkTotals("0.8", 1)
	exec("update spend set amount_cny=0.3 where op_key=$1", key+"first")
	checkTotals("0.8", 1)
	checkConflict(reserve("second", "0.5"), "团队预算不足")
	exec("update spend set settled=true where op_key=$1", key+"first")
	checkTotals("0.3", 1)
	if err := reserve("second", "0.5"); err != nil {
		t.Fatal(err)
	}
	checkTotals("0.8", 2)
	checkConflict(reserve("third", "0.1"), "资源调用次数已用完")

	exec("savepoint undo_delete")
	exec("delete from spend where op_key=$1", key+"second")
	checkTotals("0.3", 1)
	exec("rollback to savepoint undo_delete")
	checkTotals("0.8", 2)
}
