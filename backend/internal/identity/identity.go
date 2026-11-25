package identity

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"context"
	"crypto/subtle"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

type Member struct {
	Departed bool   `json:"departed,omitempty"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email"`
	Role     string `json:"role"`
	Enabled  bool   `json:"enabled"`
}
type Server struct {
	DB        *pgxpool.Pool
	Bootstrap string
	Secure    bool
	SSO       *EnterpriseSSO
}
type Action func(http.ResponseWriter, *http.Request, Member) error
type credentials struct{ Email, Password, Name, Enterprise, Token string }

func (s Server) Routes(m *http.ServeMux) {
	s.orgRoutes(m)
	s.oidcRoutes(m)
	public := map[string]web.Handler{"GET /api/instance": s.status, "POST /api/initialize": s.initialize, "POST /api/register": s.register, "POST /api/login": s.login, "POST /api/logout": s.logout}
	for path, h := range public {
		m.Handle(path, h)
	}
	m.Handle("GET /api/me", s.Protect(false, func(w http.ResponseWriter, r *http.Request, u Member) error { return web.JSON(w, 200, u) }))
	m.Handle("GET /api/me/account-notices", s.Protect(false, s.accountNotices))
	m.Handle("PUT /api/me/account-notices/{id}/read", s.Protect(false, s.readAccountNotice))
	m.Handle("GET /api/admin/members/{id}/responsibilities", s.Protect(true, s.memberResponsibilities))
	m.Handle("GET /api/admin/members/{id}/departure-handoff", s.Protect(true, s.departurePreview))
	m.Handle("GET /api/admin/members", s.Protect(true, s.members))
	m.Handle("POST /api/admin/invitations", s.Protect(true, s.invite))
	m.Handle("GET /api/admin/members/{id}/history", s.Protect(true, s.memberHistory))
	m.Handle("PATCH /api/admin/members/{id}", s.Protect(true, s.updateMember))
}
func (s Server) Protect(admin bool, next Action) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		u, err := s.Authenticate(r)
		if err != nil {
			return err
		}
		if admin && u.Role != "admin" {
			return web.Fail(403, "需要管理员权限")
		}
		return next(w, r, u)
	}
}

