package websearch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB     *pgxpool.Pool
	Client Client
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/search/availability", auth.Protect(false, s.availability))
	mux.Handle("GET /api/admin/integrations/search", auth.Protect(true, s.settings))
	mux.Handle("PUT /api/admin/integrations/search", auth.Protect(true, s.configure))
	mux.Handle("POST /api/admin/integrations/search/probe", auth.Protect(true, s.probe))
}

type settings struct {
	Provider      string     `json:"provider"`
	Endpoint      string     `json:"endpoint"`
	HasCredential bool       `json:"hasCredential"`
	Enabled       bool       `json:"enabled"`
	Revision      int        `json:"revision"`
	Health        string     `json:"health"`
	CheckedAt     *time.Time `json:"checkedAt"`
}

func (s Server) availability(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var endpoint string
	err := s.DB.QueryRow(r.Context(), `select endpoint from web_search_integration where enabled`).Scan(&endpoint)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]bool{"enabled": endpoint != "" && s.Client.Egress.Allowed(r.Context(), endpoint, false)})
}

func (s Server) settings(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var out settings
	err := s.DB.QueryRow(r.Context(), `select provider,endpoint,encrypted_key is not null,enabled,revision,health,checked_at
		from web_search_integration`).Scan(&out.Provider, &out.Endpoint, &out.HasCredential, &out.Enabled, &out.Revision, &out.Health, &out.CheckedAt)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (s Server) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Provider, Endpoint string
		Key                *string
		Enabled            bool
		Expected           int
	}
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	standard := DefaultEndpoint(in.Provider)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	if in.Endpoint == "" {
		in.Endpoint = standard
	}
	if standard == "" || in.Expected < 1 || !validEndpoint(in.Endpoint) {
		return web.Fail(422, "请选择搜索服务并填写有效的 HTTPS 地址")
	}
	if in.Key != nil && (len(*in.Key) > 2048 || strings.ContainsFunc(*in.Key, func(c rune) bool { return c <= ' ' || c >= 127 })) {
		return web.Fail(422, "请核对搜索服务密钥")
	}
	ctx := r.Context()
	if in.Enabled && !s.Client.Egress.Allowed(ctx, in.Endpoint, false) {
		return web.Fail(422, "请先在出站白名单授权该搜索服务")
	}
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var prior Config
		if err := tx.QueryRow(ctx, `select provider,endpoint,encrypted_key,revision from web_search_integration for update`).
			Scan(&prior.Provider, &prior.Endpoint, &prior.Credential, &revision); err != nil {
			return err
		}
		if in.Expected != revision {
			return web.Fail(409, "搜索服务配置已更新，请刷新")
		}
		next := Config{Provider: in.Provider, Endpoint: in.Endpoint}
		if next.Provider == prior.Provider && next.Endpoint == prior.Endpoint && in.Key == nil {
			next.Credential = prior.Credential
		}
		if in.Key != nil && *in.Key != "" {
			var err error
			next.Credential, err = s.Client.Vault.Seal(*in.Key, credentialContext(next))
			if err != nil {
				return err
			}
		}
		if in.Enabled && len(next.Credential) == 0 {
			return web.Fail(422, "请填写搜索服务密钥后再启用")
		}
		if err := tx.QueryRow(ctx, `update web_search_integration set provider=$1,endpoint=$2,encrypted_key=$3,enabled=$4,
			revision=revision+1,health='unchecked',checked_at=null returning revision`,
			next.Provider, next.Endpoint, next.Credential, in.Enabled).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "integration.search.configure", next.Provider,
			map[string]any{"enabled": in.Enabled, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (s Server) probe(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	ctx := r.Context()
	var config Config
	var revision int
	if err := s.DB.QueryRow(ctx, `select provider,endpoint,encrypted_key,revision from web_search_integration
		where encrypted_key is not null`).Scan(&config.Provider, &config.Endpoint, &config.Credential, &revision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "请先保存搜索服务密钥")
		}
		return err
	}
	health := "ready"
	if _, err := s.Client.Search(ctx, config, "weather", func(ctx context.Context) bool {
		return s.DB.QueryRow(ctx, `select 1 from web_search_integration where revision=$1
			and exists(select from members where id=$2 and enabled and kind='person' and role='admin')`,
			revision, actor.ID).Scan(new(int)) == nil
	}); err != nil {
		health = "unavailable"
	}
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update web_search_integration set health=$2,checked_at=now() where revision=$1`, revision, health)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "搜索服务配置已更新，请重新检测")
		}
		return database.Audit(ctx, tx, actor.ID, "integration.search.probe", config.Provider, map[string]string{"health": health})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]string{"health": health})
}
