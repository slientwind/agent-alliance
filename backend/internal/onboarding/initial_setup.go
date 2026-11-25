package onboarding

import (
	"alliance/internal/database"
	"alliance/internal/ecosystem"
	"alliance/internal/identity"
	"alliance/internal/llmgateway"
	"alliance/internal/web"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const initialReceiptScope = "initial-workspace-setup/receipt"

type initialSetupReceipt struct {
	Tiers       []llmgateway.Tier `json:"tiers"`
	Invitations []string          `json:"invitations"`
	Expires     time.Time         `json:"expires"`
}

func (s Server) initialSetupStatus(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var completed, provider bool
	err := s.Catalog.DB.QueryRow(r.Context(), `select exists(select from initial_workspace_setup),exists(select from model_providers)`).
		Scan(&completed, &provider)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"complete": completed, "hasProvider": provider, "sources": ecosystem.InitialSources()})
}

func (s Server) initialSetup(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Provider    *llmgateway.InitialProvider `json:"provider"`
		Sources     []string                    `json:"sources"`
		InviteCount int                         `json:"inviteCount"`
	}
	key := r.Header.Get("Idempotency-Key")
	if err := web.ReadLimited(w, r, &in, 32<<10); err != nil {
		return err
	}
	if len(key) == 0 || len(key) > 128 || in.InviteCount < 0 || in.InviteCount > 10 || len(in.Sources) > 2 {
		return web.Fail(422, "请核对初始化设置，可一次创建至多 10 个邀请")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var receipt initialSetupReceipt
	status := http.StatusCreated
	err = database.WithTx(ctx, s.Catalog.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var team string
		if err := tx.QueryRow(ctx, "select default_team from enterprise where id for update").Scan(&team); err != nil {
			return err
		}
		var exact bool
		var ciphertext []byte
		err := tx.QueryRow(ctx, `select (actor,request_key,request_hash)=($1,$2,$3),receipt_cipher from initial_workspace_setup`,
			actor.ID, key, web.Hash(string(body))).Scan(&exact, &ciphertext)
		if err == nil {
			if !exact {
				return web.Fail(409, "首次设置已完成，请从企业管理修改配置")
			}
			raw, err := s.Catalog.Vault.Open(ciphertext, initialReceiptScope)
			if err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
				return err
			}
			if time.Now().After(receipt.Expires) {
				receipt.Invitations = []string{}
			}
			status = http.StatusOK
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		receipt = initialSetupReceipt{Tiers: []llmgateway.Tier{}, Invitations: make([]string, 0, in.InviteCount), Expires: time.Now().UTC().Add(48 * time.Hour)}
		if in.Provider != nil {
			receipt.Tiers, err = llmgateway.ConfigureInitialProvider(ctx, tx, s.Catalog.Vault, actor.ID, *in.Provider)
			if err != nil {
				return err
			}
		} else {
			receipt.Tiers = make([]llmgateway.Tier, 0, 6)
			for _, tier := range []llmgateway.Tier{llmgateway.Default, llmgateway.Fast, llmgateway.Reasoning, llmgateway.Vision, llmgateway.Image, llmgateway.Embedding} {
				var eligible bool
				switch tier {
				case llmgateway.Default, llmgateway.Fast, llmgateway.Reasoning:
					eligible, err = llmgateway.ConversationTierReady(ctx, tx, tier)
				default:
					eligible, err = llmgateway.EnabledTier(ctx, tx, tier)
				}
				if err != nil {
					return err
				}
				if eligible {
					receipt.Tiers = append(receipt.Tiers, tier)
				}
			}
		}
		if !hasDefault(receipt.Tiers) {
			return web.Fail(422, "请先接入一个已启用并填写价格的通用模型")
		}
		if err := ecosystem.ConfigureInitialSources(ctx, tx, actor.ID, in.Sources); err != nil {
			return err
		}
		for i := 0; i < in.InviteCount; i++ {
			token := web.Token()
			if _, err := tx.Exec(ctx, "insert into invites(token_hash,team_id,issued_by,expires_at) values($1,$2,$3,$4)",
				web.Hash(token), team, actor.ID, receipt.Expires); err != nil {
				return err
			}
			receipt.Invitations = append(receipt.Invitations, token)
		}
		raw, err := json.Marshal(receipt)
		if err != nil {
			return err
		}
		cipher, err := s.Catalog.Vault.Seal(string(raw), initialReceiptScope)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into initial_workspace_setup(actor,request_key,request_hash,receipt_cipher) values($1,$2,$3,$4)",
			actor.ID, key, web.Hash(string(body)), cipher); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "workspace.initial_setup", team, map[string]any{
			"tiers": receipt.Tiers, "sources": in.Sources, "inviteCount": in.InviteCount})
	})
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, status, receipt)
}

func hasDefault(tiers []llmgateway.Tier) bool {
	for _, tier := range tiers {
		if tier == llmgateway.Default {
			return true
		}
	}
	return false
}
