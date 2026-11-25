package bizconnectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type OBOSettings struct {
	Protocol      string `json:"protocol"`
	TokenURL      string `json:"tokenUrl"`
	IntrospectURL string `json:"introspectUrl,omitempty"`
	JWKSURL       string `json:"jwksUrl,omitempty"`
	Issuer        string `json:"issuer"`
	SubjectAud    string `json:"subjectAudience"`
	Resource      string `json:"resource"`
	Scope         string `json:"scope,omitempty"`
}

func oboURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && validText(raw, 550) && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && (u.Path == "" || u.Path == "/" || approvedPath(u.Path, false) && u.RawPath == "")
}

func validOBOSettings(c *OBOSettings) bool {
	if c == nil || !oboURL(c.TokenURL) || !oboURL(c.Issuer) || !validText(c.Resource, 300) ||
		!validText(c.SubjectAud, 300) || (c.Scope != "" && !validText(c.Scope, 400)) {
		return false
	}
	if c.Protocol == "rfc8693" {
		return oboURL(c.IntrospectURL) && c.JWKSURL == ""
	}
	return c.Protocol == "entra" && oboURL(c.JWKSURL) && c.IntrospectURL == "" && c.Scope != ""
}

type oboIdentity struct {
	Subject  string
	Employee string
	DeptCode string
	Revision int
}

type rowIdentity struct {
	Employee, Department, Grant string
}

func oboCipherScope(connector, member string) string {
	return "business-obo/" + connector + "/" + member
}

func (s Server) getOBOIdentity(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, connector, member := r.PathValue("space"), r.PathValue("connector"), r.PathValue("member")
	if !validID(space) || !validID(connector) || !validID(member) {
		return web.Fail(404, "找不到可管理的员工授权")
	}
	var current oboIdentity
	err := s.DB.QueryRow(r.Context(), `select i.issuer_subject,i.employee_no,i.dept_code,i.revision from business_obo_identities i
	 join business_connectors c on c.id=i.connector_id where (c.id,c.space_id,i.member_id)=($1,$2,$3)
	  and space_role(c.space_id,$4)='space_admin' and space_role(c.space_id,i.member_id) in ('member','space_admin')`, connector, space, member, actor.ID).
		Scan(&current.Subject, &current.Employee, &current.DeptCode, &current.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可管理的员工授权")
	}
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, map[string]any{"issuerSubject": current.Subject, "employeeNo": current.Employee, "departmentCode": current.DeptCode, "revision": current.Revision})
}

func (s Server) bindOBOIdentity(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, connector, member := r.PathValue("space"), r.PathValue("connector"), r.PathValue("member")
	if !validID(space) || !validID(connector) || !validID(member) {
		return web.Fail(404, "找不到可管理的员工授权")
	}
	var in struct {
		Subject  string `json:"issuerSubject"`
		Employee string `json:"employeeNo"`
		DeptCode string `json:"departmentCode"`
		Expected int    `json:"expectedVersion"`
	}
	if err := web.ReadLimited(w, r, &in, 8<<10); err != nil {
		return err
	}
	if !validText(in.Subject, 300) || !pathValuePattern.MatchString(in.Employee) || !pathValuePattern.MatchString(in.DeptCode) || in.Expected < 0 {
		return web.Fail(422, "请填写已核验的系统员工号、部门编号和授权主体")
	}
	var version int
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := requireSpaceAdmin(ctx, tx, space, actor.ID); err != nil {
			return err
		}
		var department string
		if err := tx.QueryRow(ctx, `select m.department_id from members m join business_connectors c on (c.id,c.space_id)=($1,$3)
		  where m.id=$2 and m.department_id is not null and c.auth_mode='obo' and space_role(c.space_id,m.id) in ('space_admin','member')
		  for share of m,c`, connector, member, space).Scan(&department); err != nil {
			return err
		}
		if in.Expected == 0 {
			if err := tx.QueryRow(ctx, `insert into business_obo_identities(connector_id,member_id,department_id,issuer_subject,employee_no,dept_code,verified_by)
			  values($1,$2,$3,$4,$5,$6,$7) on conflict do nothing returning revision`,
				connector, member, department, in.Subject, in.Employee, in.DeptCode, actor.ID).Scan(&version); err != nil {
				return err
			}
		} else if err := tx.QueryRow(ctx, `update business_obo_identities set department_id=$3,issuer_subject=$4,employee_no=$5,dept_code=$6,
			  verified_by=$7,revision=revision+1 where (connector_id,member_id,revision)=($1,$2,$8) returning revision`,
			connector, member, department, in.Subject, in.Employee, in.DeptCode, actor.ID, in.Expected).Scan(&version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from business_obo_grants where (connector_id,member_id)=($1,$2)", connector, member); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "business.obo.bind", connector, map[string]any{"member": member, "revision": version})
	})
	var conflict *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &conflict) && conflict.Code == "23505" {
		return web.Fail(409, "需核对员工当前的空间、部门和身份绑定版本")
	}
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"revision": version})
}

