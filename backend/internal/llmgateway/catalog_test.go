package llmgateway

import (
	"alliance/internal/database"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestInvalidTierDoesNotQueryCatalog(t *testing.T) {
	if _, err := (Catalog{}).ModelsForTier(t.Context(), Tier("typo"), time.Now()); err == nil {
		t.Fatal("unrecognized tier accepted")
	}
}

func TestCatalogSelectsEffectivePriceAndEnabledRoute(t *testing.T) {
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
	provider, primary, backup := uuid.NewString(), uuid.NewString(), uuid.NewString()
	old, next := uuid.NewString(), uuid.NewString()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exec(`insert into model_providers(id,name,kind,endpoint) values($1,$2,'openai','https://provider.example.test/v1')`, provider, "provider-"+provider)
	for id, name := range map[string]string{primary: "primary", backup: "backup"} {
		exec(`insert into model_catalog(id,provider_id,upstream_model,capabilities,context_tokens,max_output_tokens,rpm,tpm)
		 values($1,$2,$3,array['text','tool_call'],8192,2048,10,5000)`, id, provider, name)
	}
	exec(`insert into model_pricing(id,model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny)
	 values($1,$3,$5,10,20,3,0),($2,$3,$6,12,24,4,0),(gen_random_uuid(),$4,$5,30,40,0,0)`, old, next, primary, backup, start, start.Add(time.Hour))
	for _, tier := range []Tier{Default, Fast, Reasoning, Vision, Image, Embedding} {
		exec(`insert into model_tier_routes(tier,model_id,priority,weight) values($1,$2,0,1),($1,$3,1,4)`, tier, primary, backup)
	}

	catalog := Catalog{DB: tx}
	models, err := catalog.ModelsForTier(ctx, Fast, start.Add(-time.Second))
	if err != nil || len(models) != 0 {
		t.Fatalf("unpriced route: %v, %v", models, err)
	}
	for _, tc := range []struct {
		at   time.Time
		id   string
		rate int64
	}{
		{start, old, 10},
		{start.Add(time.Hour - time.Second), old, 10},
		{start.Add(time.Hour), next, 12},
	} {
		models, err := catalog.ModelsForTier(ctx, Fast, tc.at)
		if err != nil || len(models) != 2 || models[0].ModelID != primary || models[0].PriceVersion != tc.id ||
			models[0].Price.InputPerMillion != tc.rate || models[1].Weight != 4 || *models[0].TokensPerMin != 5000 {
			t.Fatalf("catalog at %s: %+v, %v", tc.at, models, err)
		}
	}
	exec(`update model_catalog set enabled=false where id=$1`, primary)
	models, err = catalog.ModelsForTier(ctx, Fast, start)
	if err != nil || len(models) != 1 || models[0].ModelID != backup {
		t.Fatalf("disabled primary: %v, %v", models, err)
	}
	exec(`savepoint before_price_edit`)
	if _, err := tx.Exec(ctx, `update model_pricing set input_per_1m_micro_cny=0 where id=$1`, old); err == nil {
		t.Fatal("changed a price revision in place")
	}
	exec(`rollback to savepoint before_price_edit`)
}
