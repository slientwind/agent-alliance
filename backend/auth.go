package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Principal struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	TenantID string `json:"workspaceId"`
	Role     string `json:"role"`
	KeyID    string `json:"-"`
	CSRF     string `json:"csrfToken,omitempty"`
}
type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}
type principalKey struct{}
type Gateway struct {
	db                *sql.DB
	masterKey         []byte
	allowPrivate      bool
	allowRegistration bool
	demo              bool
	secureCookies     bool
	mu                sync.Mutex
	servers           map[string]*Server
}

func newGateway(db *sql.DB, key []byte, allowPrivate bool) *Gateway {
	return &Gateway{db: db, masterKey: key, allowPrivate: allowPrivate, servers: map[string]*Server{}}
}
func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hashToken(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func forbidden() error          { return apiError{403, "没有当前工作区的操作权限"} }
func replyError(w http.ResponseWriter, err error) {
	code := 500
	message := "服务暂不可用，请稍后重试"
	var ae apiError
	if errors.As(err, &ae) {
		code = ae.code
		message = ae.message
	}
	if code == 401 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="agent-alliance"`)
	}
	jsonReply(w, code, map[string]string{"error": message})
}
func (g *Gateway) server(tenantID string) (*Server, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if s := g.servers[tenantID]; s != nil {
		return s, nil
	}
	st, e := newDatabaseStore(g.db, tenantID, g.masterKey)
	if e != nil {
		return nil, e
	}
	s := newServer(st, "", g.allowPrivate)
	g.servers[tenantID] = s
	return s, nil
}
func (g *Gateway) authenticate(r *http.Request) (Principal, error) {
	var p Principal
	ctx := r.Context()
	if auth := r.Header.Get("Authorization"); auth != "" {
		if !strings.HasPrefix(auth, "Bearer ") {
			return p, apiError{401, "平台访问凭据无效"}
		}
		err := g.db.QueryRowContext(ctx, `SELECT k.id,k.tenant_id,k.role,u.id,u.email,u.name FROM alliance_api_keys k JOIN alliance_users u ON u.id=k.created_by WHERE k.hash=$1 AND k.revoked_at IS NULL`, hashToken(strings.TrimPrefix(auth, "Bearer "))).Scan(&p.KeyID, &p.TenantID, &p.Role, &p.UserID, &p.Email, &p.Name)
		if errors.Is(err, sql.ErrNoRows) {
			return p, apiError{401, "平台访问凭据无效或已撤销"}
		}
		if err != nil {
			return p, err
		}
		if selected := r.Header.Get("X-Workspace-ID"); selected != "" && selected != p.TenantID {
			return p, forbidden()
		}
		return p, nil
	}
	cookie, e := r.Cookie("alliance_session")
	if e != nil {
		return p, apiError{401, "请先登录工作区"}
	}
	e = g.db.QueryRowContext(ctx, `SELECT u.id,u.email,u.name,s.csrf FROM alliance_sessions s JOIN alliance_users u ON u.id=s.user_id WHERE s.hash=$1 AND s.expires_at>now()`, hashToken(cookie.Value)).Scan(&p.UserID, &p.Email, &p.Name, &p.CSRF)
	if errors.Is(e, sql.ErrNoRows) {
		return p, apiError{401, "登录已过期，请重新登录"}
	}
	if e != nil {
		return p, e
	}
	p.TenantID = r.Header.Get("X-Workspace-ID")
	if p.TenantID == "" {
		e = g.db.QueryRowContext(ctx, `SELECT tenant_id,role FROM alliance_memberships WHERE user_id=$1 ORDER BY tenant_id LIMIT 1`, p.UserID).Scan(&p.TenantID, &p.Role)
	} else {
		e = g.db.QueryRowContext(ctx, `SELECT role FROM alliance_memberships WHERE user_id=$1 AND tenant_id=$2`, p.UserID, p.TenantID).Scan(&p.Role)
	}
	if errors.Is(e, sql.ErrNoRows) {
		return p, forbidden()
	}
	if e != nil {
		return p, e
	}
	return p, nil
}
func (g *Gateway) checkCSRF(r *http.Request, p Principal) error {
	if p.KeyID != "" || r.Method == "GET" || r.Method == "HEAD" {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(p.CSRF)) != 1 {
		return apiError{403, "请求校验失败，请刷新后重试"}
	}
	return checkOrigin(r)
}
func checkOrigin(r *http.Request) error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	u, e := url.Parse(origin)
	if e != nil || u.Host != r.Host {
		return apiError{403, "不允许跨站请求"}
	}
	return nil
}
func canMutate(p Principal, r *http.Request) bool {
	if r.Method == "GET" || r.Method == "HEAD" {
		return true
	}
	if p.Role == "admin" || p.Role == "editor" {
		return true
	}
	if p.Role == "operator" {
		return r.Method == "POST" && (strings.HasSuffix(r.URL.Path, "/invoke") || strings.HasSuffix(r.URL.Path, "/cancel"))
	}
	return false
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/healthz" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if e := g.db.PingContext(ctx); e != nil {
			jsonReply(w, 503, map[string]string{"status": "database-unavailable"})
		} else {
			jsonReply(w, 200, map[string]string{"status": "ok", "storage": "postgres"})
		}
		return
	}
	if r.URL.Path == "/api/v1/auth/config" && r.Method == "GET" {
		jsonReply(w, 200, map[string]bool{"registrationEnabled": g.allowRegistration, "demoEnabled": g.demo})
		return
	}
	if r.URL.Path == "/api/v1/auth/register" || r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/demo" {
		if r.Method != "POST" {
			replyError(w, apiError{405, "不支持此方法"})
			return
		}
		if e := checkOrigin(r); e != nil {
			replyError(w, e)
			return
		}
		if e := g.authLimit(r); e != nil {
			replyError(w, e)
			return
		}
		out, e := g.signIn(w, r)
		if e != nil {
			replyError(w, e)
		} else {
			jsonReply(w, 200, out)
		}
		return
	}
	p, e := g.authenticate(r)
	if e != nil {
		replyError(w, e)
		return
	}
	if e = g.checkCSRF(r, p); e != nil {
		replyError(w, e)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") || strings.HasPrefix(r.URL.Path, "/api/v1/workspaces") || strings.HasPrefix(r.URL.Path, "/api/v1/api-keys") || r.URL.Path == "/api/v1/audit" {
		code, out, e := g.identityRoute(w, r, p)
		if e != nil {
			replyError(w, e)
		} else {
			jsonReply(w, code, out)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/a2a/") {
		s, e := g.server(p.TenantID)
		if e != nil {
			replyError(w, e)
			return
		}
		g.serveA2A(w, r, p, s)
		return
	}
	if !canMutate(p, r) {
		replyError(w, forbidden())
		return
	}
	s, e := g.server(p.TenantID)
	if e != nil {
		replyError(w, e)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		replyError(w, missing())
		return
	}
	intent := &auditIntent{Principal: p, Action: r.Method, Resource: r.URL.Path}
	r = r.WithContext(context.WithValue(context.WithValue(r.Context(), principalKey{}, p), auditIntentKey{}, intent))
	code, out, e := s.route(w, r)
	if e != nil {
		replyError(w, e)
		return
	}
	if r.Method != "GET" && !intent.Recorded {
		if e = g.audit(r.Context(), p, r.Method, r.URL.Path); e != nil {
			s.logf("audit write failed: %v", e)
		}
	}
	jsonReply(w, code, out)
}
func (g *Gateway) authLimit(r *http.Request) error {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := hashToken(host + "/" + r.URL.Path)
	var count int
	err := g.db.QueryRowContext(r.Context(), `INSERT INTO alliance_auth_limits(key,count,reset_at) VALUES($1,1,now()+interval '15 minutes') ON CONFLICT(key) DO UPDATE SET count=CASE WHEN alliance_auth_limits.reset_at<now() THEN 1 ELSE alliance_auth_limits.count+1 END,reset_at=CASE WHEN alliance_auth_limits.reset_at<now() THEN now()+interval '15 minutes' ELSE alliance_auth_limits.reset_at END RETURNING count`, key).Scan(&count)
	if err != nil {
		return err
	}
	if count > 60 {
		return apiError{429, "登录尝试过多，请稍后重试"}
	}
	return nil
}
func (g *Gateway) signIn(w http.ResponseWriter, r *http.Request) (any, error) {
	var userID string
	if r.URL.Path == "/api/v1/auth/demo" {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip := net.ParseIP(host)
		if !g.demo || ip == nil || !ip.IsLoopback() {
			return nil, forbidden()
		}
		var err error
		userID, err = g.ensureDemo(r.Context())
		if err != nil {
			return nil, err
		}
	} else {
		var q struct {
			Email     string `json:"email"`
			Password  string `json:"password"`
			Name      string `json:"name"`
			Workspace string `json:"workspace"`
		}
		if e := decode(w, r, &q); e != nil {
			return nil, e
		}
		q.Email = strings.ToLower(strings.TrimSpace(q.Email))
		if r.URL.Path == "/api/v1/auth/register" {
			if !g.allowRegistration {
				return nil, forbidden()
			}
			parsed, e := mail.ParseAddress(q.Email)
			if e != nil || parsed.Address != q.Email || len(q.Email) > 254 || !nonempty(q.Name) || len(q.Name) > 100 || !nonempty(q.Workspace) || len(q.Workspace) > 100 {
				return nil, bad("请填写有效邮箱、姓名和工作区名称")
			}
			if len(q.Password) < 12 || len(q.Password) > 72 {
				return nil, bad("密码需为 12–72 字节")
			}
			hash, e := bcrypt.GenerateFromPassword([]byte(q.Password), 12)
			if e != nil {
				return nil, e
			}
			tx, e := g.db.BeginTx(r.Context(), nil)
			if e != nil {
				return nil, e
			}
			defer tx.Rollback()
			userID = id("usr_")
			var inserted string
			e = tx.QueryRowContext(r.Context(), `INSERT INTO alliance_users(id,email,name,password_hash) VALUES($1,$2,$3,$4) ON CONFLICT(email) DO NOTHING RETURNING id`, userID, q.Email, q.Name, string(hash)).Scan(&inserted)
			if errors.Is(e, sql.ErrNoRows) {
				return nil, conflict("该邮箱已注册，请登录")
			}
			if e != nil {
				return nil, e
			}
			if e = createWorkspace(r.Context(), tx, id("ws_"), q.Workspace, userID); e != nil {
				return nil, e
			}
			if e = tx.Commit(); e != nil {
				return nil, e
			}
		} else {
			var hash string
			e := g.db.QueryRowContext(r.Context(), `SELECT id,password_hash FROM alliance_users WHERE email=$1`, q.Email).Scan(&userID, &hash)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return nil, e
			}
			if e != nil {
				hash = "$2a$12$DUG/b7vWxHPgPnJh1O6g6.xZf1rnyYDyj.7fvfBfz3DYgWqJhaJV2"
			}
			if bcrypt.CompareHashAndPassword([]byte(hash), []byte(q.Password)) != nil || userID == "" {
				return nil, apiError{401, "邮箱或密码不正确"}
			}
		}
	}
	value := randomToken()
	csrf := randomToken()
	_, e := g.db.ExecContext(r.Context(), `INSERT INTO alliance_sessions(hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '24 hours')`, hashToken(value), userID, csrf)
	if e != nil {
		return nil, e
	}
	// Rotate any prior browser session rather than leaving it valid after account changes.
	if old, err := r.Cookie("alliance_session"); err == nil {
		_, _ = g.db.ExecContext(r.Context(), `DELETE FROM alliance_sessions WHERE hash=$1`, hashToken(old.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "alliance_session", Value: value, Path: "/", HttpOnly: true, Secure: g.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
	return map[string]string{"csrfToken": csrf}, nil
}
func (g *Gateway) ensureDemo(ctx context.Context) (string, error) {
	tx, e := g.db.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7614430202)`); e != nil {
		return "", e
	}
	var uid string
	e = tx.QueryRowContext(ctx, `SELECT id FROM alliance_users WHERE email='demo@alliance.local'`).Scan(&uid)
	if errors.Is(e, sql.ErrNoRows) {
		uid = id("usr_")
		if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_users(id,email,name,password_hash) VALUES($1,'demo@alliance.local','演示用户','disabled')`, uid); e != nil {
			return "", e
		}
		if e = createWorkspace(ctx, tx, id("ws_"), "演示工作区", uid); e != nil {
			return "", e
		}
	} else if e != nil {
		return "", e
	}
	return uid, tx.Commit()
}
func (g *Gateway) workspaces(ctx context.Context, userID string) ([]Workspace, error) {
	rows, e := g.db.QueryContext(ctx, `SELECT t.id,t.name,m.role FROM alliance_tenants t JOIN alliance_memberships m ON m.tenant_id=t.id WHERE m.user_id=$1 ORDER BY t.created_at`, userID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var v Workspace
		if e = rows.Scan(&v.ID, &v.Name, &v.Role); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (g *Gateway) audit(ctx context.Context, p Principal, action, resource string) error {
	_, e := g.db.ExecContext(ctx, `INSERT INTO alliance_audit(tenant_id,actor_id,action,resource) VALUES($1,$2,$3,$4)`, p.TenantID, p.UserID, action, resource)
	return e
}
func (g *Gateway) identityRoute(w http.ResponseWriter, r *http.Request, p Principal) (int, any, error) {
	path := r.URL.Path
	ctx := r.Context()
	method := r.Method
	if path == "/api/v1/auth/me" && method == "GET" {
		spaces, e := g.workspaces(ctx, p.UserID)
		if p.KeyID != "" {
			spaces = []Workspace{}
		}
		return 200, map[string]any{"user": p, "workspaces": spaces}, e
	}
	if path == "/api/v1/auth/logout" && method == "POST" {
		if c, e := r.Cookie("alliance_session"); e == nil {
			if _, e = g.db.ExecContext(ctx, `DELETE FROM alliance_sessions WHERE hash=$1`, hashToken(c.Value)); e != nil {
				return 0, nil, e
			}
		}
		http.SetCookie(w, &http.Cookie{Name: "alliance_session", Value: "", Path: "/", HttpOnly: true, Secure: g.secureCookies, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		return 200, map[string]bool{"loggedOut": true}, nil
	}
	if path == "/api/v1/workspaces" {
		if p.KeyID != "" {
			return 0, nil, forbidden()
		}
		if method == "GET" {
			spaces, e := g.workspaces(ctx, p.UserID)
			return 200, spaces, e
		}
		if method == "POST" {
			var q struct {
				Name string `json:"name"`
			}
			if e := decode(w, r, &q); e != nil {
				return 0, nil, e
			}
			if !nonempty(q.Name) || len(q.Name) > 100 {
				return 0, nil, bad("工作区名称需为 1–100 字节")
			}
			tx, e := g.db.BeginTx(ctx, nil)
			if e != nil {
				return 0, nil, e
			}
			defer tx.Rollback()
			wid := id("ws_")
			if e = createWorkspace(ctx, tx, wid, q.Name, p.UserID); e != nil {
				return 0, nil, e
			}
			if e = tx.Commit(); e != nil {
				return 0, nil, e
			}
			return 201, Workspace{wid, q.Name, "admin"}, nil
		}
	}
	if path == "/api/v1/workspaces/members" && method == "GET" {
		rows, e := g.db.QueryContext(ctx, `SELECT u.id,u.email,u.name,m.role FROM alliance_memberships m JOIN alliance_users u ON u.id=m.user_id WHERE m.tenant_id=$1 ORDER BY u.email`, p.TenantID)
		if e != nil {
			return 0, nil, e
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var uid, email, name, role string
			if e = rows.Scan(&uid, &email, &name, &role); e != nil {
				return 0, nil, e
			}
			out = append(out, map[string]string{"id": uid, "email": email, "name": name, "role": role})
		}
		return 200, out, rows.Err()
	}
	if p.Role != "admin" {
		return 0, nil, forbidden()
	}
	if path == "/api/v1/workspaces/members" && (method == "POST" || method == "PUT" || method == "DELETE") {
		var q struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if e := decode(w, r, &q); e != nil {
			return 0, nil, e
		}
		q.Email = strings.ToLower(strings.TrimSpace(q.Email))
		if method != "DELETE" && !validRole(q.Role) {
			return 0, nil, bad("无效角色")
		}
		tx, e := g.db.BeginTx(ctx, nil)
		if e != nil {
			return 0, nil, e
		}
		defer tx.Rollback()
		var locked string
		if e = tx.QueryRowContext(ctx, `SELECT id FROM alliance_tenants WHERE id=$1 FOR UPDATE`, p.TenantID).Scan(&locked); e != nil {
			return 0, nil, e
		}
		var uid string
		if e = tx.QueryRowContext(ctx, `SELECT id FROM alliance_users WHERE email=$1`, q.Email).Scan(&uid); errors.Is(e, sql.ErrNoRows) {
			return 0, nil, bad("该邮箱尚未注册，请对方先创建账号")
		}
		if e != nil {
			return 0, nil, e
		}
		var current string
		e = tx.QueryRowContext(ctx, `SELECT role FROM alliance_memberships WHERE tenant_id=$1 AND user_id=$2`, p.TenantID, uid).Scan(&current)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return 0, nil, e
		}
		if method == "POST" && e == nil {
			return 0, nil, conflict("用户已是成员")
		}
		if method != "POST" && errors.Is(e, sql.ErrNoRows) {
			return 0, nil, missing()
		}
		if current == "admin" && (method == "DELETE" || q.Role != "admin") {
			var n int
			if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM alliance_memberships WHERE tenant_id=$1 AND role='admin'`, p.TenantID).Scan(&n); e != nil {
				return 0, nil, e
			}
			if n <= 1 {
				return 0, nil, conflict("不能移除或降级最后一位管理员")
			}
		}
		if method == "DELETE" {
			_, e = tx.ExecContext(ctx, `DELETE FROM alliance_memberships WHERE tenant_id=$1 AND user_id=$2`, p.TenantID, uid)
		} else {
			_, e = tx.ExecContext(ctx, `INSERT INTO alliance_memberships(tenant_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT(tenant_id,user_id) DO UPDATE SET role=excluded.role`, p.TenantID, uid, q.Role)
		}
		if e != nil {
			return 0, nil, e
		}
		if method == "DELETE" || current != q.Role {
			if _, e = tx.ExecContext(ctx, `UPDATE alliance_api_keys SET revoked_at=now() WHERE tenant_id=$1 AND created_by=$2 AND revoked_at IS NULL`, p.TenantID, uid); e != nil {
				return 0, nil, e
			}
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO alliance_audit(tenant_id,actor_id,action,resource) VALUES($1,$2,$3,$4)`, p.TenantID, p.UserID, "member."+method, uid); e != nil {
			return 0, nil, e
		}
		return 200, map[string]bool{"updated": true}, tx.Commit()
	}
	if path == "/api/v1/api-keys" && method == "GET" {
		rows, e := g.db.QueryContext(ctx, `SELECT id,name,prefix,role,created_at,revoked_at FROM alliance_api_keys WHERE tenant_id=$1 ORDER BY created_at DESC`, p.TenantID)
		if e != nil {
			return 0, nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var keyID, name, prefix, role string
			var created time.Time
			var revoked sql.NullTime
			if e = rows.Scan(&keyID, &name, &prefix, &role, &created, &revoked); e != nil {
				return 0, nil, e
			}
			out = append(out, map[string]any{"id": keyID, "name": name, "prefix": prefix, "role": role, "createdAt": created, "revoked": revoked.Valid})
		}
		return 200, out, rows.Err()
	}
	if path == "/api/v1/api-keys" && method == "POST" {
		if p.KeyID != "" {
			return 0, nil, forbidden()
		}
		var q struct {
			Name string `json:"name"`
			Role string `json:"role"`
		}
		if e := decode(w, r, &q); e != nil {
			return 0, nil, e
		}
		if !nonempty(q.Name) || len(q.Name) > 100 || !validRole(q.Role) {
			return 0, nil, bad("名称或角色无效")
		}
		key := "aa_" + randomToken()
		keyID := id("key_")
		_, e := g.db.ExecContext(ctx, `INSERT INTO alliance_api_keys(id,tenant_id,name,hash,prefix,role,created_by) VALUES($1,$2,$3,$4,$5,$6,$7)`, keyID, p.TenantID, q.Name, hashToken(key), key[:11], q.Role, p.UserID)
		if e != nil {
			return 0, nil, e
		}
		return 201, map[string]string{"id": keyID, "key": key, "name": q.Name, "role": q.Role}, g.audit(ctx, p, "key.create", keyID)
	}
	if strings.HasPrefix(path, "/api/v1/api-keys/") && method == "DELETE" {
		keyID := strings.TrimPrefix(path, "/api/v1/api-keys/")
		res, e := g.db.ExecContext(ctx, `UPDATE alliance_api_keys SET revoked_at=now() WHERE id=$1 AND tenant_id=$2`, keyID, p.TenantID)
		if e != nil {
			return 0, nil, e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return 0, nil, missing()
		}
		return 200, map[string]bool{"revoked": true}, g.audit(ctx, p, "key.revoke", keyID)
	}
	if path == "/api/v1/audit" && method == "GET" {
		rows, e := g.db.QueryContext(ctx, `SELECT actor_id,action,resource,created_at FROM alliance_audit WHERE tenant_id=$1 ORDER BY id DESC LIMIT 200`, p.TenantID)
		if e != nil {
			return 0, nil, e
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var actor, action, resource string
			var created time.Time
			if e = rows.Scan(&actor, &action, &resource, &created); e != nil {
				return 0, nil, e
			}
			out = append(out, map[string]any{"actorId": actor, "action": action, "resource": resource, "createdAt": created})
		}
		return 200, out, rows.Err()
	}
	return 0, nil, missing()
}
func validRole(role string) bool {
	return role == "admin" || role == "editor" || role == "operator" || role == "viewer"
}
func (g *Gateway) shutdown() {
	g.mu.Lock()
	servers := make([]*Server, 0, len(g.servers))
	for _, s := range g.servers {
		servers = append(servers, s)
	}
	g.mu.Unlock()
	for _, s := range servers {
		s.shutdown()
	}
}