func (s Server) oauthForm(ctx context.Context, endpoint string, form url.Values, basic *Credentials) (map[string]any, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, ErrUnavailable
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Accept", "application/json")
	if basic != nil {
		r.SetBasicAuth(basic.AppKey, basic.AppSecret)
	}
	response, err := s.businessHTTP(ctx, r)
	if err != nil {
		return nil, err
	}
	raw, err := boundedBusinessJSON(response)
	object, ok := raw.(map[string]any)
	if err != nil || !ok {
		return nil, ErrUnavailable
	}
	return object, nil
}

func audienceContains(raw any, wanted string) bool {
	switch value := raw.(type) {
	case string:
		return value == wanted
	case []any:
		for _, item := range value {
			if item == wanted {
				return true
			}
		}
	}
	return false
}

func claimExpiry(claim any) (time.Time, bool) {
	v, ok := claim.(json.Number)
	if !ok {
		return time.Time{}, false
	}
	stamp, err := v.Int64()
	return time.Unix(stamp, 0), err == nil
}

func (s Server) verifyOBOSubject(ctx context.Context, config OBOSettings, app Credentials, user oboIdentity, assertion string) (time.Time, error) {
	if config.Protocol == "entra" {
		client, err := s.businessClient(ctx, config.JWKSURL)
		if err != nil {
			return time.Time{}, ErrUnavailable
		}
		verifyCtx := oidc.ClientContext(ctx, client)
		verifier := oidc.NewVerifier(config.Issuer, oidc.NewRemoteKeySet(verifyCtx, config.JWKSURL),
			&oidc.Config{ClientID: config.SubjectAud, SupportedSigningAlgs: []string{oidc.RS256}})
		token, err := verifier.Verify(verifyCtx, assertion)
		if err != nil || token.Subject != user.Subject {
			return time.Time{}, ErrBadQuery
		}
		return token.Expiry, nil
	}
	claims, err := s.oauthForm(ctx, config.IntrospectURL, url.Values{"token": {assertion}, "token_type_hint": {"access_token"}}, &app)
	expiry, known := claimExpiry(claims["exp"])
	if err != nil || claims["active"] != true || claims["sub"] != user.Subject || claims["iss"] != config.Issuer ||
		!audienceContains(claims["aud"], config.SubjectAud) || !known || time.Until(expiry) <= 0 {
		return time.Time{}, ErrBadQuery
	}
	for claim, bound := range map[string]string{"employee_no": user.Employee, "dept_id": user.DeptCode} {
		if value, present := claims[claim]; present && value != bound {
			return time.Time{}, ErrBadQuery
		}
	}
	return expiry, nil
}

func (s Server) exchangeOBO(ctx context.Context, c Connector, app Credentials, profile oboIdentity, subjectToken string) (string, time.Time, error) {
	config := c.Settings.OBO
	if !validOBOSettings(config) || app.AppKey == "" || app.AppSecret == "" {
		return "", time.Time{}, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	expiry, err := s.verifyOBOSubject(ctx, *config, app, profile, subjectToken)
	if err != nil {
		return "", time.Time{}, err
	}
	form := url.Values{}
	if config.Scope != "" {
		form.Set("scope", config.Scope)
	}
	basic := &app
	if config.Protocol == "entra" {
		basic = nil
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
		form.Set("requested_token_use", "on_behalf_of")
		form.Set("assertion", subjectToken)
		form.Set("client_id", app.AppKey)
		form.Set("client_secret", app.AppSecret)
	} else {
		const access = "urn:ietf:params:oauth:token-type:access_token"
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:token-exchange")
		form.Set("subject_token", subjectToken)
		form.Set("subject_token_type", access)
		form.Set("requested_token_type", access)
		form.Set("audience", config.Resource)
	}
	response, err := s.oauthForm(ctx, config.TokenURL, form, basic)
	token, _ := response["access_token"].(string)
	tokenType, _ := response["token_type"].(string)
	ttl, errTTL := strconv.Atoi(strings.TrimSpace(toOAuthValue(response["expires_in"])))
	if err != nil || !validText(token, 12000) || !strings.EqualFold(tokenType, "bearer") || errTTL != nil || ttl < 1 {
		return "", time.Time{}, ErrUnavailable
	}
	if max := time.Now().Add(time.Duration(min(ttl, 3600)) * time.Second); max.Before(expiry) {
		expiry = max
	}
	return token, expiry, nil
}

func toOAuthValue(raw any) string {
	switch value := raw.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	}
	return ""
}

