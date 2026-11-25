package llmgateway

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func EnabledTier(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tier Tier) (bool, error) {
	var available bool
	err := q.QueryRow(ctx, `select exists(select from model_tier_routes r
	 join model_catalog m on m.id=r.model_id and m.enabled
	 join model_providers p on p.id=m.provider_id and p.enabled
	 where r.tier=$1 and (case when $1 in ('default','fast') then 'text' else $1 end)=any(m.capabilities)
	  and exists(select from model_pricing price where price.model_id=m.id and price.effective_at<=now()))`,
		tier).Scan(&available)
	return available, err
}
