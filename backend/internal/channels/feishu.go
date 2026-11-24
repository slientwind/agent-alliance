package channels

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Feishu struct {
	DB                                                *pgxpool.Pool
	Vault                                             resources.Vault
	App, Secret, Encrypt, Verify, Tenant, Bot, Public string
}

func NewFeishu(db *pgxpool.Pool, vault resources.Vault) Feishu {
	return Feishu{DB: db, Vault: vault, App: os.Getenv("FEISHU_APP_ID"), Secret: os.Getenv("FEISHU_APP_SECRET"),
		Encrypt: os.Getenv("FEISHU_ENCRYPT_KEY"), Verify: os.Getenv("FEISHU_VERIFICATION_TOKEN"),
		Tenant: os.Getenv("FEISHU_TENANT_KEY"), Bot: os.Getenv("FEISHU_BOT_OPEN_ID"),
		Public: strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/")}
}
func (f Feishu) enabled() bool {
	return f.App != "" && f.Secret != "" && f.Encrypt != "" && f.Verify != "" && f.Tenant != "" && strings.HasPrefix(f.Public, "https://")
}
func (f Feishu) Routes(m *http.ServeMux, g identity.Server) {
	m.Handle("GET /api/admin/channels/feishu/review-health", g.Protect(true, f.reminderHealth))
	m.Handle("GET /api/channels/feishu/review-reminders", g.Protect(false, f.reminderPreference))
	m.Handle("PUT /api/channels/feishu/review-reminders", g.Protect(false, f.reminderPreference))
	m.Handle("POST /api/channels/feishu/review-reminders/retry", g.Protect(false, f.retryReminder))
	m.Handle("GET /api/channels/feishu/bind", g.Protect(false, f.bindingStatus))
	m.Handle("POST /api/channels/feishu/bind", g.Protect(false, f.code))
	m.Handle("DELETE /api/channels/feishu/bind", g.Protect(false, f.unlink))
	m.Handle("POST /api/channels/feishu/events", web.Handler(f.event))
}
func (f Feishu) bindingStatus(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var bound bool
	if err := f.DB.QueryRow(r.Context(), "select exists(select from channel_links where tenant=$1 and member_id=$2)", f.Tenant, u.ID).Scan(&bound); err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]bool{"configured": f.enabled(), "bound": bound})
}
func (f Feishu) code(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	if !f.enabled() {
		return web.Fail(409, "实例尚未配置飞书应用")
	}
	token := web.Token()
	_, err := f.DB.Exec(r.Context(), "insert into channel_codes values($1,$2,now()+interval '10 minutes')", web.Hash(token), u.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"command": "绑定 " + token})
}
func (f Feishu) unlink(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		for _, q := range []string{"delete from channel_codes where member_id=$1", "delete from channel_contexts where member_id=$1", "delete from channel_links where member_id=$1"} {
			if _, err := tx.Exec(ctx, q, u.ID); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "channel.unlink", "feishu", map[string]any{})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

type incoming struct {
	Type, Challenge, Token string
	Header                 struct {
		ID     string `json:"event_id"`
		Type   string `json:"event_type"`
		Token  string
		App    string `json:"app_id"`
		Tenant string `json:"tenant_key"`
	}
	Event struct {
		Sender struct {
			ID struct {
				Open string `json:"open_id"`
			} `json:"sender_id"`
			Type string `json:"sender_type"`
		}
		Message channelMessage
		cardActionEvent
	}
}

type channelMessage struct {
	ID       string `json:"message_id"`
	Chat     string `json:"chat_id"`
	Type     string `json:"chat_type"`
	Format   string `json:"message_type"`
	Parent   string `json:"parent_id"`
	Root     string `json:"root_id"`
	Created  string `json:"create_time"`
	Content  string
	Mentions []struct {
		Key string
		ID  struct {
			Open string `json:"open_id"`
		}
	}
}

func (f Feishu) event(w http.ResponseWriter, r *http.Request) error {
	if !f.enabled() {
		return pgx.ErrNoRows
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope struct{ Encrypt string }
	if json.Unmarshal(raw, &envelope) != nil {
		return web.Fail(422, "无效事件")
	}
	data, err := base64.StdEncoding.DecodeString(envelope.Encrypt)
	if err != nil || len(data) < 32 || len(data)%16 != 0 {
		return web.Fail(403, "事件验证失败")
	}
	ts := r.Header.Get("X-Lark-Request-Timestamp")
	nonce := r.Header.Get("X-Lark-Request-Nonce")
	sig := r.Header.Get("X-Lark-Signature")
	// Feishu challenges may omit the signature. Only the challenge route accepts that variant.
	signed := false
	if ts != "" && nonce != "" {
		n, e := strconv.ParseInt(ts, 10, 64)
		delta := time.Now().Unix() - n
		sum := sha256.Sum256(append([]byte(ts+nonce+f.Encrypt), raw...))
		signed = e == nil && delta >= -300 && delta <= 300 && subtle.ConstantTimeCompare([]byte(sig), []byte(hex.EncodeToString(sum[:]))) == 1
	}
	key := sha256.Sum256([]byte(f.Encrypt))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	cipher.NewCBCDecrypter(block, data[:16]).CryptBlocks(data[16:], data[16:])
	b := data[16:]
	start, end := bytes.IndexByte(b, '{'), bytes.LastIndexByte(b, '}')
	var ev incoming
	if start < 0 || end < start || json.Unmarshal(b[start:end+1], &ev) != nil {
		return web.Fail(403, "事件验证失败")
	}
	same := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	if ev.Type == "url_verification" && ev.Challenge != "" && same(ev.Token, f.Verify) {
		return web.JSON(w, 200, map[string]string{"challenge": ev.Challenge})
	}
	if !signed || !same(ev.Header.Token, f.Verify) || ev.Header.App != f.App || ev.Header.Tenant != f.Tenant || ev.Header.ID == "" {
		return web.Fail(403, "事件验证失败")
	}
	if ev.Header.Type == "card.action.trigger" {
		return f.cardAction(w, r, ev.Event.cardActionEvent)
	}
	msg := ev.Event.Message
	private := msg.Type == "p2p"
	isFile := private && msg.Format == "file"
	if ev.Header.Type != "im.message.receive_v1" || ev.Event.Sender.Type != "user" || (msg.Format != "text" && !isFile) {
		return web.JSON(w, 200, map[string]int{"code": 0})
	}
	var content struct{ Text string }
	if !isFile && json.Unmarshal([]byte(msg.Content), &content) != nil {
		return web.Fail(422, "无效消息")
	}
	if !private {
		mentioned := false
		for _, v := range msg.Mentions {
			if f.Bot != "" && v.ID.Open == f.Bot {
				content.Text = strings.ReplaceAll(content.Text, v.Key, "")
				mentioned = true
			}
		}
		if !mentioned {
			return web.JSON(w, 200, map[string]int{"code": 0})
		}
	}
	if msg.Chat == "" || msg.ID == "" || ev.Event.Sender.ID.Open == "" {
		return web.Fail(422, "消息缺少身份或会话标识")
	}
	ctx := r.Context()
	err = database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "insert into channel_events(id,tenant,message_id) values($1,$2,$3) on conflict do nothing", ev.Header.ID, f.Tenant, msg.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		text := strings.TrimSpace(content.Text)
		who := ev.Event.Sender.ID.Open
		var actor, binding string
		var reply channelReply
		err = tx.QueryRow(ctx, "select m.id,l.binding_id from members m join channel_links l on l.member_id=m.id where l.tenant=$1 and l.open_id=$2 and m.enabled for share of m,l", f.Tenant, who).Scan(&actor, &binding)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if isFile {
			if actor == "" {
				return nil
			}
			reply, err = f.stageFile(ctx, tx, actor, msg)
			if err != nil {
				return err
			}
		} else if strings.HasPrefix(text, "绑定 ") && private && actor == "" {
			var member string
			err := tx.QueryRow(ctx, "delete from channel_codes where token_hash=$1 and expires_at>now() returning member_id", web.Hash(strings.TrimSpace(strings.TrimPrefix(text, "绑定 ")))).Scan(&member)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
			if err == nil {
				err := tx.QueryRow(ctx, "insert into channel_links(tenant,open_id,member_id) select $1,$2,id from members where id=$3 and enabled on conflict do nothing returning binding_id", f.Tenant, who, member).Scan(&binding)
				if err != nil && err != pgx.ErrNoRows {
					return err
				}
				if err == nil {
					actor = member
					reply.note = "平台成员身份已绑定。"
				}
			}
			if reply.note == "" {
				reply.note = "绑定凭证失效或已有绑定，请在网页重新核对。"
			}
		} else if strings.HasPrefix(text, "绑定 ") && actor != "" {
			reply.note = "当前账号已绑定。更换绑定请先在企业网页解除原绑定。"
		} else if actor == "" {
			reply.note = "请在企业网页的我的工作区获取绑定码，并在单聊中提交。"
		} else {
			reply, err = f.handleCommand(ctx, tx, actor, binding, ev.Header.ID, text, msg)
			if err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `insert into channel_replies(event_id,message_id,member_id,project_id,note,binding_id,private_chat,reply_kind,argument,chat_id)
 values($1,$2,nullif($3,'')::uuid,nullif($4,'')::uuid,$5,nullif($6,'')::uuid,$7,$8,$9,$10)`, ev.Header.ID, msg.ID, actor, reply.project, reply.note, binding, private, reply.kind, reply.argument, msg.Chat)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]int{"code": 0})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (f Feishu) command(ctx context.Context, tx pgx.Tx, actor, chat, text string) (string, string, error) {
	verb, value, _ := strings.Cut(text, " ")
	value = strings.TrimSpace(value)
	var project string
	switch verb {
	case "新建":
		if value == "" {
			return "", "请输入“新建 工作目标”。", nil
		}
		title := []rune(value)
		if len(title) > 55 {
			title = title[:55]
		}
		if err := tx.QueryRow(ctx, "insert into projects(owner,final_reviewer,team_id,title) select $1,$1,default_team,$2 from enterprise returning id", actor, string(title)).Scan(&project); err != nil {
			return "", "", err
		}
		if _, err := tx.Exec(ctx, "insert into project_memberships values($1,$2,true)", project, actor); err != nil {
			return "", "", err
		}
		if err := projects.Note(ctx, tx, project, "user", value, actor); err != nil {
			return "", "", err
		}
	case "切换":
		if !uuidPattern.MatchString(value) {
			return "", "请输入网页对话链接中的项目 ID。", nil
		}
		err := tx.QueryRow(ctx, "select project_id from project_members where member_id=$1 and project_id=$2", actor, value).Scan(&project)
		if err == pgx.ErrNoRows {
			return "", "对话不可用。", nil
		}
		if err != nil {
			return "", "", err
		}
	default:
		err := tx.QueryRow(ctx, "select c.project_id from channel_contexts c join project_members m on(m.project_id,m.member_id)=(c.project_id,c.member_id) where tenant=$1 and chat_id=$2 and c.member_id=$3", f.Tenant, chat, actor).Scan(&project)
		if err == pgx.ErrNoRows {
			return "", "发送“服务”查看团队服务，或使用“新建 工作目标”“切换 项目ID”。", nil
		}
		if err != nil {
			return "", "", err
		}
		if verb != "进度" && verb != "待办" && verb != "成果" {
			if err := projects.Note(ctx, tx, project, "user", text, actor); err != nil {
				return "", "", err
			}
			if _, err := tx.Exec(ctx, "update projects set revision=revision+1,updated_at=now() where id=$1", project); err != nil {
				return "", "", err
			}
		}
		return project, "请打开企业工作区查看并处理当前事项。", nil
	}
	return project, "请打开企业工作区查看并处理当前事项。", f.setProject(ctx, tx, chat, actor, project)
}
func (f Feishu) Notify(ctx context.Context) {
	if !f.enabled() {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			_ = database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
				var id, message, note, kind, arg, chat string
				var private bool
				var actor, project, binding *string
				var attempts int
				if _, err := tx.Exec(ctx, "delete from channel_proposals where expires_at<=now()"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "delete from channel_remote_input_proposals where tenant=$1 and expires_at<=now()", f.Tenant); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, "delete from channel_card_actions where expires_at<=now()"); err != nil {
					return err
				}
				err := tx.QueryRow(ctx, "select id,message_id,note,member_id,project_id,attempts,binding_id,private_chat,reply_kind,argument,chat_id from channel_replies where sent_at is null and suppressed_at is null and next_at<=now() and attempts<6 and reply_kind<>'attachment_pending' order by next_at limit 1 for update skip locked").Scan(&id, &message, &note, &actor, &project, &attempts, &binding, &private, &kind, &arg, &chat)
				if err == pgx.ErrNoRows {
					return nil
				}
				if err != nil {
					return err
				}
				if actor != nil {
					var linked bool
					if err := tx.QueryRow(ctx, "select exists(select from channel_links l join members m on m.id=l.member_id and m.enabled where tenant=$1 and member_id=$2 and binding_id=$3 for share of l,m)", f.Tenant, *actor, binding).Scan(&linked); err != nil {
						return err
					}
					if !linked {
						_, err = tx.Exec(ctx, "update channel_replies set suppressed_at=now() where id=$1", id)
						return err
					}
				}
				if kind == taskCard && actor != nil && chat != "" {
					projectID := ""
					if project != nil {
						projectID = *project
					}
					return f.sendTaskCard(ctx, tx, id, message, *actor, projectID, private, attempts)
				}
				if private && actor != nil && (kind == "catalog" || kind == "proposal") {
					note, err = f.serviceMessage(ctx, tx, *actor, kind, arg)
					if err != nil {
						return err
					}
				}
				if private && actor != nil && kind == "receipt_proposal" {
					note, err = f.runReceiptMessage(ctx, tx, *actor, chat, arg)
					if err != nil {
						return err
					}
				}
				if private && actor != nil && binding != nil && kind == remoteInputPreview {
					note, err = f.remoteInputPreviewMessage(ctx, tx, *actor, *binding, chat, arg)
					if err != nil {
						return err
					}
				}
				if project != nil && actor != nil {
					var stage string
					var total, reviewed, takeover int
					err := tx.QueryRow(ctx, `select p.stage,count(a.id),count(r.artifact_id),count(a.id) filter(where takeover_review_needed(p.id,a.id,a.current_version)) from projects p join project_members m on m.project_id=p.id left join artifacts a on a.project_id=p.id
 left join reviews r on(r.artifact_id,r.version)=(a.id,a.current_version) where m.member_id=$1 and p.id=$2 group by p.id`, *actor, *project).Scan(&stage, &total, &reviewed, &takeover)
					if err == pgx.ErrNoRows {
						note = "原事项已失效。"
					} else if err != nil {
						return err
					} else {
						if private {
							if kind == "成果" || kind == "待办" {
								note, err = f.reviewMessage(ctx, tx, *actor, *project, kind)
								if err != nil {
									return err
								}
								if stage == "review" && kind == "成果" {
									note += "\n发送“反馈 成果ID v版本号 具体意见”提出修改建议。"
								}
							}
							if kind == "progress" && (stage == "blocked" || stage == "running") {
								progress, err := stepWaitProgress(ctx, tx, *actor, *project)
								if err != nil {
									return err
								}
								if progress != "" {
									note = progress
								}
							}
							if kind == "progress" && (stage == "reconcile" || stage == "review") {
								remote, err := f.remoteInputProgress(ctx, tx, *actor, *project)
								if err != nil {
									return err
								}
								if remote != "" {
									note = remote
								}
							}
							if takeover > 0 {
								note = fmt.Sprintf("接管人还需核对 %d 份当前版本。\n", takeover) + note
							}
							note = "当前状态 · " + stageName(stage) + fmt.Sprintf(" · 已审核 %d/%d 份当前成果\n", reviewed, total) + note
						} else {
							note = "请成员登录企业工作区查看当前事项。"
						}
						note += "\n" + f.Public + "/#" + *project
					}
				}
				return recordReply(ctx, tx, id, attempts, f.reply(ctx, id, message, note))
			})
		}
	}
}
func stageName(stage string) string {
	switch stage {
	case "brief":
		return "待完善需求"
	case "plan":
		return "待确认方案"
	case "running":
		return "执行中"
	case "review":
		return "待人工审核"
	case "delivered":
		return "已交付"
	case "adopted":
		return "已采纳"
	case "blocked":
		return "需处理阻塞"
	case "reconcile":
		return "等待核对"
	case "failed":
		return "执行失败"
	case "cancelled":
		return "已取消"
	case "paused":
		return "已暂停"
	}
	return stage
}

