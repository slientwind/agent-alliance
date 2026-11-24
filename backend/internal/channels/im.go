package channels

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type imChannel struct {
	db                                        *pgxpool.Pool
	vault                                     resources.Vault
	key, provider, name, conversation, public string
}

func (s imChannel) publicConfigured() bool {
	base, err := url.Parse(s.public)
	return err == nil && base.Scheme == "https" && base.Hostname() != "" && base.User == nil &&
		base.RawQuery == "" && !base.ForceQuery && base.Fragment == "" && !strings.ContainsAny(s.public, "<>|")
}

func (s imChannel) conversationGuide() string {
	return "发送“服务”查找已发布服务，或发送“新建 工作目标”“切换 项目ID”进入项目对话。后续在同一" + s.conversation + "发送“进度”“待办”或“补充 内容”。"
}

type imMessage struct {
	event, chat, thread, message, sender, text string
	replyURL                                   string
	replyExpiry                                *time.Time
}

func (s imChannel) accept(ctx context.Context, msg imMessage) error {
	sealed, err := s.vault.Seal(msg.text, s.eventScope(msg.event))
	if err != nil {
		return err
	}
	var target []byte
	if msg.replyURL != "" {
		target, err = s.vault.Seal(msg.replyURL, s.replyScope(msg.event))
		if err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = s.db.Exec(ctx, `insert into im_events(connection,event_id,chat_id,thread_id,message_id,external_user,binding_id,payload,reply_target,reply_expires_at)
        values($1,$2,$3,$4,$5,$6,(select b.id from im_bindings b join members m on m.id=b.member_id
          and m.enabled and m.kind='person' where (b.connection,b.external_user)=($1,$6)),$7,$8,$9) on conflict do nothing`,
		s.key, msg.event, msg.chat, msg.thread, msg.message, msg.sender, sealed, target, msg.replyExpiry)
	return err
}

func (s imChannel) eventScope(id string) string { return s.key + "/event/" + id }
func (s imChannel) replyScope(id string) string { return s.key + "/reply/" + id }

func (s imChannel) bindingRoutes(m *http.ServeMux, g identity.Server, enabled bool) {
	bind := "/api/channels/" + s.provider + "/bind"
	m.Handle("GET "+bind, g.Protect(false, func(w http.ResponseWriter, r *http.Request, member identity.Member) error {
		return s.bindingStatus(w, r, member, enabled)
	}))
	m.Handle("POST "+bind, g.Protect(false, func(w http.ResponseWriter, r *http.Request, member identity.Member) error {
		return s.newBindingCode(w, r, member, enabled)
	}))
	m.Handle("DELETE "+bind, g.Protect(false, s.unlink))
}

func (s imChannel) bindingStatus(w http.ResponseWriter, r *http.Request, member identity.Member, enabled bool) error {
	var bound bool
	if err := s.db.QueryRow(r.Context(), "select exists(select from im_bindings where (connection,member_id)=($1,$2))",
		s.key, member.ID).Scan(&bound); err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]bool{"configured": enabled, "bound": bound})
}

func (s imChannel) newBindingCode(w http.ResponseWriter, r *http.Request, member identity.Member, enabled bool) error {
	if !enabled {
		return web.Fail(409, "企业尚未配置"+s.name+"应用")
	}
	code := web.Token()
	if _, err := s.db.Exec(r.Context(), `insert into im_binding_codes(connection,member_id,token_hash,expires_at)
        values($1,$2,$3,now()+interval '10 minutes') on conflict(connection,member_id) do update
        set token_hash=excluded.token_hash,expires_at=excluded.expires_at`, s.key, member.ID, web.Hash(code)); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 201, map[string]string{"command": "绑定 " + code})
}

func (s imChannel) unlink(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	ctx := r.Context()
	err := database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "delete from im_binding_codes where (connection,member_id)=($1,$2)", s.key, member.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from im_bindings where (connection,member_id)=($1,$2)", s.key, member.ID); err != nil {
			return err
		}
		return database.Audit(ctx, tx, member.ID, "channel.unlink", s.provider, map[string]any{})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
