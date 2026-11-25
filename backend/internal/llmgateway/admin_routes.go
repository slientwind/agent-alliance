package llmgateway

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a Admin) listTiers(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := a.DB.Query(r.Context(), `select jsonb_build_object('tier',s.tier,'revision',s.revision,'routes',
	 coalesce((select jsonb_agg(jsonb_build_object('modelId',r.model_id,'model',m.upstream_model,'providerId',p.id,'provider',p.name,
	  'priority',r.priority,'weight',r.weight,'enabled',p.enabled and m.enabled,'health',p.health)
	  order by r.priority,m.upstream_model,r.model_id) from model_tier_routes r join model_catalog m on m.id=r.model_id
	  join model_providers p on p.id=m.provider_id where r.tier=s.tier),'[]')) from model_tier_settings s
	 order by array_position(array['default','fast','reasoning','vision','image','embedding'],s.tier)`)
	if err != nil {
		return err
	}
	return sendJSONRows(w, rows)
}

type routeInput struct {
	ModelID  string `json:"modelId"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
}

type tierInput struct {
	ExpectedRevision int          `json:"expectedRevision"`
	Routes           []routeInput `json:"routes"`
}

func (in *tierInput) valid() bool {
	if in.ExpectedRevision < 1 || in.Routes == nil || len(in.Routes) > 32 {
		return false
	}
	seen := make(map[uuid.UUID]bool)
	var primary int
	for i := range in.Routes {
		route := &in.Routes[i]
		model, err := uuid.Parse(route.ModelID)
		if err != nil || model == uuid.Nil || seen[model] || route.Priority < 0 || route.Priority > 32767 || route.Weight < 0 || route.Weight > 32767 {
			return false
		}
		seen[model] = true
		if route.Priority == 0 {
			primary++
		}
		route.ModelID = model.String()
		route.Weight = max(1, route.Weight)
	}
	return len(in.Routes) == 0 || primary == 1
}

func (a Admin) updateTier(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in tierInput
	if err := web.ReadLimited(w, r, &in, 16<<10); err != nil {
		return err
	}
	tier := Tier(r.PathValue("tier"))
	if !tier.Valid() || !in.valid() {
		return web.Fail(422, "请为所选档位设置一个主模型和可选备选模型")
	}
	ctx := r.Context()
	var next int
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var revision int
		if err := tx.QueryRow(ctx, `select revision from model_tier_settings where tier=$1 for update`, tier).Scan(&revision); err != nil {
			return err
		}
		if revision != in.ExpectedRevision {
			return web.Fail(409, "档位配置已更新，请刷新后再保存")
		}
		for _, route := range in.Routes {
			var caps []string
			if err := tx.QueryRow(ctx, `select capabilities from model_catalog where id=$1 for share`, route.ModelID).Scan(&caps); err != nil {
				return err
			}
			cap := string(tier)
			if tier == Default || tier == Fast {
				cap = "text"
			}
			if !slices.Contains(caps, cap) {
				return web.Fail(422, "所选模型不支持该档位")
			}
		}
		if _, err := tx.Exec(ctx, `delete from model_tier_routes where tier=$1`, tier); err != nil {
			return err
		}
		for _, route := range in.Routes {
			if _, err := tx.Exec(ctx, `insert into model_tier_routes(tier,model_id,priority,weight) values($1,$2,$3,$4)`, tier,
				route.ModelID, route.Priority, route.Weight); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `update model_tier_settings set revision=revision+1 where tier=$1 returning revision`, tier).Scan(&next); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "gateway.tier.update", string(tier), map[string]any{"revision": next, "routes": in.Routes})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"tier": tier, "revision": next})
}

func (a Admin) listPrices(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := a.DB.Query(r.Context(), `select jsonb_build_object('id',q.id,'modelId',q.model_id,'model',m.upstream_model,
	 'providerId',m.provider_id,'effectiveAt',q.effective_at,'inputPerMillionMicroCny',q.input_per_1m_micro_cny::text,
	 'outputPerMillionMicroCny',q.output_per_1m_micro_cny::text,'cachedPerMillionMicroCny',q.cached_per_1m_micro_cny::text,
	 'perImageMicroCny',q.per_image_micro_cny::text) from model_pricing q join model_catalog m on m.id=q.model_id
	 where ($1='' or q.model_id=nullif($1,'')::uuid) order by q.effective_at desc,q.id limit 200`, r.URL.Query().Get("model"))
	if err != nil {
		return err
	}
	return sendJSONRows(w, rows)
}

type priceInput struct {
	ModelID          string     `json:"modelId"`
	EffectiveAt      *time.Time `json:"effectiveAt"`
	InputPerMillion  string     `json:"inputPerMillionMicroCny"`
	OutputPerMillion string     `json:"outputPerMillionMicroCny"`
	CachedPerMillion string     `json:"cachedPerMillionMicroCny"`
	PerImage         string     `json:"perImageMicroCny"`
}

func (in priceInput) price() (Price, error) {
	var rates [4]int64
	for i, raw := range []string{in.InputPerMillion, in.OutputPerMillion, in.CachedPerMillion, in.PerImage} {
		value, err := strconv.ParseUint(raw, 10, 63)
		if err != nil {
			return Price{}, web.Fail(422, "请输入非负整数微元单价")
		}
		rates[i] = int64(value)
	}
	return Price{InputPerMillion: rates[0], OutputPerMillion: rates[1], CachedPerMillion: rates[2], PerImage: rates[3]}, nil
}

func (a Admin) createPrice(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in priceInput
	if err := web.ReadLimited(w, r, &in, 4<<10); err != nil {
		return err
	}
	price, err := in.price()
	if err != nil {
		return err
	}
	ctx := r.Context()
	var id string
	var effectiveAt time.Time
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,
		 cached_per_1m_micro_cny,per_image_micro_cny) select id,coalesce($2,transaction_timestamp()),$3,$4,$5,$6
		 from model_catalog where id=$1 returning id,effective_at`, in.ModelID, in.EffectiveAt,
			price.InputPerMillion, price.OutputPerMillion, price.CachedPerMillion, price.PerImage).Scan(&id, &effectiveAt); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "gateway.pricing.create", id, map[string]any{"model": in.ModelID, "effectiveAt": effectiveAt})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, map[string]any{"id": id, "effectiveAt": effectiveAt})
}
