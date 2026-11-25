package bizconnectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB         *pgxpool.Pool
	Vault      resources.Vault
	Egress     egress.Policy
	HTTP       func(context.Context, string) (*http.Client, error)
	BrowserURL string
	BrowserKey string
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	const root = "/api/spaces/{space}/business/connectors"
	mux.Handle("GET "+root, auth.Protect(false, s.list))
	mux.Handle("PUT "+root+"/{connector}", auth.Protect(false, s.configure))
	mux.Handle("POST "+root+"/{connector}/test", auth.Protect(false, s.test))
	mux.Handle("POST /api/spaces/{space}/business/openapi/import", auth.Protect(false, s.importOpenAPI))
	mux.Handle("GET /api/spaces/{space}/business/views", auth.Protect(false, s.listViews))
	mux.Handle("PUT /api/spaces/{space}/business/views/{view}", auth.Protect(false, s.configureView))
	mux.Handle("PUT "+root+"/{connector}/obo-identities/{member}", auth.Protect(false, s.bindOBOIdentity))
	mux.Handle("GET "+root+"/{connector}/obo-identities/{member}", auth.Protect(false, s.getOBOIdentity))
	mux.Handle("POST "+root+"/{connector}/obo", auth.Protect(false, s.authorizeOBO))
	mux.Handle("GET "+root+"/{connector}/obo", auth.Protect(false, s.oboStatus))
	mux.Handle("DELETE "+root+"/{connector}/obo", auth.Protect(false, s.revokeOBO))
	mux.Handle("POST /api/spaces/{space}/business/views/{view}/test", auth.Protect(false, s.testView))
}

const safeColumns = `c.id,c.space_id,c.category,c.driver,c.provider,c.name,
 case when space_role(c.space_id,$2)='space_admin' then c.endpoint else '' end,c.auth_mode,
 case when space_role(c.space_id,$2)='space_admin' then c.settings else '{}' end,c.operations,
 c.health_status,c.revision,c.enabled,c.credential_cipher is not null`

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space := r.PathValue("space")
	if !validID(space) {
		return web.Fail(404, "找不到业务空间")
	}
	rows, err := s.DB.Query(r.Context(), `select `+safeColumns+` from business_connectors c where c.space_id=$1
	 and space_role(c.space_id,$2) in ('space_admin','member') and (c.enabled or space_role(c.space_id,$2)='space_admin')
	 order by c.category,c.name,c.id limit 250`, space, actor.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Connector])
	if err != nil {
		return err
	}
	if items == nil {
		items = []Connector{}
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, items)
}

func requireSpaceAdmin(ctx context.Context, tx pgx.Tx, space, actor string) error {
	var kind string
	err := tx.QueryRow(ctx, `select s.kind from spaces s join members m on m.id=$2 and m.enabled and m.departed_at is null and m.kind='person'
	 where s.id=$1 and s.kind in ('team','commons') and space_role(s.id,m.id)='space_admin' for share of s,m`, space, actor).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的业务空间")
	}
	if err != nil || kind != "team" {
		return err
	}
	var allowed int
	err = tx.QueryRow(ctx, `select 1 from space_members where (space_id,member_id)=($1,$2) and role='space_admin' for share`, space, actor).Scan(&allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的业务空间")
	}
	return err
}

func validateConnector(input *Connector) bool {
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, 480) || utf8.RuneCountInString(input.Name) > 120 ||
		(input.AuthMode != "service" && input.AuthMode != "obo") || len(input.Settings.Resources) > 60 {
		return false
	}
	endpoint, err := endpointURL(input.Endpoint, input.Driver == "readonly_sql")
	if err != nil {
		return false
	}
	input.Endpoint = strings.TrimSuffix(endpoint.String(), "/")
	if input.AuthMode == "obo" {
		if !validOBOSettings(input.Settings.OBO) {
			return false
		}
	} else if input.Settings.OBO != nil {
		return false
	}
	for _, value := range input.Settings.Resources {
		if !validText(value, 1500) {
			return false
		}
	}
	switch input.Category {
	case "erp", "crm", "commerce", "warehouse", "internal":
	default:
		return false
	}
	switch input.Driver {
	case "saas_pack":
		family, operations := saasProfile(input.Provider)
		if family == "" || family != input.Category {
			return false
		}
		input.Operations = operations
	case "openapi_rest":
		if len(input.Operations) == 0 || input.Provider != "openapi" {
			return false
		}
		for _, op := range input.Operations {
			if op.Risk == "read" && op.Method != http.MethodGet && op.Method != http.MethodHead {
				return false
			}
		}
	case "readonly_sql":
		input.Operations = []Operation{}
		if len(input.Settings.AllowedRelations) == 0 || len(input.Settings.AllowedRelations) > 70 || !identifierPattern.MatchString(input.Settings.Database) || input.Category != "warehouse" {
			return false
		}
		for _, relation := range input.Settings.AllowedRelations {
			if !approvedRelation(relation) {
				return false
			}
		}
		want := map[string]string{"postgresql": "postgresql", "mysql": "mysql", "starrocks": "mysql", "clickhouse": "https"}
		if want[input.Provider] != endpoint.Scheme {
			return false
		}
	case "browser_rpa_readonly":
		if input.Provider != "chromium" || !approvedPath(input.Settings.BrowserPath, false) || !selectorPattern.MatchString(input.Settings.TableSelector) {
			return false
		}
		input.Operations = []Operation{{Name: "read_table", Description: "读取已核定页面的业务表格", Method: http.MethodGet, Path: input.Settings.BrowserPath, Risk: "read"}}
	default:
		return false
	}
	return validOperations(input.Operations)
}

