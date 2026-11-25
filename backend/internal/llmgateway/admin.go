package llmgateway

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Admin struct {
	DB interface {
		Begin(context.Context) (pgx.Tx, error)
		Query(context.Context, string, ...any) (pgx.Rows, error)
		QueryRow(context.Context, string, ...any) pgx.Row
	}
	Vault    resources.Vault
	Provider interface {
		Call(context.Context, Candidate, Request) (Result, error)
	}
}

func (a Admin) Routes(mux *http.ServeMux, auth identity.Server) {
	for pattern, handle := range map[string]identity.Action{
		"GET /api/admin/models/providers":                     a.listProviders,
		"POST /api/admin/models/providers":                    a.createProvider,
		"PUT /api/admin/models/providers/{id}":                a.updateProvider,
		"GET /api/admin/models/providers/{id}/impact":         a.providerImpact,
		"POST /api/admin/models/providers/{id}/probe":         a.probeProvider,
		"POST /api/admin/models/providers/{id}/models":        a.createModel,
		"PUT /api/admin/models/providers/{id}/models/{model}": a.updateModel,
		"GET /api/admin/models/tiers":                         a.listTiers,
		"PUT /api/admin/models/tiers/{tier}":                  a.updateTier,
		"GET /api/admin/models/pricing":                       a.listPrices,
		"POST /api/admin/models/pricing":                      a.createPrice,
	} {
		mux.Handle(pattern, auth.Protect(true, handle))
	}
}

func sendJSONRows(w http.ResponseWriter, rows pgx.Rows) error {
	data, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

func (a Admin) listProviders(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := a.DB.Query(r.Context(), `select jsonb_build_object(
	 'id',p.id,'name',p.name,'kind',p.kind,'endpoint',p.endpoint,'enabled',p.enabled,'revision',p.revision,
	 'hasKey',p.encrypted_key is not null,'health',p.health,'probedAt',p.probed_at,'models',coalesce((
	  select jsonb_agg(jsonb_build_object('id',m.id,'name',m.upstream_model,'capabilities',m.capabilities,'enabled',m.enabled,
	   'contextTokens',m.context_tokens,'maxOutputTokens',m.max_output_tokens,'rpm',m.rpm,'tpm',m.tpm) order by m.upstream_model)
	  from model_catalog m where m.provider_id=p.id),'[]')) from model_providers p order by p.name,p.id`)
	if err != nil {
		return err
	}
	return sendJSONRows(w, rows)
}

type providerInput struct {
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	Endpoint         string  `json:"endpoint"`
	Key              *string `json:"key"`
	Enabled          *bool   `json:"enabled"`
	ExpectedRevision int     `json:"expectedRevision"`
}

func (in *providerInput) read(w http.ResponseWriter, r *http.Request) error {
	if err := web.ReadLimited(w, r, in, 16<<10); err != nil {
		return err
	}
	in.Name, in.Endpoint = strings.TrimSpace(in.Name), strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
	if in.Name == "" || len([]rune(in.Name)) > 120 || len(in.Endpoint) > 2048 || !providerEndpoint(in.Kind, in.Endpoint) ||
		(in.Key != nil && (len(*in.Key) > 4096 || strings.ContainsAny(*in.Key, "\r\n\x00"))) {
		return web.Fail(422, "请检查供应商名称、服务地址和凭据格式")
	}
	return nil
}

func (a Admin) sealProviderKey(id string, key *string) ([]byte, error) {
	if key == nil || *key == "" {
		return nil, nil
	}
	return a.Vault.Seal(*key, providerKeyScope(id))
}

func (a Admin) createProvider(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in providerInput
	if err := in.read(w, r); err != nil {
		return err
	}
	id := uuid.NewString()
	sealed, err := a.sealProviderKey(id, in.Key)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(r.Context(), a.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `insert into model_providers(id,name,kind,endpoint,encrypted_key,enabled)
		 values($1,$2,$3,$4,$5,coalesce($6,true))`, id, in.Name, in.Kind, in.Endpoint, sealed, in.Enabled); err != nil {
			return err
		}
		return database.Audit(r.Context(), tx, actor.ID, "gateway.provider.create", id, map[string]any{"kind": in.Kind})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, map[string]any{"id": id, "revision": 1})
}