// Authenticate resolves the current session, including expiry and identity-provider policy.
func (s Server) Authenticate(r *http.Request) (Member, error) {
	var u Member
	c, err := r.Cookie("aa_session")
	if err != nil {
		return u, web.Fail(401, "请登录")
	}
	issuer, client, emergency := s.loginScope()
	err = s.DB.QueryRow(r.Context(), `select m.id,m.display_name,m.email,m.role,m.enabled from sessions s join members m on m.id=s.member_id
          left join oidc_bindings b on b.id=s.oidc_binding and b.member_id=m.id
          where s.token_hash=$1 and s.expires_at>now() and m.enabled and
          ((s.oidc_binding is not null and b.issuer=$2 and b.client_id=$3) or
           (s.oidc_binding is null and not s.emergency and not $4) or
           (s.emergency and $4 and m.role='admin' and m.id::text=$5))`, web.Hash(c.Value), issuer, client, s.required(), emergency).Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.Enabled)
	if err == pgx.ErrNoRows {
		return u, web.Fail(401, "登录已失效")
	}
	return u, err
}
func (s Server) status(w http.ResponseWriter, r *http.Request) error {
	var name string
	err := s.DB.QueryRow(r.Context(), "select name from enterprise").Scan(&name)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	state := map[string]any{"initialized": name != "", "name": name}
	if s.SSO != nil {
		state["sso"] = s.SSO.mode
	}
	return web.JSON(w, 200, state)
}
func validate(c *credentials) ([]byte, error) {
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	a, err := mail.ParseAddress(c.Email)
	if err != nil || a.Address != c.Email || web.Blank(c.Name) || len(c.Name) > 160 || len(c.Password) < 12 || len(c.Password) > 72 {
		return nil, web.Fail(422, "请提供姓名、有效邮箱和 12 至 72 字节密码")
	}
	return bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
}
func (s Server) session(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	token := web.Token()
	_, err := tx.Exec(ctx, "insert into sessions(token_hash,member_id,expires_at) values($1,$2,now()+interval '12 hours')", web.Hash(token), id)
	return token, err
}
func (s Server) cookie(w http.ResponseWriter, token string, age int) {
	if s.SSO != nil {
		s.flowCookie(w, "", -1)
	}
	http.SetCookie(w, &http.Cookie{Name: "aa_session", Value: token, Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s Server) initialize(w http.ResponseWriter, r *http.Request) error { return s.enroll(w, r, true) }
func (s Server) register(w http.ResponseWriter, r *http.Request) error   { return s.enroll(w, r, false) }
func (s Server) enroll(w http.ResponseWriter, r *http.Request, first bool) error {
	if s.required() {
		if first {
			return web.Fail(409, "请先以可选登录模式初始化实例并绑定管理员，再启用强制单点登录")
		}
		return web.Fail(403, "请通过企业身份验证并使用邀请加入")
	}
	var c credentials
	if err := web.Read(r, &c); err != nil {
		return err
	}
	if first && (s.Bootstrap == "" || subtle.ConstantTimeCompare(web.Hash(c.Token), web.Hash(s.Bootstrap)) != 1) {
		return web.Fail(403, "初始化凭证无效")
	}
	if first && (web.Blank(c.Enterprise) || len(c.Enterprise) > 200) {
		return web.Fail(422, "请输入企业名称")
	}
	hash, err := validate(&c)
	if err != nil {
		return err
	}
	ctx := r.Context()
	var token, id string
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		role := "member"
		var team string
		if first {
			if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(17952102)"); err != nil {
				return err
			}
			var exists bool
			if err := tx.QueryRow(ctx, "select exists(select from enterprise)").Scan(&exists); err != nil {
				return err
			}
			if exists {
				return web.Fail(409, "实例已完成初始化")
			}
			if err := tx.QueryRow(ctx, "insert into teams(name) values('默认团队') returning id").Scan(&team); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "insert into enterprise(name,default_team) values($1,$2)", c.Enterprise, team); err != nil {
				return err
			}
			role = "admin"
		} else {
			err := tx.QueryRow(ctx, "select team_id from invites where token_hash=$1 and used_by is null and expires_at>now() for update", web.Hash(c.Token)).Scan(&team)
			if err == pgx.ErrNoRows {
				return web.Fail(403, "邀请已失效")
			}
			if err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, "insert into members(display_name,email,password_hash,role) values($1,$2,$3,$4) returning id", strings.TrimSpace(c.Name), c.Email, string(hash), role).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into team_members(team_id,member_id) values($1,$2)", team, id); err != nil {
			return err
		}
		if !first {
			if _, err := tx.Exec(ctx, "update invites set used_by=$1 where token_hash=$2", id, web.Hash(c.Token)); err != nil {
				return err
			}
		}
		if err := database.Audit(ctx, tx, id, "member.enroll", id, map[string]string{"role": role}); err != nil {
			return err
		}
		var err error
		token, err = s.session(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	s.cookie(w, token, 43200)
	return web.JSON(w, 201, map[string]string{"id": id})
}
func (s Server) login(w http.ResponseWriter, r *http.Request) error {
	var c struct {
		credentials
		Reason string
	}
	if err := web.Read(r, &c); err != nil {
		return err
	}
	if s.required() && (web.Blank(c.Reason) || len(c.Reason) > 800) {
		return web.Fail(403, "企业要求单点登录，应急管理员须填写原因")
	}
	var id, hash string
	err := s.DB.QueryRow(r.Context(), "select id,password_hash from members where email=$1 and enabled and kind='person'", strings.ToLower(strings.TrimSpace(c.Email))).Scan(&id, &hash)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil {
		return web.Fail(401, "邮箱或密码不正确")
	}
	var token string
	ctx := r.Context()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(ctx, "select role from members where id=$1 and enabled and password_hash=$2 for share", id, hash).Scan(&role); err != nil {
			return err
		}
		if s.required() {
			if role != "admin" || id != s.SSO.emergency {
				return web.Fail(403, "企业要求使用单点登录")
			}
			token = web.Token()
			if _, err := tx.Exec(ctx, "insert into sessions(token_hash,member_id,expires_at,emergency) values($1,$2,now()+interval '30 minutes',true)", web.Hash(token), id); err != nil {
				return err
			}
			return database.Audit(ctx, tx, id, "identity.oidc.emergency", id, map[string]string{"reason": strings.TrimSpace(c.Reason)})
		}
		var err error
		token, err = s.session(ctx, tx, id)
		return err
	})
	if err != nil {
		return err
	}
	age := 43200
	if s.required() {
		age = 1800
	}
	s.cookie(w, token, age)
	return web.JSON(w, 200, map[string]string{"id": id})
}
func (s Server) logout(w http.ResponseWriter, r *http.Request) error {
	if c, err := r.Cookie("aa_session"); err == nil {
		if _, err = s.DB.Exec(r.Context(), "delete from sessions where token_hash=$1", web.Hash(c.Value)); err != nil {
			return err
		}
	}
	s.cookie(w, "", -1)
	w.WriteHeader(204)
	return nil
}
func (s Server) invite(w http.ResponseWriter, r *http.Request, u Member) error {
	ctx := r.Context()
	token := web.Token()
	expires := time.Now().Add(48 * time.Hour)
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into invites(token_hash,team_id,issued_by,expires_at) select $1,default_team,$2,$3 from enterprise", web.Hash(token), u.ID, expires); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "member.invite", "default-team", map[string]any{"expires": expires})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"token": token, "expires": expires})
}
func RequireAdmin(ctx context.Context, tx pgx.Tx, id string) error {
	var ok bool
	err := tx.QueryRow(ctx, "select enabled and role='admin' from members where id=$1 for share", id).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return web.Fail(403, "管理员权限已变更")
	}
	return nil
}

