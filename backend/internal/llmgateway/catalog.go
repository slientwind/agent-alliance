package llmgateway

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type Tier string

const (
	Default   Tier = "default"
	Fast      Tier = "fast"
	Reasoning Tier = "reasoning"
	Vision    Tier = "vision"
	Image     Tier = "image"
	Embedding Tier = "embedding"
)

func (t Tier) Valid() bool {
	switch t {
	case Default, Fast, Reasoning, Vision, Image, Embedding:
		return true
	default:
		return false
	}
}

type Catalog struct {
	DB interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}
}

type Candidate struct {
	ModelID          string
	ProviderID       string
	Kind             string
	Endpoint         string
	EncryptedKey     []byte `json:"-"`
	Name             string
	Capabilities     []string
	ContextTokens    int
	MaxOutputTokens  int
	RequestsPerMin   *int
	TokensPerMin     *int
	Priority         int
	Weight           int
	PriceVersion     string
	PriceEffectiveAt time.Time
	Price            Price
}

type Price struct {
	InputPerMillion  int64 `json:"inputPerMillionMicroCny"`
	OutputPerMillion int64 `json:"outputPerMillionMicroCny"`
	CachedPerMillion int64 `json:"cachedPerMillionMicroCny"`
	PerImage         int64 `json:"perImageMicroCny"`
}

// ModelsForTier loads the enabled candidates with the price effective when a
// request was admitted. Keeping the price ID with a call permits later billing
// without re-reading the current catalog.
func (c Catalog) ModelsForTier(ctx context.Context, tier Tier, at time.Time) ([]Candidate, error) {
	if !tier.Valid() {
		return nil, errors.New("unknown model tier")
	}
	return c.models(ctx, "from model_tier_routes r join model_catalog m on m.id=r.model_id and m.enabled", "r.priority,r.weight",
		"where r.tier=$1 order by r.priority,m.id", tier, at)
}

func (c Catalog) ModelForEvaluation(ctx context.Context, id string, at time.Time) ([]Candidate, error) {
	return c.models(ctx, "from model_catalog m", "0,1", "where m.id=$1 and m.enabled", id, at)
}

func (c Catalog) models(ctx context.Context, from, weights, condition string, key any, at time.Time) ([]Candidate, error) {
	rows, err := c.DB.Query(ctx, `
	 select m.id,p.id,p.kind,p.endpoint,p.encrypted_key,m.upstream_model,m.capabilities,
	  m.context_tokens,m.max_output_tokens,m.rpm,m.tpm,`+weights+`,
	  q.id,q.effective_at,q.input_per_1m_micro_cny,q.output_per_1m_micro_cny,q.cached_per_1m_micro_cny,q.per_image_micro_cny
	 `+from+`
	 join model_providers p on p.id=m.provider_id and p.enabled
	 join lateral (select * from model_pricing where model_id=m.id and effective_at<=$2 order by effective_at desc limit 1) q on true
	 `+condition, key, at)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Candidate, error) {
		var m Candidate
		err := row.Scan(&m.ModelID, &m.ProviderID, &m.Kind, &m.Endpoint, &m.EncryptedKey, &m.Name, &m.Capabilities,
			&m.ContextTokens, &m.MaxOutputTokens, &m.RequestsPerMin, &m.TokensPerMin, &m.Priority, &m.Weight, &m.PriceVersion,
			&m.PriceEffectiveAt, &m.Price.InputPerMillion, &m.Price.OutputPerMillion, &m.Price.CachedPerMillion, &m.Price.PerImage)
		return m, err
	})
}
