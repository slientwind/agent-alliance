package llmgateway

import (
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type InitialProvider struct {
	Name, Kind, Endpoint, Key string
	Models                    []InitialModel
}

type InitialModel struct {
	Name                                                          string
	Capabilities                                                  []string
	ContextTokens, MaxOutputTokens                                int
	InputPerMillion, OutputPerMillion, CachedPerMillion, PerImage string
}

var initialTiers = []Tier{Default, Fast, Reasoning, Vision, Image, Embedding}

func initialModelFor(models []modelInput, tier Tier) int {
	for i, model := range models {
		required := string(tier)
		if tier == Default || tier == Fast {
			required = "text"
		}
		if !slices.Contains(model.Capabilities, required) {
			continue
		}
		if (tier == Default || tier == Fast || tier == Reasoning) && !slices.Contains(model.Capabilities, "tool_call") {
			continue
		}
		return i
	}
	return -1
}

// ConfigureInitialProvider runs inside the first-setup transaction. The
// enterprise row is already locked by the caller. Capability routing uses the
// first compatible entry supplied for each of the six tiers.
func ConfigureInitialProvider(ctx context.Context, tx pgx.Tx, vault resources.Vault, actor string, cfg InitialProvider) ([]Tier, error) {
	in := providerInput{Name: cfg.Name, Kind: cfg.Kind, Endpoint: cfg.Endpoint, Key: &cfg.Key}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(in.Endpoint, "https://") || len(cfg.Models) < 1 || len(cfg.Models) > 6 {
		return nil, web.Fail(422, "请填写 HTTPS 服务地址，并登记一至六个模型")
	}
	inputs := make([]modelInput, len(cfg.Models))
	prices := make([]Price, len(cfg.Models))
	names := make(map[string]bool, len(inputs))
	for i, model := range cfg.Models {
		input := modelInput{Name: model.Name, Capabilities: slices.Clone(model.Capabilities), ContextTokens: model.ContextTokens, MaxOutputTokens: model.MaxOutputTokens}
		if err := input.validate(); err != nil {
			return nil, err
		}
		if names[input.Name] || input.ContextTokens < 1 || input.MaxOutputTokens < 1 {
			return nil, web.Fail(422, "模型名称应唯一，请填写输入与输出容量")
		}
		if in.Kind == "anthropic" && (slices.Contains(input.Capabilities, "image") || slices.Contains(input.Capabilities, "embedding")) {
			return nil, web.Fail(422, "此供应商连接不支持生图或向量检索")
		}
		price, err := (priceInput{InputPerMillion: model.InputPerMillion, OutputPerMillion: model.OutputPerMillion,
			CachedPerMillion: model.CachedPerMillion, PerImage: model.PerImage}).price()
		if err != nil {
			return nil, err
		}
		names[input.Name] = true
		inputs[i], prices[i] = input, price
	}
	if initialModelFor(inputs, Default) < 0 {
		return nil, web.Fail(422, "至少登记一个支持对话和工具调用的模型")
	}
	var exists bool
	if err := tx.QueryRow(ctx, "select exists(select from model_providers)").Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, web.Fail(409, "已有模型供应商，请在模型管理中更新")
	}
	if err := egress.AllowPublicEndpoint(ctx, tx, in.Endpoint); err != nil {
		if errors.Is(err, egress.ErrBlocked) {
			return nil, web.Fail(422, "此服务地址未获准使用，请核对地址或企业访问设置")
		}
		return nil, err
	}
	provider := uuid.NewString()
	encrypted, err := (Admin{Vault: vault}).sealProviderKey(provider, in.Key)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `insert into model_providers(id,name,kind,endpoint,encrypted_key) values($1,$2,$3,$4,$5)`,
		provider, in.Name, in.Kind, in.Endpoint, encrypted); err != nil {
		return nil, err
	}
	ids := make([]string, len(inputs))
	for i, model := range inputs {
		if err := tx.QueryRow(ctx, `insert into model_catalog(provider_id,upstream_model,capabilities,context_tokens,max_output_tokens)
		    values($1,$2,$3,$4,$5) returning id`, provider, model.Name, model.Capabilities, model.ContextTokens, model.MaxOutputTokens).Scan(&ids[i]); err != nil {
			return nil, err
		}
		price := prices[i]
		if _, err := tx.Exec(ctx, `insert into model_pricing(model_id,effective_at,input_per_1m_micro_cny,output_per_1m_micro_cny,
		 cached_per_1m_micro_cny,per_image_micro_cny) values($1,transaction_timestamp(),$2,$3,$4,$5)`, ids[i], price.InputPerMillion, price.OutputPerMillion, price.CachedPerMillion, price.PerImage); err != nil {
			return nil, err
		}
	}
	mapped := make([]Tier, 0, len(initialTiers))
	for _, tier := range initialTiers {
		index := initialModelFor(inputs, tier)
		if index == -1 {
			continue
		}
		if _, err := tx.Exec(ctx, "insert into model_tier_routes(tier,model_id,priority,weight) values($1,$2,0,1)", tier, ids[index]); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, "update model_tier_settings set revision=revision+1 where tier=$1", tier); err != nil {
			return nil, err
		}
		mapped = append(mapped, tier)
	}
	return mapped, database.Audit(ctx, tx, actor, "gateway.provider.initial_setup", provider, map[string]any{"tiers": mapped, "models": len(ids)})
}
