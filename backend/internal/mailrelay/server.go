package mailrelay

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (r Relay) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/admin/integrations/email", auth.Protect(true, r.settings))
	mux.Handle("PUT /api/admin/integrations/email", auth.Protect(true, r.configure))
	mux.Handle("POST /api/admin/integrations/email/probe", auth.Protect(true, r.probe))
}

type settings struct {
	Host        string     `json:"host"`
	Port        int        `json:"port"`
	Security    string     `json:"security"`
	Sender      string     `json:"sender"`
	Username    string     `json:"username"`
	HasPassword bool       `json:"hasPassword"`
	Enabled     bool       `json:"enabled"`
	Revision    int        `json:"revision"`
	Health      string     `json:"health"`
	CheckedAt   *time.Time `json:"checkedAt"`
}

func (r Relay) settings(w http.ResponseWriter, req *http.Request, _ identity.Member) error {
	var out settings
	err := r.DB.QueryRow(req.Context(), `select host,port,security,sender,username,encrypted_password is not null,enabled,revision,
		health,checked_at from mail_relay`).Scan(&out.Host, &out.Port, &out.Security, &out.Sender, &out.Username,
		&out.HasPassword, &out.Enabled, &out.Revision, &out.Health, &out.CheckedAt)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, out)
}

func (r Relay) configure(w http.ResponseWriter, req *http.Request, actor identity.Member) error {
	var in struct {
		Host, Security, Sender, Username string
		Port, Expected                   int
		Password                         *string
		Enabled                          bool
	}
	if err := web.ReadLimited(w, req, &in, 8<<10); err != nil {
		return err
	}
	in.Host = strings.ToLower(strings.TrimSpace(in.Host))
	in.Sender = strings.TrimSpace(in.Sender)
	if in.Expected < 1 || in.Port < 1 || in.Port > 65535 ||
		in.Host == "" || len(in.Host) > 253 || strings.ContainsAny(in.Host, "/\\@?# \r\n\t") ||
		(strings.Contains(in.Host, ":") && net.ParseIP(in.Host) == nil) || !ValidAddress(in.Sender) ||
		(in.Security != "tls" && in.Security != "starttls") || len(in.Username) > 254 || strings.ContainsRune(in.Username, 0) ||
		(in.Password != nil && (len(*in.Password) > 2048 || strings.ContainsRune(*in.Password, 0))) {
		return web.Fail(422, "请核对邮件服务地址、安全方式与发件地址")
	}
	ctx := req.Context()
	authority := url.URL{Scheme: "https", Host: net.JoinHostPort(in.Host, strconv.Itoa(in.Port))}
	if in.Enabled && !r.Egress.Allowed(ctx, authority.String(), false) {
		return web.Fail(422, "请先在出站白名单授权邮件服务")
	}
	var revision int
	err := database.WithTx(ctx, r.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, actor.ID); err != nil {
			return err
		}
		var previous config
		if err := tx.QueryRow(ctx, `select host,port,security,username,encrypted_password,revision from mail_relay for update`).
			Scan(&previous.Host, &previous.Port, &previous.Security, &previous.Username, &previous.Password, &revision); err != nil {
			return err
		}
		if revision != in.Expected {
			return web.Fail(409, "邮件服务已更新，请刷新")
		}
		next := config{Host: in.Host, Port: in.Port, Security: in.Security, Sender: in.Sender, Username: in.Username, Enabled: in.Enabled}
		if next.Username != "" {
			switch {
			case in.Password == nil && credentialScope(next) == credentialScope(previous):
				next.Password = previous.Password
			case in.Password != nil && *in.Password != "":
				var err error
				next.Password, err = r.Vault.Seal(*in.Password, credentialScope(next))
				if err != nil {
					return err
				}
			}
			if in.Enabled && len(next.Password) == 0 {
				return web.Fail(422, "请输入邮件服务密码后启用")
			}
		}
		if err := tx.QueryRow(ctx, `update mail_relay set host=$1,port=$2,security=$3,sender=$4,username=$5,encrypted_password=$6,
			enabled=$7,revision=revision+1,checked_at=null,health='unchecked' returning revision`,
			next.Host, next.Port, next.Security, next.Sender, next.Username, next.Password, next.Enabled).Scan(&revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "integration.email.configure", "smtp",
			map[string]any{"enabled": next.Enabled, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]int{"revision": revision})
}

func (r Relay) probe(w http.ResponseWriter, req *http.Request, actor identity.Member) error {
	ctx := req.Context()
	c, err := r.current(ctx)
	if err != nil {
		return err
	}
	if c.Host == "" || !ValidAddress(c.Sender) {
		return web.Fail(409, "请先保存邮件服务配置")
	}
	err = r.DB.QueryRow(ctx, `select 1 from members where id=$1 and enabled and kind='person' and role='admin'`, actor.ID).Scan(new(int))
	if errors.Is(err, pgx.ErrNoRows) {
		return web.Fail(403, "需要企业管理员权限")
	}
	if err != nil {
		return err
	}
	short, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	health := "unavailable"
	if client, err := r.connect(short, c, nil); err == nil {
		client.Close()
		health = "ready"
	}
	err = database.WithTx(ctx, r.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update mail_relay set health=$2,checked_at=now() where revision=$1`, c.Revision, health)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "邮件服务已更新，请重新检测")
		}
		return database.Audit(ctx, tx, actor.ID, "integration.email.probe", "smtp", map[string]string{"health": health})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]string{"health": health})
}
