package identity

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"alliance/internal/database"
	"alliance/internal/web"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/oauth2"
)

type EnterpriseSSO struct {
	httpClient                                          *http.Client
	issuer, clientID, secret, redirect, mode, emergency string
	mu                                                  sync.Mutex
	provider                                            *oidc.Provider
	refreshed                                           time.Time
}

func safeEndpoint(value string, local bool) bool {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	ip := net.ParseIP(u.Hostname())
	return u.Scheme == "https" || (local && u.Scheme == "http" && (u.Hostname() == "localhost" || ip.IsLoopback()))
}

func ConfiguredSSO() (*EnterpriseSSO, error) {
	c := &EnterpriseSSO{issuer: os.Getenv("OIDC_ISSUER"), clientID: os.Getenv("OIDC_CLIENT_ID"), secret: os.Getenv("OIDC_CLIENT_SECRET"), mode: os.Getenv("OIDC_MODE"), emergency: os.Getenv("OIDC_EMERGENCY_MEMBER_ID")}
	if c.issuer == "" && c.clientID == "" && c.secret == "" && c.mode == "" && c.emergency == "" {
		return nil, nil
	}
	if c.mode == "" {
		c.mode = "optional"
	}
	base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")
	u, _ := url.Parse(base)
	issuer, _ := url.Parse(c.issuer)
	if !safeEndpoint(c.issuer, false) || issuer.RawQuery != "" || c.clientID == "" || c.secret == "" || !safeEndpoint(base, os.Getenv("COOKIE_SECURE") == "false") || u.Path != "" || u.RawQuery != "" || (c.mode != "optional" && c.mode != "required") {
		return nil, errors.New("invalid OIDC issuer, client, mode or public base URL")
	}
	if c.mode == "required" {
		id, err := uuid.Parse(c.emergency)
		if err != nil || id == uuid.Nil {
			return nil, errors.New("required OIDC needs a designated emergency administrator UUID")
		}
		c.emergency = id.String()
	}
	c.httpClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if file := os.Getenv("OIDC_CA_FILE"); file != "" {
		pem, err := os.ReadFile(file)
		if err != nil {
			return nil, errors.New("cannot read OIDC CA file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return nil, err
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid OIDC CA file")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		c.httpClient.Transport = transport
	}
	c.redirect = base + "/api/oidc/callback"
	return c, nil
}

func (c *EnterpriseSSO) discover(ctx context.Context) (*oidc.Provider, oauth2.Config, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.provider == nil || time.Since(c.refreshed) > time.Hour {
		p, err := oidc.NewProvider(oidc.ClientContext(ctx, c.httpClient), c.issuer)
		if err != nil {
			return nil, oauth2.Config{}, web.Fail(503, "企业身份服务暂不可用，请稍后重试")
		}
		var metadata struct {
			JWKS string `json:"jwks_uri"`
		}
		if p.Claims(&metadata) != nil || !safeEndpoint(metadata.JWKS, false) || !safeEndpoint(p.Endpoint().AuthURL, false) || !safeEndpoint(p.Endpoint().TokenURL, false) {
			return nil, oauth2.Config{}, web.Fail(503, "身份服务的端点配置无效")
		}
		c.provider = p
		c.refreshed = time.Now()
	}
	return c.provider, oauth2.Config{ClientID: c.clientID, ClientSecret: c.secret, RedirectURL: c.redirect, Endpoint: c.provider.Endpoint(), Scopes: []string{oidc.ScopeOpenID}}, nil
}
func (s Server) required() bool { return s.SSO != nil && s.SSO.mode == "required" }
func (s Server) loginScope() (issuer, client, emergency string) {
	if s.SSO != nil {
		issuer = s.SSO.issuer
		client = s.SSO.clientID
		if s.required() {
			emergency = s.SSO.emergency
		}
	}
	return
}
func (s Server) oidcRoutes(m *http.ServeMux) {
	if s.SSO == nil {
		return
	}
	m.Handle("POST /api/oidc/login", web.Handler(func(w http.ResponseWriter, r *http.Request) error { return s.beginSSO(w, r, "login", Member{}) }))
	m.Handle("POST /api/oidc/enroll", web.Handler(func(w http.ResponseWriter, r *http.Request) error { return s.beginSSO(w, r, "enroll", Member{}) }))
	m.Handle("POST /api/me/oidc/bind", s.Protect(false, func(w http.ResponseWriter, r *http.Request, u Member) error { return s.beginSSO(w, r, "bind", u) }))
	m.Handle("GET /api/me/oidc", s.Protect(false, s.bindingStatus))
	m.Handle("POST /api/me/oidc/unlink", s.Protect(false, s.unlinkSSO))
	m.Handle("GET /api/oidc/callback", web.Handler(s.finishSSO))
}
func (s Server) bindingStatus(w http.ResponseWriter, r *http.Request, u Member) error {
	var bound bool
	err := s.DB.QueryRow(r.Context(), `select exists(select from oidc_bindings where member_id=$1 and issuer=$2 and client_id=$3)`, u.ID, s.SSO.issuer, s.SSO.clientID).Scan(&bound)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"bound": bound, "required": s.required(), "issuer": s.SSO.issuer})
}
func (s Server) flowCookie(w http.ResponseWriter, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "aa_oidc_flow", Value: value, Path: "/api/oidc/callback", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (s Server) beginSSO(w http.ResponseWriter, r *http.Request, intent string, u Member) error {
	var in credentials
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	var session, invite []byte
	var passwordHash *string
	var email, name *string
	switch intent {
	case "bind":
		var hash string
		if err := s.DB.QueryRow(ctx, "select password_hash from members where id=$1 and enabled", u.ID).Scan(&hash); err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
			return web.Fail(403, "绑定前请核对当前平台账号密码")
		}
		cookie, _ := r.Cookie("aa_session")
		session = web.Hash(cookie.Value)
	case "enroll":
		hash, err := validate(&in)
		if err != nil {
			return err
		}
		encoded := string(hash)
		passwordHash = &encoded
		email = &in.Email
		trimmed := strings.TrimSpace(in.Name)
		name = &trimmed
		invite = web.Hash(in.Token)
		var valid bool
		if err := s.DB.QueryRow(ctx, "select exists(select from invites where token_hash=$1 and used_by is null and expires_at>now())", invite).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return web.Fail(403, "邀请已失效")
		}
	}
	_, config, err := s.SSO.discover(ctx)
	if err != nil {
		return err
	}
	state, browser, nonce, verifier := web.Token(), web.Token(), web.Token(), oauth2.GenerateVerifier()
	_, err = s.DB.Exec(ctx, `insert into oidc_flows(state_hash,browser_hash,nonce,verifier,issuer,client_id,redirect_uri,intent,binding_session,invite_hash,email,display_name,password_hash)
      values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, web.Hash(state), web.Hash(browser), nonce, verifier, s.SSO.issuer, s.SSO.clientID, s.SSO.redirect, intent, session, invite, email, name, passwordHash)
	if err != nil {
		return err
	}
	s.flowCookie(w, browser, 300)
	options := []oauth2.AuthCodeOption{oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)}
	if intent != "login" {
		options = append(options, oauth2.SetAuthURLParam("prompt", "login"))
	}
	return web.JSON(w, 200, map[string]string{"url": config.AuthCodeURL(state, options...)})
}

type oidcFlow struct {
	intent, nonce, verifier string
	session, invite         []byte
	email, name, password   *string
}

func oidcResult(w http.ResponseWriter, r *http.Request, code string) error {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, "/?sso="+code, http.StatusSeeOther)
	return nil
}
func (s Server) finishSSO(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	cookie, err := r.Cookie("aa_oidc_flow")
	if err != nil || len(q["state"]) != 1 || q.Get("state") == "" {
		return oidcResult(w, r, "expired")
	}
	var f oidcFlow
	ctx := r.Context()
	err = s.DB.QueryRow(ctx, `delete from oidc_flows where state_hash=$1 and browser_hash=$2 and issuer=$3 and client_id=$4 and redirect_uri=$5 and expires_at>now()
      returning intent,nonce,verifier,binding_session,invite_hash,email,display_name,password_hash`, web.Hash(q.Get("state")), web.Hash(cookie.Value), s.SSO.issuer, s.SSO.clientID, s.SSO.redirect).Scan(&f.intent, &f.nonce, &f.verifier, &f.session, &f.invite, &f.email, &f.name, &f.password)
	if err == pgx.ErrNoRows {
		return oidcResult(w, r, "expired")
	}
	if err != nil {
		return err
	}
	s.flowCookie(w, "", -1)
	if q.Get("error") != "" || len(q["code"]) != 1 || q.Get("code") == "" {
		return oidcResult(w, r, "denied")
	}
	provider, config, err := s.SSO.discover(ctx)
	if err != nil {
		return oidcResult(w, r, "unavailable")
	}
	tokens, err := config.Exchange(oidc.ClientContext(ctx, s.SSO.httpClient), q.Get("code"), oauth2.VerifierOption(f.verifier))
	if err != nil {
		return oidcResult(w, r, "unverified")
	}
	raw, _ := tokens.Extra("id_token").(string)
	token, err := provider.Verifier(&oidc.Config{ClientID: s.SSO.clientID, SupportedSigningAlgs: []string{oidc.RS256}}).Verify(ctx, raw)
	var claims struct {
		AuthorizedParty string `json:"azp"`
	}
	if err != nil || token.Subject == "" || len(token.Subject) > 255 || !token.Expiry.After(time.Now()) || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(f.nonce)) != 1 || token.Claims(&claims) != nil || ((len(token.Audience) > 1 || claims.AuthorizedParty != "") && claims.AuthorizedParty != s.SSO.clientID) {
		return oidcResult(w, r, "unverified")
	}
	var member, binding string
	localToken := web.Token()
	expiry := time.Now().Add(12 * time.Hour)
	if token.Expiry.Before(expiry) {
		expiry = token.Expiry
	}
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		switch f.intent {
		case "bind":
			if err := tx.QueryRow(ctx, `select m.id from sessions s join members m on m.id=s.member_id where s.token_hash=$1 and s.expires_at>now() and m.enabled and m.kind='person' and (not $2 or (s.emergency and m.role='admin' and m.id::text=$3)) for share of m,s`, f.session, s.required(), s.SSO.emergency).Scan(&member); err != nil {
				return err
			}
		case "enroll":
			var team string
			if err := tx.QueryRow(ctx, "select team_id from invites where token_hash=$1 and used_by is null and expires_at>now() for update", f.invite).Scan(&team); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, "insert into members(email,display_name,password_hash,role) values($1,$2,$3,'member') returning id", *f.email, *f.name, *f.password).Scan(&member); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "insert into team_members(team_id,member_id) values($1,$2)", team, member); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "update invites set used_by=$1 where token_hash=$2", member, f.invite); err != nil {
				return err
			}
			if err := database.Audit(ctx, tx, member, "member.enroll", member, map[string]string{"role": "member", "via": "oidc"}); err != nil {
				return err
			}
		}
		if f.intent == "login" {
			if err := tx.QueryRow(ctx, `select m.id,b.id from oidc_bindings b join members m on m.id=b.member_id where b.issuer=$1 and b.client_id=$2 and b.subject=$3 and m.enabled and m.kind='person' for share of m,b`, s.SSO.issuer, s.SSO.clientID, token.Subject).Scan(&member, &binding); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, "insert into oidc_bindings(issuer,client_id,subject,member_id) values($1,$2,$3,$4) returning id", s.SSO.issuer, s.SSO.clientID, token.Subject, member).Scan(&binding); err != nil {
				return err
			}
			if err := database.Audit(ctx, tx, member, "identity.oidc.bind", binding, map[string]string{"issuer": s.SSO.issuer}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, "insert into sessions(token_hash,member_id,expires_at,oidc_binding) values($1,$2,$3,$4)", web.Hash(localToken), member, expiry, binding); err != nil {
			return err
		}
		return database.Audit(ctx, tx, member, "identity.oidc.login", member, map[string]string{"binding": binding})
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return oidcResult(w, r, "unlinked")
		}
		if databaseConflict(err) {
			return oidcResult(w, r, "conflict")
		}
		return err
	}
	s.cookie(w, localToken, int(time.Until(expiry).Seconds())+1)
	return oidcResult(w, r, "success")
}
func databaseConflict(err error) bool {
	var db interface{ SQLState() string }
	return errors.As(err, &db) && db.SQLState() == "23505"
}
func (s Server) unlinkSSO(w http.ResponseWriter, r *http.Request, u Member) error {
	if s.required() {
		return web.Fail(409, "企业要求单点登录，请联系管理员处理身份变更")
	}
	var in struct{ Password, Reason string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) || len(in.Reason) > 800 {
		return web.Fail(422, "请填写解除原因")
	}
	err := database.WithTx(r.Context(), s.DB, func(tx pgx.Tx) error {
		var hash string
		if err := tx.QueryRow(r.Context(), "select password_hash from members where id=$1 and enabled for update", u.ID).Scan(&hash); err != nil {
			return err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
			return web.Fail(403, "请核对当前平台账号密码")
		}
		var id string
		if err := tx.QueryRow(r.Context(), "delete from oidc_bindings where member_id=$1 and issuer=$2 and client_id=$3 returning id", u.ID, s.SSO.issuer, s.SSO.clientID).Scan(&id); err != nil {
			return err
		}
		return database.Audit(r.Context(), tx, u.ID, "identity.oidc.unlink", id, map[string]string{"reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

// ActivateLoginPolicy runs at API startup, before accepting traffic.
func (s Server) ActivateLoginPolicy(ctx context.Context) error {
	issuer, client, emergency := s.loginScope()
	var mode, redirect string
	if s.SSO != nil {
		mode, redirect = s.SSO.mode, s.SSO.redirect
	}
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var oldMode string
		var same bool
		if err := tx.QueryRow(ctx, `select mode,(issuer,client_id,mode,emergency_member,redirect_uri)=($1,$2,$3,$4,$5)
           from enterprise_login_policy where singleton for update`, issuer, client, mode, emergency, redirect).Scan(&oldMode, &same); err != nil {
			return err
		}
		if same {
			return nil
		}
		if oldMode == "required" && mode == "" {
			return errors.New("OIDC environment missing for an instance requiring enterprise login")
		}
		if mode == "required" {
			var ready bool
			if err := tx.QueryRow(ctx, `select exists(select from members where id::text=$1 and enabled and role='admin' and kind='person')
                and exists(select from oidc_bindings b join members m on m.id=b.member_id where b.issuer=$2 and b.client_id=$3 and m.role='admin' and m.enabled)`, emergency, issuer, client).Scan(&ready); err != nil {
				return err
			}
			if !ready {
				return errors.New("bind an active administrator and designate an enabled emergency administrator before requiring OIDC")
			}
		}
		if _, err := tx.Exec(ctx, "delete from oidc_flows"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from sessions where emergency or oidc_binding is not null or $1`, s.required()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update enterprise_login_policy set issuer=$1,client_id=$2,mode=$3,emergency_member=$4,redirect_uri=$5 where singleton`, issuer, client, mode, emergency, redirect); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into audit_events(action,subject,detail) values('identity.oidc.policy','enterprise',jsonb_build_object('mode',$1::text,'previous',$2::text,'issuer',$3::text))`, mode, oldMode, issuer)
		return err
	})
}