func (s Server) authorizeOBO(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("connector")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到业务连接器")
	}
	var input struct {
		Token string `json:"subjectToken"`
	}
	if err := web.ReadLimited(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validText(input.Token, 12000) {
		return web.Fail(422, "请提交当前员工的 OAuth 授权凭据")
	}
	ctx := r.Context()
	c, app, err := s.load(ctx, space, actor.ID, id, false)
	if err != nil || c.AuthMode != "obo" {
		return web.Fail(404, "找不到可授权的业务连接器")
	}
	var profile oboIdentity
	err = s.DB.QueryRow(ctx, `select i.issuer_subject,i.employee_no,i.dept_code,i.revision
	 from business_obo_identities i join members m on m.id=i.member_id and m.department_id=i.department_id
	 where (i.connector_id,i.member_id)=($1,$2) and m.enabled and m.departed_at is null`, id, actor.ID).
		Scan(&profile.Subject, &profile.Employee, &profile.DeptCode, &profile.Revision)
	if err != nil {
		return web.Fail(403, "当前员工需要有效的业务系统身份绑定")
	}
	token, expiry, err := s.exchangeOBO(ctx, c, app, profile, input.Token)
	if err != nil {
		if errors.Is(err, ErrBadQuery) {
			return web.Fail(403, "授权凭据与当前员工或发行方不匹配")
		}
		return web.Fail(502, "业务系统暂无法授权")
	}
	grant := uuid.NewString()
	cipher, err := s.Vault.Seal(token, oboCipherScope(id, actor.ID)+"/"+grant)
	if err != nil {
		return err
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into business_obo_grants(connector_id,member_id,grant_id,connector_revision,identity_revision,token_cipher,expires_at)
		  select c.id,m.id,$3,c.revision,i.revision,$6,$7 from business_connectors c join business_obo_identities i on i.connector_id=c.id
		  join members m on (m.id,m.department_id)=(i.member_id,i.department_id)
		  where (c.id,m.id,c.revision,i.revision)=($1,$2,$4,$5) and c.enabled and c.auth_mode='obo'
		   and space_role(c.space_id,m.id) in ('space_admin','member')
		  on conflict(connector_id,member_id) do update set grant_id=excluded.grant_id,connector_revision=excluded.connector_revision,
		  identity_revision=excluded.identity_revision,token_cipher=excluded.token_cipher,expires_at=excluded.expires_at`,
			id, actor.ID, grant, c.Revision, profile.Revision, cipher, expiry)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "员工绑定或连接器授权已变化")
		}
		return database.Audit(ctx, tx, actor.ID, "business.obo.authorize", id, map[string]any{"expiresAt": expiry})
	})
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, map[string]any{"authorized": true, "expiresAt": expiry})
}

func (s Server) oboStatus(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("connector")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到业务连接器")
	}
	var bound, authorized bool
	var expiry *time.Time
	err := s.DB.QueryRow(r.Context(), `select i.revision is not null,coalesce(business_obo_grant_usable(c.id,$3,g.grant_id),false),g.expires_at
	 from business_connectors c join members m on m.id=$3 left join business_obo_identities i on (i.connector_id,i.member_id,i.department_id)=(c.id,m.id,m.department_id)
	 left join business_obo_grants g on (g.connector_id,g.member_id)=(i.connector_id,i.member_id)
	 where (c.id,c.space_id)=($1,$2) and c.enabled and c.auth_mode='obo' and space_role(c.space_id,m.id) in ('space_admin','member')`,
		id, space, actor.ID).Scan(&bound, &authorized, &expiry)
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(404, "找不到可授权的业务连接器")
	}
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, http.StatusOK, map[string]any{"bound": bound, "authorized": authorized, "expiresAt": expiry})
}

func (s Server) revokeOBO(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	space, id := r.PathValue("space"), r.PathValue("connector")
	if !validID(space) || !validID(id) {
		return web.Fail(404, "找不到业务连接器")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from business_obo_grants g using business_connectors c where g.connector_id=c.id
		   and (c.id,c.space_id,g.member_id)=($1,$2,$3)`, id, space, actor.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "business.obo.revoke", id, nil)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s Server) loadOBO(ctx context.Context, c Connector, actor string, app Credentials) (rowIdentity, Credentials, error) {
	var rows rowIdentity
	var cipher []byte
	err := s.DB.QueryRow(ctx, `select i.employee_no,i.dept_code,g.grant_id,g.token_cipher
	 from business_obo_grants g join business_obo_identities i using(connector_id,member_id)
	 where (g.connector_id,g.member_id,g.connector_revision)=($1,$2,$3) and business_obo_grant_usable($1,$2,g.grant_id)`,
		c.ID, actor, c.Revision).Scan(&rows.Employee, &rows.Department, &rows.Grant, &cipher)
	if err != nil {
		return rows, Credentials{}, ErrUnavailable
	}
	token, err := s.Vault.Open(cipher, oboCipherScope(c.ID, actor)+"/"+rows.Grant)
	if err != nil || !validText(token, 12000) || !pathValuePattern.MatchString(rows.Employee) || !pathValuePattern.MatchString(rows.Department) {
		return rows, Credentials{}, ErrUnavailable
	}
	secret := Credentials{Token: token, AppKey: app.AppKey, AppSecret: app.AppSecret, Account: rows.Employee}
	if c.Driver == "readonly_sql" {
		secret.Username, secret.Password = rows.Employee, token
	}
	return rows, secret, nil
}
