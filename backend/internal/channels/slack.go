package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Slack struct {
	db                      *pgxpool.Pool
	vault                   resources.Vault
	team, app, signing, bot string
	public, endpoint        string
	client                  *http.Client
}

func NewSlack(db *pgxpool.Pool, vault resources.Vault) Slack {
	return Slack{db: db, vault: vault, team: os.Getenv("SLACK_TEAM_ID"), app: os.Getenv("SLACK_APP_ID"),
		signing: os.Getenv("SLACK_SIGNING_SECRET"), bot: os.Getenv("SLACK_BOT_TOKEN"),
		public: strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"), endpoint: "https://slack.com/api/chat.postMessage",
		client: &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

var slackID = regexp.MustCompile(`^[A-Za-z0-9]{2,64}$`)
var slackTimestamp = regexp.MustCompile(`^[0-9]{1,16}\.[0-9]{1,12}$`)

func (s Slack) connection() string { return "slack/" + s.team + "/" + s.app }

func (s Slack) configured() bool {
	base, err := url.Parse(s.public)
	return err == nil && base.Scheme == "https" && base.Hostname() != "" && base.User == nil &&
		base.RawQuery == "" && !base.ForceQuery && base.Fragment == "" && !strings.ContainsAny(s.public, "<>|") &&
		slackID.MatchString(s.team) && strings.HasPrefix(s.team, "T") && slackID.MatchString(s.app) && strings.HasPrefix(s.app, "A") &&
		s.signing != "" && s.bot != ""
}

func (s Slack) Routes(m *http.ServeMux, g identity.Server) {
	m.Handle("GET /api/channels/slack/bind", g.Protect(false, s.bindingStatus))
	m.Handle("POST /api/channels/slack/bind", g.Protect(false, s.newBindingCode))
	m.Handle("DELETE /api/channels/slack/bind", g.Protect(false, s.unlink))
	m.Handle("POST /api/channels/slack/events", web.Handler(s.event))
}

func (s Slack) bindingStatus(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	var bound bool
	if err := s.db.QueryRow(r.Context(), "select exists(select from im_bindings where (connection,member_id)=($1,$2))",
		s.connection(), member.ID).Scan(&bound); err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]bool{"configured": s.configured(), "bound": bound})
}

func (s Slack) newBindingCode(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	if !s.configured() {
		return web.Fail(409, "企业尚未配置 Slack 应用")
	}
	code := web.Token()
	if _, err := s.db.Exec(r.Context(), `insert into im_binding_codes(connection,member_id,token_hash,expires_at)
        values($1,$2,$3,now()+interval '10 minutes') on conflict(connection,member_id) do update
        set token_hash=excluded.token_hash,expires_at=excluded.expires_at`, s.connection(), member.ID, web.Hash(code)); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 201, map[string]string{"command": "绑定 " + code})
}

func (s Slack) unlink(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	ctx := r.Context()
	err := database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "delete from im_binding_codes where (connection,member_id)=($1,$2)", s.connection(), member.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from im_bindings where (connection,member_id)=($1,$2)", s.connection(), member.ID); err != nil {
			return err
		}
		return database.Audit(ctx, tx, member.ID, "channel.unlink", "slack", map[string]any{})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func verifySlackSignature(secret, stamp, signature string, body []byte, now time.Time) bool {
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || seconds < now.Unix()-300 || seconds > now.Unix()+300 || len(signature) != 67 || !strings.HasPrefix(signature, "v0=") {
		return false
	}
	actual, err := hex.DecodeString(signature[3:])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, "v0:"+stamp+":")
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), actual)
}

type slackEnvelope struct {
	Type, Challenge string
	Team            string `json:"team_id"`
	ContextTeam     string `json:"context_team_id"`
	App             string `json:"api_app_id"`
	ID              string `json:"event_id"`
	Shared          bool   `json:"is_ext_shared_channel"`
	Event           struct {
		Type, Subtype, Channel, User, Text, TS string
		ChannelType                            string `json:"channel_type"`
		Thread                                 string `json:"thread_ts"`
		Bot                                    string `json:"bot_id"`
		BotApp                                 string `json:"app_id"`
		UserTeam                               string `json:"user_team"`
	}
}

func (s Slack) event(w http.ResponseWriter, r *http.Request) error {
	if !s.configured() {
		return pgx.ErrNoRows
	}
	const maxEnvelope = 64 << 10
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEnvelope))
	if err != nil {
		return web.Fail(413, "Slack 事件超出接收范围")
	}
	if !verifySlackSignature(s.signing, r.Header.Get("X-Slack-Request-Timestamp"), r.Header.Get("X-Slack-Signature"), raw, time.Now()) {
		return web.Fail(403, "Slack 事件验证失败")
	}
	var envelope slackEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return web.Fail(422, "Slack 事件格式无效")
	}
	if envelope.Type == "url_verification" && envelope.Challenge != "" {
		return web.JSON(w, 200, map[string]string{"challenge": envelope.Challenge})
	}
	if envelope.Type != "event_callback" {
		w.WriteHeader(200)
		return nil
	}
	if envelope.Team != s.team || envelope.App != s.app || envelope.ContextTeam != "" && envelope.ContextTeam != s.team {
		return web.Fail(403, "Slack 工作区或应用不匹配")
	}
	m := envelope.Event
	if envelope.Shared || m.Type != "message" || m.ChannelType != "im" || m.Subtype != "" || m.Bot != "" || m.BotApp != "" ||
		(m.UserTeam != "" && m.UserTeam != s.team) || !strings.HasPrefix(m.Channel, "D") {
		w.WriteHeader(200)
		return nil
	}
	if !slackID.MatchString(envelope.ID) || !slackID.MatchString(m.Channel) || !slackID.MatchString(m.User) ||
		(!strings.HasPrefix(m.User, "U") && !strings.HasPrefix(m.User, "W")) || !slackTimestamp.MatchString(m.TS) {
		return web.Fail(422, "Slack 消息缺少有效身份或会话标识")
	}
	thread := m.Thread
	if thread == "" {
		thread = m.TS
	}
	if !slackTimestamp.MatchString(thread) {
		return web.Fail(422, "Slack 线程标识无效")
	}
	text := strings.TrimSpace(m.Text)
	if text == "" {
		w.WriteHeader(200)
		return nil
	}
	sealed, err := s.vault.Seal(text, s.eventScope(envelope.ID))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_, err = s.db.Exec(ctx, `insert into im_events(connection,event_id,chat_id,thread_id,message_id,external_user,binding_id,payload)
       values($1,$2,$3,$4,$5,$6,(select b.id from im_bindings b join members m on m.id=b.member_id
            and m.enabled and m.kind='person' where (b.connection,b.external_user)=($1,$6)),$7) on conflict do nothing`,
		s.connection(), envelope.ID, m.Channel, thread, m.TS, m.User, sealed)
	if err != nil {
		return err
	}
	w.WriteHeader(200)
	return nil
}

func (s Slack) eventScope(id string) string { return s.connection() + "/event/" + id }
