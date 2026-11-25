package llmgateway

import (
	"alliance/internal/database"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMonthlyBudgetReservationsAndSettlement(t *testing.T) {
	dsn := os.Getenv("ALLIANCE_LOOKUP_TEST_DATABASE")
	if dsn == "" {
		t.Skip("set ALLIANCE_LOOKUP_TEST_DATABASE to a scratch pgvector database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(cfg.ConnConfig.Database, "alliance_lookup_test") {
		t.Fatal("use a scratch database named alliance_lookup_test*")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
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

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	team, member, price, model, provider := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	exec(`insert into teams(id,name) values($1,$2)`, team, "model budget-"+team)
	exec(`insert into members(id,email,display_name,password_hash,role) values($1,$2,'budget test','','member')`, member, member+"@example.test")
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$1::uuid::text,'openai','https://model.example.test')`, provider)
	exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'test-model',array['text'],4096,1024)`, model, provider)
	exec(`insert into model_pricing(id,model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,$2,now()-interval '1 hour',1000000,0,0,0)`, price, model)
	exec(`insert into model_budget_policies(scope,scope_id,limit_micro_cny) values ('enterprise',$1,80),('team',$2,85),('member',$3,100)`, uuid.Nil, team, member)

	b := Budget{DB: tx}
	key := uuid.NewString()
	reserve := func(suffix string, prompt int64) (bool, error) {
		return b.Reserve(ctx, Reservation{OperationKey: key + suffix, TeamID: team, MemberID: member, PriceID: price, Tier: Default, Estimate: Usage{PromptTokens: prompt}})
	}
	checkConflict := func(suffix string, count int64, want error, scope string) {
		t.Helper()
		if acquired, err := reserve(suffix, count); acquired || !errors.Is(err, want) || !strings.Contains(err.Error(), scope) {
			t.Fatalf("reserve %s: acquired=%v err=%v, want %v %s", suffix, acquired, err, want, scope)
		}
	}
	checkConflict("denied", 1, ErrBudgetForbidden, "")
	exec(`insert into team_members(team_id,member_id) values($1,$2)`, team, member)
	checkConflict("denied", 90, ErrBudgetExceeded, "enterprise")
	exec(`update model_budget_policies set limit_micro_cny=150 where scope='enterprise'`)
	checkConflict("denied", 90, ErrBudgetExceeded, "team")
	exec(`update model_budget_policies set limit_micro_cny=120 where scope='team' and scope_id=$1`, team)
	checkConflict("denied", 101, ErrBudgetExceeded, "member")
	if fresh, err := reserve("original", 80); !fresh || err != nil {
		t.Fatalf("reserve 80: fresh=%v err=%v", fresh, err)
	}
	if fresh, err := reserve("original", 80); fresh || err != nil {
		t.Fatalf("retry: fresh=%v err=%v", fresh, err)
	}
	checkConflict("original", 81, ErrChargeConflict, "")
	var count int
	if err := tx.QueryRow(ctx, `select count(*) from model_budget_alert_events where scope='member' and scope_id=$1 and threshold=80`, member).Scan(&count); err != nil || count != 1 {
		t.Fatalf("soft alerts=%d, err=%v", count, err)
	}
	if fresh, err := reserve("extra", 20); !fresh || err != nil {
		t.Fatalf("reserve up to member limit: fresh=%v err=%v", fresh, err)
	}
	checkConflict("denied", 1, ErrBudgetExceeded, "member")
	for range 2 {
		if cost, err := b.Settle(ctx, key+"original", Usage{PromptTokens: 30}); cost != 30 || err != nil {
			t.Fatalf("settle: cost=%d err=%v", cost, err)
		}
		if err := b.Release(ctx, key+"extra"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Settle(ctx, key+"original", Usage{PromptTokens: 31}); !errors.Is(err, ErrChargeConflict) {
		t.Fatalf("changed settled usage: %v", err)
	}
	if err := tx.QueryRow(ctx, `select count(*) from model_monthly_budget_totals
	 where ((scope='enterprise' and scope_id=$1) or (scope='team' and scope_id=$2) or (scope='member' and scope_id=$3))
	 and spent_micro_cny=30 and reserved_micro_cny=0`, uuid.Nil, team, member).Scan(&count); err != nil || count != 3 {
		t.Fatalf("expected settled totals across all three scopes, count=%d err=%v", count, err)
	}
	if err := tx.QueryRow(ctx, `select count(*) from model_budget_alert_events where scope='member' and scope_id=$1`, member).Scan(&count); err != nil || count != 2 {
		t.Fatalf("expected one event per threshold, count=%d err=%v", count, err)
	}
}