func (a Admin) updateProvider(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in providerInput
	if err := in.read(w, r); err != nil {
		return err
	}
	if in.ExpectedRevision < 1 {
		return web.Fail(422, "需要当前配置版本")
	}
	id := r.PathValue("id")
	sealed, err := a.sealProviderKey(id, in.Key)
	if err != nil {
		return err
	}
	var next int
	err = pgx.BeginFunc(r.Context(), a.DB, func(tx pgx.Tx) error {
		var currentKind, endpoint string
		var revision int
		var hasKey bool
		if err := tx.QueryRow(r.Context(), `select kind,endpoint,revision,encrypted_key is not null from model_providers where id=$1 for update`, id).
			Scan(&currentKind, &endpoint, &revision, &hasKey); err != nil {
			return err
		}
		if revision != in.ExpectedRevision {
			return web.Fail(409, "供应商配置已更新，请刷新后再保存")
		}
		if currentKind != in.Kind {
			return web.Fail(422, "请为不同协议新增供应商")
		}
		if endpoint != in.Endpoint && hasKey && in.Key == nil {
			return web.Fail(422, "更改服务地址时请重新确认凭据")
		}
		if err := tx.QueryRow(r.Context(), `update model_providers set name=$2,endpoint=$3,enabled=coalesce($4,enabled),
		 encrypted_key=case when $5 then $6 else encrypted_key end,revision=revision+1,
		 health=case when endpoint<>$3 or $5 then 'unchecked' else health end,
		 probed_at=case when endpoint<>$3 or $5 then null else probed_at end where id=$1 returning revision`,
			id, in.Name, in.Endpoint, in.Enabled, in.Key != nil, sealed).Scan(&next); err != nil {
			return err
		}
		return database.Audit(r.Context(), tx, actor.ID, "gateway.provider.update", id, map[string]any{"revision": next, "keyChanged": in.Key != nil})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"id": id, "revision": next})
}

