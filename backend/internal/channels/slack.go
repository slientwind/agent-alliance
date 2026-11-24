package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Slack struct {
	imChannel
	team, app, signing, bot string
	endpoint                string
	client                  *http.Client
}

func NewSlack(db *pgxpool.Pool, vault resources.Vault) Slack {
	team, app := os.Getenv("SLACK_TEAM_ID"), os.Getenv("SLACK_APP_ID")
	return Slack{imChannel: imChannel{db: db, vault: vault, key: "slack/" + team + "/" + app, provider: "slack", name: "Slack", conversation: " Slack 线程",
		public: strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")}, team: team, app: app, signing: os.Getenv("SLACK_SIGNING_SECRET"),
		bot: os.Getenv("SLACK_BOT_TOKEN"), endpoint: "https://slack.com/api/chat.postMessage", client: newIMHTTPClient()}
}

var slackID = regexp.MustCompile(`^[A-Za-z0-9]{2,64}$`)
var slackTimestamp = regexp.MustCompile(`^[0-9]{1,16}\.[0-9]{1,12}$`)

func (s Slack) configured() bool {
	return s.publicConfigured() && slackID.MatchString(s.team) && strings.HasPrefix(s.team, "T") && slackID.MatchString(s.app) &&
		strings.HasPrefix(s.app, "A") && s.signing != "" && s.bot != ""
}

func (s Slack) Routes(m *http.ServeMux, g identity.Server) {
	s.bindingRoutes(m, g, s.configured())
	m.Handle("POST /api/channels/slack/events", web.Handler(s.event))
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
	if err := s.accept(r.Context(), imMessage{event: envelope.ID, chat: m.Channel, thread: thread, message: m.TS, sender: m.User, text: text}); err != nil {
		return err
	}
	w.WriteHeader(200)
	return nil
}
