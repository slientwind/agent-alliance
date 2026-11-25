package llmgateway

import (
	"context"

	"github.com/jackc/pgx/v5"
)

const readyRoutes = ` from model_tier_routes r
 join model_catalog m on m.id=r.model_id and m.enabled
 join model_providers p on p.id=m.provider_id and p.enabled
 where (case when r.tier in ('default','fast') then 'text' else r.tier end)=any(m.capabilities)
  and exists(select from model_pricing price where price.model_id=m.id and price.effective_at<=now())`

const conversationRoutes = readyRoutes + ` and r.tier in ('default','fast','reasoning') and 'tool_call'=any(m.capabilities)`

type tierReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func EnabledTier(ctx context.Context, q tierReader, tier Tier) (bool, error) {
	var available bool
	err := q.QueryRow(ctx, "select exists(select"+readyRoutes+" and r.tier=$1)", tier).Scan(&available)
	return available, err
}

func ConversationTierReady(ctx context.Context, q tierReader, tier Tier) (bool, error) {
	var available bool
	err := q.QueryRow(ctx, "select exists(select"+conversationRoutes+" and r.tier=$1)", tier).Scan(&available)
	return available, err
}

func ConversationTiers(ctx context.Context, q tierReader) ([]Tier, error) {
	var tiers []Tier
	err := q.QueryRow(ctx, "select array(select distinct r.tier"+conversationRoutes+" order by r.tier)").Scan(&tiers)
	return tiers, err
}