func (s Server) configure(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("connector")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到业务连接器")
	}
	var in struct {
		Connector
		Credential *Credentials `json:"credential,omitempty"`
		Expected   int          `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 256<<10); err != nil {
		return err
	}
	if !validateConnector(&in.Connector) || in.Expected < 0 || (in.Credential != nil && !in.Credential.valid()) {
		return web.Fail(422, "业务连接器配置无效")
	}
	var secret []byte
	if in.Credential != nil {
		value, err := json.Marshal(in.Credential)
		if err != nil {
			return err
		}
		secret, err = s.Vault.Seal(string(value), credentialScope(id))
		if err != nil {
			return err
		}
	}
	ctx := r.Context()
	var revision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := requireSpaceAdmin(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		if in.Expected == 0 {
			if err := tx.QueryRow(ctx, `insert into business_connectors(id,space_id,category,driver,provider,name,endpoint,auth_mode,
			 credential_cipher,settings,operations,maintainer,enabled) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 on conflict do nothing returning revision`, id, space, in.Category, in.Driver, in.Provider, in.Name, in.Endpoint, in.AuthMode,
				secret, in.Settings, in.Operations, actor.ID, in.Enabled).Scan(&revision); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "insert into business_connector_bindings(connector_id,space_id,owner_id) values($1,$2,$3)", id, space, actor.ID); err != nil {
				return err
			}
		} else if err := tx.QueryRow(ctx, `update business_connectors set name=$3,auth_mode=$4,credential_cipher=coalesce($5,credential_cipher),
		 settings=$6,operations=$7,enabled=$8,revision=revision+1,health_status='untested',checked_at=null
		 where (id,space_id,revision,driver,provider,category,endpoint)=($1,$2,$9,$10,$11,$12,$13) returning revision`,
			id, space, in.Name, in.AuthMode, secret, in.Settings, in.Operations, in.Enabled,
			in.Expected, in.Driver, in.Provider, in.Category, in.Endpoint).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "business.connector.configure", id,
			map[string]any{"space": space, "driver": in.Driver, "enabled": in.Enabled, "revision": revision})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(409, "业务连接器已变更，请刷新。切换数据源需新建连接器")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"id": id, "revision": revision})
}

func (s Server) load(ctx context.Context, space, actor, id string, adminOnly bool) (Connector, Credentials, error) {
	var c Connector
	var encrypted []byte
	err := s.DB.QueryRow(ctx, `select c.id,c.space_id,c.category,c.driver,c.provider,c.name,c.endpoint,c.auth_mode,c.settings,c.operations,c.health_status,
	 c.revision,c.enabled,c.credential_cipher is not null,c.credential_cipher from business_connectors c
	 where (c.id,c.space_id)=($1,$2) and c.enabled and space_role(c.space_id,$3) in ('member','space_admin')
	 and (not $4 or space_role(c.space_id,$3)='space_admin')`, id, space, actor, adminOnly).Scan(&c.ID, &c.Space, &c.Category, &c.Driver,
		&c.Provider, &c.Name, &c.Endpoint, &c.AuthMode, &c.Settings, &c.Operations, &c.Health, &c.Revision, &c.Enabled, &c.HasSecret, &encrypted)
	if err != nil {
		return c, Credentials{}, err
	}
	var secret Credentials
	if len(encrypted) != 0 {
		raw, err := s.Vault.Open(encrypted, credentialScope(id))
		if err != nil || json.Unmarshal([]byte(raw), &secret) != nil || !secret.valid() {
			return c, Credentials{}, ErrUnavailable
		}
	}
	return c, secret, nil
}

func (s Server) test(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("connector")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到业务连接器")
	}
	var input ReadRequest
	if err := web.ReadLimited(w, r, &input, 32<<10); err != nil {
		return err
	}
	c, secret, err := s.load(r.Context(), space, actor.ID, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可测试的业务连接器")
	}
	if err != nil {
		return web.Fail(503, "连接器凭据不可用")
	}
	if c.AuthMode == "obo" {
		return web.Fail(409, "请先绑定当前员工的业务系统授权")
	}
	result, executionErr := s.read(r.Context(), c, secret, input)
	status := "ready"
	if errors.Is(executionErr, ErrBadQuery) {
		return web.Fail(422, "查询内容超出业务连接器的只读授权")
	}
	if executionErr != nil {
		status = "unavailable"
	}
	if _, err := s.DB.Exec(r.Context(), `update business_connectors set health_status=$3,checked_at=clock_timestamp()
	 where (id,revision)=($1,$2)`, id, c.Revision, status); err != nil {
		return err
	}
	if executionErr != nil {
		return web.Fail(502, "业务系统暂不可连接或返回了无效结果")
	}
	return web.JSON(w, http.StatusOK, map[string]any{"status": status, "rowCount": len(result.Rows), "columns": result.Columns})
}
