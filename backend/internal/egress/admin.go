package egress

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Admin struct{ DB *pgxpool.Pool }

func (a Admin) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/integrations/outbound-hosts", auth.Protect(true, a.list))
	mux.Handle("PUT /api/admin/integrations/outbound-hosts", auth.Protect(true, a.save))
}

func (a Admin) list(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	var data json.RawMessage
	err := a.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('host',host,'port',port,'trustedNetworks',trusted_networks,
	 'enabled',enabled,'revision',revision,'updatedAt',updated_at) order by host,port),'[]') from outbound_host_allowlist`).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, data)
}

type hostInput struct {
	Host             string   `json:"host"`
	Port             int      `json:"port"`
	TrustedNetworks  []string `json:"trustedNetworks"`
	Enabled          *bool    `json:"enabled"`
	ExpectedRevision int      `json:"expectedRevision"`
}

func (in *hostInput) validate() error {
	host, err := hostIdentity(strings.TrimSpace(in.Host))
	if err != nil || in.Port < 0 || in.Port > 65535 || in.ExpectedRevision < 0 || len(in.TrustedNetworks) > 32 || in.Enabled == nil {
		return web.Fail(422, "请检查域名、端口和启用状态")
	}
	in.Host = host
	if in.Port == 0 {
		in.Port = 443
	}
	prefixes, err := parseTrustedNetworks(in.TrustedNetworks)
	if err != nil {
		return web.Fail(422, "受信任网段须为明确的内网 CIDR")
	}
	in.TrustedNetworks = make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		in.TrustedNetworks = append(in.TrustedNetworks, prefix.String())
	}
	slices.Sort(in.TrustedNetworks)
	in.TrustedNetworks = slices.Compact(in.TrustedNetworks)
	return nil
}

func (a Admin) save(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in hostInput
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		var err error
		if in.ExpectedRevision == 0 {
			err = tx.QueryRow(ctx, `insert into outbound_host_allowlist(host,port,trusted_networks,enabled)
			 values($1,$2,$3::text[]::cidr[],$4) on conflict do nothing returning revision`, in.Host, in.Port, in.TrustedNetworks, in.Enabled).Scan(&revision)
		} else {
			err = tx.QueryRow(ctx, `update outbound_host_allowlist set trusted_networks=$3::text[]::cidr[],enabled=$4,revision=revision+1,updated_at=now()
			 where (host,port,revision)=($1,$2,$5) returning revision`, in.Host, in.Port, in.TrustedNetworks, in.Enabled, in.ExpectedRevision).Scan(&revision)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "出站设置已变化，请刷新后再保存")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "outbound.host.save", in.Host+":"+strconv.Itoa(in.Port),
			map[string]any{"revision": revision, "enabled": in.Enabled, "trustedNetworks": in.TrustedNetworks})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"host": in.Host, "port": in.Port, "revision": revision})
}