func recordReply(ctx context.Context, tx pgx.Tx, id string, attempts int, deliveryErr error) error {
	if deliveryErr != nil {
		_, err := tx.Exec(ctx, "update channel_replies set attempts=attempts+1,next_at=now()+make_interval(secs=>$1) where id=$2", float64(uint64(1)<<uint(attempts))*10, id)
		return err
	}
	_, err := tx.Exec(ctx, "update channel_replies set sent_at=now() where id=$1", id)
	return err
}

var feishuOrigin = "https://open.feishu.cn"

func feishuClient() http.Client {
	return http.Client{Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (f Feishu) reply(ctx context.Context, id, message, text string) error {
	token, err := f.accessToken(ctx)
	if err != nil {
		return err
	}
	return sendFeishuText(ctx, token, "im/v1/messages/"+url.PathEscape(message)+"/reply", id, text, "")
}
func feishuPost(ctx context.Context, path, token string, payload any) (map[string]any, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", feishuOrigin+"/open-apis/"+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	cl := feishuClient()
	rsp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer rsp.Body.Close()
	var val map[string]any
	if rsp.StatusCode != 200 || json.NewDecoder(io.LimitReader(rsp.Body, 1<<20)).Decode(&val) != nil || val["code"] != float64(0) {
		return nil, fmt.Errorf("飞书消息服务未确认")
	}
	return val, nil
}
func (f Feishu) accessToken(ctx context.Context) (string, error) {
	auth, err := feishuPost(ctx, "auth/v3/tenant_access_token/internal", "", map[string]string{"app_id": f.App, "app_secret": f.Secret})
	if err != nil {
		return "", err
	}
	token, _ := auth["tenant_access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("飞书授权无效")
	}
	return token, nil
}
func sendFeishuText(ctx context.Context, token, endpoint, id, text, recipient string) error {
	content, _ := json.Marshal(map[string]string{"text": text})
	payload := map[string]string{"msg_type": "text", "content": string(content), "uuid": id}
	if recipient != "" {
		payload["receive_id"] = recipient
	}
	_, err := feishuPost(ctx, endpoint, token, payload)
	return err
}