// RequireMemberAdmin serializes changes to the issuer and recipient in a stable
// order. This also covers an administrator granting or changing their own rights.
func RequireMemberAdmin(ctx context.Context, tx pgx.Tx, issuer, recipient string) error {
	if _, err := tx.Exec(ctx, "select id from members where id in ($1,$2) order by id for no key update", issuer, recipient); err != nil {
		return err
	}
	return RequireAdmin(ctx, tx, issuer)
}

func (s Server) updateMember(w http.ResponseWriter, r *http.Request, u Member) error {
	var in struct {
		Role             string
		Enabled          *bool
		Reason, Password string
		Employment       string
		Successor        string
		Expected         *string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Enabled == nil || web.Blank(in.Reason) || (in.Role != "admin" && in.Role != "member") {
		return web.Fail(422, "请选择角色、账号状态并说明原因")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(17952102)"); err != nil {
			return err
		}
		if err := RequireMemberAdmin(ctx, tx, u.ID, id); err != nil {
			return err
		}
		var hash string
		if err := tx.QueryRow(ctx, "select password_hash from members where id=$1", u.ID).Scan(&hash); err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
			return web.Fail(403, "请重新验证管理员密码")
		}
		var old string
		var enabled, departed bool
		var revision string
		if err := tx.QueryRow(ctx, "select role,enabled,departed_at is not null,account_revision::text from members where id=$1 and kind='person'", id).Scan(&old, &enabled, &departed, &revision); err != nil {
			return err
		}
		if in.Expected != nil && *in.Expected != revision {
			return web.Fail(409, "账号已发生变更，请刷新成员资料后重新核对")
		}
		if old == "admin" && enabled && (in.Role != "admin" || !*in.Enabled) {
			var count int
			if err := tx.QueryRow(ctx, "select count(*) from members where role='admin' and enabled").Scan(&count); err != nil {
				return err
			}
			if count < 2 {
				return web.Fail(409, "请先交接给另一名可用管理员")
			}
		}
		if in.Successor != "" && in.Employment != "depart" {
			return web.Fail(422, "批量交接适用于正式离职")
		}
		switch in.Employment {
		case "":
			if departed {
				return web.Fail(409, "请明确办理重新入职")
			}
		case "depart":
			if *in.Enabled || departed || in.Role != "member" {
				return web.Fail(409, "离职须停用账号并移除管理员身份")
			}
			if in.Successor != "" {
				if err := applyDepartureHandoff(ctx, tx, u.ID, id, in.Successor, in.Reason); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, "update members set enabled=false,departed_at=now(),department_id=null where id=$1", id); err != nil {
				return err
			}
			for _, statement := range []string{
				"update preferences set deleted_at=coalesce(deleted_at,now()),departure_guard=true,enabled=false,revision=revision+1,lifecycle_revision=lifecycle_revision+1 where member_id=$1 and purged_at is null and not departure_guard",
				"update teams set trace_manager=null,trace_policy_revision=trace_policy_revision+1 where trace_manager=$1",
				"delete from team_members where member_id=$1", "delete from project_memberships where member_id=$1", "delete from trace_grants where member_id=$1",
				"delete from channel_codes where member_id=$1", "delete from channel_links where member_id=$1", "delete from channel_contexts where member_id=$1",
				"delete from channel_replies where member_id=$1 and sent_at is null",
				"delete from im_binding_codes where member_id=$1",
				"delete from im_bindings where member_id=$1",
			} {
				if _, err := tx.Exec(ctx, statement, id); err != nil {
					return err
				}
			}
		case "return":
			if !departed || !*in.Enabled || in.Role != "member" {
				return web.Fail(409, "请按普通成员明确办理重新入职")
			}
			if _, err := tx.Exec(ctx, "update members set departed_at=null where id=$1", id); err != nil {
				return err
			}
		default:
			return web.Fail(422, "未知成员状态变更")
		}
		if _, err := tx.Exec(ctx, "update members set role=$1,enabled=$2 where id=$3", in.Role, *in.Enabled, id); err != nil {
			return err
		}
		if !*in.Enabled {
			if _, err := tx.Exec(ctx, "update delegations set revoked_at=now() where member_id=$1 and revoked_at is null", id); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "delete from sessions where member_id=$1", id); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "member.change", id, map[string]any{"employment": in.Employment, "oldRole": old, "oldEnabled": enabled, "role": in.Role, "enabled": *in.Enabled, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
