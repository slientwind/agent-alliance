package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"alliance/internal/identity"
	"alliance/internal/resources"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DingTalk struct {
	imChannel
	corp, app, secret, robot string
	mutationTemplate         string
	client                   *http.Client
	dialer                   websocket.Dialer
}

func NewDingTalk(db *pgxpool.Pool, vault resources.Vault) DingTalk {
	corp, app := os.Getenv("DINGTALK_CORP_ID"), os.Getenv("DINGTALK_CLIENT_ID")
	robot := os.Getenv("DINGTALK_ROBOT_CODE")
	if robot == "" {
		robot = app
	}
	return DingTalk{imChannel: imChannel{db: db, vault: vault, key: "dingtalk/" + corp + "/" + app,
		provider: "dingtalk", name: "钉钉", conversation: "钉钉单聊", public: strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")},
		corp: corp, app: app, secret: os.Getenv("DINGTALK_CLIENT_SECRET"), robot: robot, client: newIMHTTPClient(), dialer: *websocket.DefaultDialer,
		mutationTemplate: strings.TrimSpace(os.Getenv("DINGTALK_MUTATION_TEMPLATE_ID"))}
}

var dingConfigID = regexp.MustCompile(`^[A-Za-z0-9_-]{2,128}$`)
var dingOpaqueID = regexp.MustCompile(`^[!-~]{1,512}$`)

func (d DingTalk) configured() bool {
	return d.publicConfigured() && dingConfigID.MatchString(d.corp) && dingConfigID.MatchString(d.app) &&
		dingConfigID.MatchString(d.robot) && d.secret != ""
}

func (d DingTalk) Routes(mux *http.ServeMux, identity identity.Server) {
	d.bindingRoutes(mux, identity, d.configured())
}

func (d DingTalk) Serve(ctx context.Context) {
	if !d.configured() {
		return
	}
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		d.serve(ctx, d.postReply, dingFormat)
	}()
	d.listen(ctx)
	<-stopped
}

type dingRobotMessage struct {
	MessageID        string `json:"msgId"`
	MessageType      string `json:"msgtype"`
	ConversationID   string `json:"conversationId"`
	ConversationType string `json:"conversationType"`
	Sender           string `json:"senderStaffId"`
	SenderCorp       string `json:"senderCorpId"`
	BotCorp          string `json:"chatbotCorpId"`
	Robot            string `json:"robotCode"`
	ReplyURL         string `json:"sessionWebhook"`
	ReplyExpiry      int64  `json:"sessionWebhookExpiredTime"`
	Text             struct{ Content string }
}

func (d DingTalk) acceptCallback(ctx context.Context, event, payload string) error {
	var msg dingRobotMessage
	if json.Unmarshal([]byte(payload), &msg) != nil || msg.ConversationType != "1" || msg.MessageType != "text" ||
		msg.SenderCorp != d.corp || msg.BotCorp != d.corp || msg.Robot != d.robot || !dingOpaqueID.MatchString(msg.ConversationID) ||
		!dingOpaqueID.MatchString(msg.Sender) || !dingOpaqueID.MatchString(msg.MessageID) {
		return nil
	}
	expires := time.UnixMilli(msg.ReplyExpiry)
	text := strings.TrimSpace(msg.Text.Content)
	if text == "" || !expires.After(time.Now()) || !validDingReplyURL(msg.ReplyURL) {
		return nil
	}
	// DingTalk supplies one conversation ID for a member's single chat.
	return d.accept(ctx, imMessage{event: event, chat: msg.ConversationID, thread: msg.ConversationID,
		message: msg.MessageID, sender: msg.Sender, text: text, replyURL: msg.ReplyURL, replyExpiry: &expires})
}

func validDingReplyURL(raw string) bool {
	if len(raw) > 4096 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "oapi.dingtalk.com") ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil || u.Fragment != "" || u.EscapedPath() != "/robot/sendBySession" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	return err == nil && len(q) == 1 && len(q["session"]) == 1 && q.Get("session") != ""
}

func dingFormat(text, link string) string {
	if link == "" {
		return text
	}
	return text + "\n打开企业网页中的原任务\n" + link
}

func (d DingTalk) postReply(ctx context.Context, job imJob, text string) (string, time.Duration) {
	if job.replyExpiry == nil || !job.replyExpiry.After(time.Now()) {
		return "suppressed", 0
	}
	target, err := d.vault.Open(job.replyTarget, d.replyScope(job.id))
	if err != nil || !validDingReplyURL(target) {
		return "failed", 0
	}
	payload, err := json.Marshal(map[string]any{"msgtype": "text", "text": map[string]string{"content": text}})
	if err != nil {
		return "failed", 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return "failed", 0
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return "unknown", 0
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return imRateLimited(res.Header.Get("Retry-After"), job.attempts)
	}
	if res.StatusCode >= 500 {
		return "unknown", 0
	}
	if res.StatusCode != http.StatusOK {
		return "failed", 0
	}
	var result struct {
		ErrorCode *int `json:"errcode"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&result) != nil || result.ErrorCode == nil {
		return "unknown", 0
	}
	if *result.ErrorCode != 0 {
		return "failed", 0
	}
	return "sent", 0
}
