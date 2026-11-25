package mcphub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Admin struct {
	DB     *pgxpool.Pool
	Client Client
}

func (a Admin) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/mcp/servers", auth.Protect(true, a.list))
	mux.Handle("PUT /api/admin/mcp/servers/{id}", auth.Protect(true, a.configure))
	mux.Handle("POST /api/admin/mcp/servers/{id}/probe", auth.Protect(true, a.probe))
	mux.Handle("PUT /api/admin/mcp/servers/{id}/teams/{team}", auth.Protect(true, a.grantTeam))
}

func (a Admin) list(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var result json.RawMessage
	err := a.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('id',id,'name',name,'endpoint',endpoint,
		'transport',transport,'hasCredential',encrypted_bearer is not null,'tools',tools,'allowPrivateHttp',allow_private_http,
		'enabled',enabled,'revision',revision,'teams',array(select jsonb_build_object('id',g.team_id,
		  'enabled',g.enabled,'revision',g.revision) from mcp_team_grants g where g.server_id=mcp_servers.id order by g.team_id))
		order by name,id),'[]') from mcp_servers`).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, result)
}

func (a Admin) grantTeam(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected int   `json:"expected"`
		Enabled  *bool `json:"enabled"`
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	server, team := r.PathValue("id"), r.PathValue("team")
	if _, err := uuid.Parse(server); err != nil {
		return web.Fail(422, "MCP 服务编号无效")
	}
	if _, err := uuid.Parse(team); err != nil || in.Enabled == nil || in.Expected < 0 {
		return web.Fail(422, "请选择团队及授权状态")
	}
	var revision int
	ctx := r.Context()
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select 1 from mcp_servers s cross join teams t where s.id=$1 and t.id=$2",
			server, team).Scan(new(int)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return web.Fail(404, "MCP 服务或团队不存在")
			}
			return err
		}
		var err error
		if in.Expected == 0 {
			err = tx.QueryRow(ctx, `insert into mcp_team_grants(server_id,team_id,enabled)
				values($1,$2,$3) on conflict do nothing returning revision`, server, team, *in.Enabled).Scan(&revision)
		} else {
			err = tx.QueryRow(ctx, `update mcp_team_grants set enabled=$3,revision=revision+1
				where server_id=$1 and team_id=$2 and revision=$4 returning revision`, server, team, *in.Enabled, in.Expected).Scan(&revision)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "团队授权已更新，请刷新")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "mcp.team.grant", server, map[string]any{"team": team, "enabled": *in.Enabled, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (a Admin) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Name             string            `json:"name"`
		Endpoint         string            `json:"endpoint"`
		Transport        string            `json:"transport"`
		Bearer           *string           `json:"bearer"`
		Tools            map[string]string `json:"tools"`
		AllowPrivateHTTP bool              `json:"allowPrivateHttp"`
		Enabled          bool              `json:"enabled"`
		Expected         int               `json:"expected"`
	}
	if err := web.ReadLimited(w, r, &in, 32<<10); err != nil {
		return err
	}
	raw := r.PathValue("id")
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil || id.String() != raw {
		return web.Fail(422, "MCP 服务编号无效")
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	endpoint, endpointErr := parseEndpoint(in.Endpoint, in.AllowPrivateHTTP)
	if endpointErr != nil || len(in.Endpoint) > 2048 || in.Name == "" || utf8.RuneCountInString(in.Name) > 160 ||
		strings.ContainsRune(in.Name, 0) || in.Expected < 0 || len(in.Tools) > 100 ||
		(in.Transport != StreamableHTTP && in.Transport != SSE) {
		return web.Fail(422, "请核对服务名称、连接地址和传输类型")
	}
	for name, mode := range in.Tools {
		if !toolName.MatchString(name) || (mode != "read_only" && mode != "paid_read" && mode != "write") {
			return web.Fail(422, "请选择免费查询、付费查询或写入")
		}
	}
	if in.Tools == nil {
		in.Tools = map[string]string{}
	}
	if in.Enabled && !a.Client.Egress.Allowed(r.Context(), in.Endpoint, in.AllowPrivateHTTP) {
		return web.Fail(422, "请先在出站白名单授权此 MCP 地址")
	}
	var revision int
	ctx := r.Context()
	err = database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var prior string
		var before int
		var cipher []byte
		err := tx.QueryRow(ctx, "select endpoint,revision,encrypted_bearer from mcp_servers where id=$1 for update", raw).Scan(&prior, &before, &cipher)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if before != in.Expected {
			return web.Fail(409, "MCP 配置已有更新，请刷新")
		}
		if prior != "" && prior != in.Endpoint {
			// Moving the endpoint requires supplying a credential for that destination.
			cipher = nil
		}
		if in.Bearer != nil {
			cipher = nil
			if *in.Bearer != "" {
				if !validBearer(*in.Bearer) || endpoint.Scheme != "https" {
					return web.Fail(422, "MCP 凭据需要使用 HTTPS")
				}
				cipher, err = a.Client.Vault.Seal(*in.Bearer, bearerScope(raw))
				if err != nil {
					return err
				}
			}
		}
		err = tx.QueryRow(ctx, `insert into mcp_servers(id,name,endpoint,transport,encrypted_bearer,tools,allow_private_http,enabled)
			values($1,$2,$3,$4,$5,$6,$7,$8) on conflict(id) do update
			set name=excluded.name,endpoint=excluded.endpoint,transport=excluded.transport,encrypted_bearer=excluded.encrypted_bearer,
			    tools=excluded.tools,allow_private_http=excluded.allow_private_http,enabled=excluded.enabled,
			    revision=mcp_servers.revision+1,updated_at=now()
			where mcp_servers.revision=$9 returning revision`, raw, in.Name, in.Endpoint, in.Transport, cipher, in.Tools,
			in.AllowPrivateHTTP, in.Enabled, before).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "MCP 配置已有更新，请刷新")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "mcp.server.configure", raw, map[string]any{
			"revision": revision, "enabled": in.Enabled, "transport": in.Transport,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (a Admin) probe(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	config := ServerConfig{ID: r.PathValue("id")}
	var revision int
	err := a.DB.QueryRow(r.Context(), `select endpoint,transport,encrypted_bearer,tools,allow_private_http,revision
		from mcp_servers where id=$1 and enabled`, config.ID).Scan(
		&config.Endpoint, &config.Transport, &config.EncryptedBearer, &config.AllowedTools, &config.AllowPrivateHTTP, &revision)
	if err != nil {
		return err
	}
	config.authorized = func(ctx context.Context) bool {
		return a.DB.QueryRow(ctx, "select 1 from mcp_servers where id=$1 and enabled and revision=$2", config.ID, revision).Scan(new(int)) == nil
	}
	session, err := a.Client.Open(r.Context(), config)
	if err != nil {
		return web.Fail(502, ErrUnavailable.Error())
	}
	defer session.Close()
	tools, err := session.discover(r.Context(), true)
	if err != nil {
		return web.Fail(502, ErrUnavailable.Error())
	}
	return web.JSON(w, http.StatusOK, map[string]any{"status": "ready", "tools": tools})
}
