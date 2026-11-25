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

func TestResilienceSharesRateLimitsAndCircuitStateInPostgres(t *testing.T) {
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

	model, provider := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `insert into model_providers(id,name,kind,endpoint)
	 values($1,$1::uuid::text,'openai','https://model.example.test')`, provider); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
	 values($1,$2,'fixture',array['text'],4096,256)`, model, provider); err != nil {
		t.Fatal(err)
	}
	rpm, tpm := 2, 100
	m := Candidate{ModelID: model, RequestsPerMin: &rpm, TokensPerMin: &tpm}
	a, b := Resilience{DB: tx}, Resilience{DB: tx}
	curr := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	acquire := func(r Resilience, tokens int64, at time.Time) Permit {
		t.Helper()
		p, err := r.Acquire(ctx, m, tokens, at)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	wantBlocked := func(r Resilience, tokens int64, at time.Time, want error) {
		t.Helper()
		if _, err := r.Acquire(ctx, m, tokens, at); !errors.Is(err, want) {
			t.Fatalf("got %v rather than %v", err, want)
		}
	}
	complete := func(r Resilience, p Permit, attempt Attempt) {
		t.Helper()
		if err := r.Complete(ctx, p, attempt); err != nil {
			t.Fatal(err)
		}
	}
	first := acquire(a, 70, curr)
	wantBlocked(b, 31, curr, ErrRateLimited)
	complete(a, first, Attempt{Status: "succeeded", Usage: &Usage{PromptTokens: 20}})
	second := acquire(b, 70, curr)
	wantBlocked(a, 1, curr, ErrRateLimited)
	complete(b, second, Attempt{Unsent: true})
	acquire(b, 80, curr)
	wantBlocked(a, 1, curr, ErrRateLimited)

	for n := range maxFailedCalls {
		at := curr.Add(time.Duration(n+1) * time.Minute)
		permit := acquire(a, 5, at)
		complete(a, permit, Attempt{Status: "failed", TransientFailure: true, Finished: at})
	}
	opened := curr.Add(time.Duration(maxFailedCalls) * time.Minute)
	wantBlocked(a, 5, opened.Add(circuitCooldown-time.Second), ErrCircuitOpen)
	probe := acquire(b, 5, opened.Add(circuitCooldown))
	if probe.Probe == nil {
		t.Fatal("expired circuit did not reserve a single probe")
	}
	wantBlocked(a, 5, opened.Add(circuitCooldown), ErrCircuitOpen)
	complete(b, probe, Attempt{Status: "succeeded", Usage: &Usage{PromptTokens: 4}})
	var failures int
	var reopened *time.Time
	if err := tx.QueryRow(ctx, `select failures,open_until from model_gateway_state where model_id=$1`, model).Scan(&failures, &reopened); err != nil || failures != 0 || reopened != nil {
		t.Fatalf("probe did not close circuit: failures=%d reopen=%v err=%v", failures, reopened, err)
	}
}