func (a Admin) providerImpact(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var data json.RawMessage
	err := a.DB.QueryRow(r.Context(), `select jsonb_build_object('id',p.id,'revision',p.revision,'routes',coalesce((
	 select jsonb_agg(jsonb_build_object('tier',r.tier,'model',m.upstream_model,'priority',r.priority,'availableAlternates',(
	  select count(*) from model_tier_routes b join model_catalog bm on bm.id=b.model_id and bm.enabled
	  join model_providers bp on bp.id=bm.provider_id and bp.enabled where b.tier=r.tier and bp.id<>p.id
	   and exists(select from model_pricing q where q.model_id=bm.id and q.effective_at<=now()))) order by r.tier,r.priority)
	 from model_tier_routes r join model_catalog m on m.id=r.model_id where m.provider_id=p.id),'[]'))
	 from model_providers p where p.id=$1`, r.PathValue("id")).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

func (a Admin) probeProvider(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		ModelID          string `json:"modelId"`
		ExpectedRevision int    `json:"expectedRevision"`
	}
	if err := web.ReadLimited(w, r, &in, 4<<10); err != nil {
		return err
	}
	ctx := r.Context()
	var model Candidate
	var revision int
	err := a.DB.QueryRow(ctx, `select p.id,p.kind,p.endpoint,p.encrypted_key,p.revision,m.id,m.upstream_model,m.capabilities,m.max_output_tokens
	 from model_providers p join model_catalog m on m.provider_id=p.id where p.id=$1 and m.id=$2`, r.PathValue("id"), in.ModelID).
		Scan(&model.ProviderID, &model.Kind, &model.Endpoint, &model.EncryptedKey, &revision, &model.ModelID, &model.Name, &model.Capabilities, &model.MaxOutputTokens)
	if err != nil {
		return err
	}
	if revision != in.ExpectedRevision {
		return web.Fail(409, "供应商配置已更新，请刷新后再检测")
	}
	probe := Request{Tier: Default, MaxTokens: 32, Messages: []resources.Message{{Role: "user", Content: "Reply with OK."}}}
	if slices.Contains(model.Capabilities, "embedding") {
		probe = Request{Tier: Embedding, Embeddings: []string{"Alliance connection check"}}
	} else if !slices.Contains(model.Capabilities, "text") && !slices.Contains(model.Capabilities, "reasoning") && !slices.Contains(model.Capabilities, "vision") {
		return web.Fail(422, "请选择可运行文字或向量检测的模型")
	}
	callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	started := time.Now()
	_, probeErr := a.Provider.Call(callCtx, model, probe)
	status := "healthy"
	if probeErr != nil {
		status = "unavailable"
	}
	var providerCode int
	var providerErr ProviderError
	if errors.As(probeErr, &providerErr) {
		providerCode = providerErr.Status
	}
	err = pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update model_providers set health=$2,probed_at=now() where id=$1 and revision=$3`, model.ProviderID, status, revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "检测期间配置已更新，请重新检测")
		}
		return database.Audit(ctx, tx, actor.ID, "gateway.provider.probe", model.ProviderID, map[string]any{"health": status, "providerStatus": providerCode})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"health": status, "providerStatus": providerCode, "elapsedMs": time.Since(started).Milliseconds()})
}

type modelInput struct {
	Name            string   `json:"name"`
	Capabilities    []string `json:"capabilities"`
	ContextTokens   int      `json:"contextTokens"`
	MaxOutputTokens int      `json:"maxOutputTokens"`
	RPM             *int     `json:"rpm"`
	TPM             *int     `json:"tpm"`
	Enabled         *bool    `json:"enabled"`
}

func (in *modelInput) read(w http.ResponseWriter, r *http.Request) error {
	if err := web.ReadLimited(w, r, in, 8<<10); err != nil {
		return err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len([]rune(in.Name)) > 240 || len(in.Capabilities) == 0 || len(in.Capabilities) > 6 ||
		in.ContextTokens < 0 || in.MaxOutputTokens < 0 || (in.ContextTokens > 0 && in.MaxOutputTokens > in.ContextTokens) ||
		(in.RPM != nil && *in.RPM <= 0) || (in.TPM != nil && *in.TPM <= 0) {
		return web.Fail(422, "请检查模型名称、能力及用量上限")
	}
	slices.Sort(in.Capabilities)
	in.Capabilities = slices.Compact(in.Capabilities)
	for _, cap := range in.Capabilities {
		switch cap {
		case "text", "tool_call", "reasoning", "vision", "image", "embedding":
		default:
			return web.Fail(422, "存在未知模型能力")
		}
	}
	return nil
}

func (a Admin) createModel(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	return a.saveModel(w, r, actor, "")
}

func (a Admin) updateModel(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	return a.saveModel(w, r, actor, r.PathValue("model"))
}

func (a Admin) saveModel(w http.ResponseWriter, r *http.Request, actor identity.Member, id string) error {
	var in modelInput
	if err := in.read(w, r); err != nil {
		return err
	}
	status := http.StatusOK
	provider := r.PathValue("id")
	err := pgx.BeginFunc(r.Context(), a.DB, func(tx pgx.Tx) error {
		if id == "" {
			status = http.StatusCreated
			if err := tx.QueryRow(r.Context(), `insert into model_catalog(provider_id,upstream_model,capabilities,context_tokens,max_output_tokens,rpm,tpm,enabled)
			 select id,$2,$3,$4,$5,$6,$7,coalesce($8,true) from model_providers where id=$1 returning id`, provider,
				in.Name, in.Capabilities, in.ContextTokens, in.MaxOutputTokens, in.RPM, in.TPM, in.Enabled).Scan(&id); err != nil {
				return err
			}
		} else {
			// Pricing revisions attach to a stable upstream model identity.
			tag, err := tx.Exec(r.Context(), `update model_catalog set capabilities=$4,context_tokens=$5,max_output_tokens=$6,
			 rpm=$7,tpm=$8,enabled=coalesce($9,enabled) where provider_id=$1 and id=$2 and upstream_model=$3`,
				provider, id, in.Name, in.Capabilities, in.ContextTokens, in.MaxOutputTokens, in.RPM, in.TPM, in.Enabled)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "模型标识已变化，请刷新配置")
			}
		}
		return database.Audit(r.Context(), tx, actor.ID, "gateway.model.save", id, map[string]any{"provider": provider})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, status, map[string]any{"id": id})
}
