package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/planner"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const taskCard = "task_card"

type cardElement = map[string]any

func plain(text string) cardElement {
	return cardElement{"tag": "plain_text", "content": text}
}

func cardLine(text string) cardElement {
	return cardElement{"tag": "div", "text": plain(text)}
}

func cardLink(label, target string) cardElement {
	return cardElement{
		"tag": "button", "text": plain(label),
		"behaviors": []cardElement{{"type": "open_url", "default_url": target}},
	}
}

func cardActionButton(ticket string) cardElement {
	return cardElement{
		"tag": "button", "text": plain("采纳这条回答"), "type": "primary",
		"confirm":   cardElement{"title": plain("确认采纳"), "text": plain("采纳卡片中显示的完整答复，并结束本次对话的确认。")},
		"behaviors": []cardElement{{"type": "callback", "value": cardElement{"adopt": ticket}}},
	}
}

func cardPayload(title string, elements []cardElement) (string, error) {
	data, err := json.Marshal(cardElement{
		"schema": "2.0",
		"config": cardElement{"enable_forward": false},
		"header": cardElement{"title": plain(title)},
		"body":   cardElement{"elements": elements},
	})
	return string(data), err
}

func taskLocation(task projects.ReviewTodo) (label, anchor string) {
	switch task.Kind {
	case "distribution":
		return "核对资料撤权影响", "delivery-distribution"
	case "deliver":
		return "最终确认交付", fmt.Sprintf("deliver-%d", task.Revision)
	case "coverage":
		return "核对必交项和缺项安排", "delivery-coverage"
	case "review":
		return fmt.Sprintf("审核成果 v%d", task.Version), fmt.Sprintf("review-%s-%d", task.Subject, task.Version)
	}
	switch task.Kind {
	case "suggested":
		label = "处理建议"
	case "required":
		label = "跟进修改"
	case "investigate":
		label = "核实验收问题"
	case "verification":
		label = "复核修改"
	default:
		return "查看项目", ""
	}
	return label, fmt.Sprintf("feedback-%s-%d-%d", task.Subject, task.Version, task.Revision)
}

func clipped(text string, limit int) string {
	chars := []rune(text)
	if len(chars) > limit {
		return string(chars[:limit]) + "…"
	}
	return text
}

func (f Feishu) sendTaskCard(ctx context.Context, tx pgx.Tx, reply, message, actor, project string, private bool, attempts int) error {
	token, err := f.accessToken(ctx)
	if err != nil {
		return recordReply(ctx, tx, reply, attempts, err)
	}
	title, elements, ticket, err := f.taskContent(ctx, tx, reply, actor, project, private)
	endpoint := "im/v1/messages/" + url.PathEscape(message) + "/reply"
	if errors.Is(err, pgx.ErrNoRows) {
		err = sendFeishuText(ctx, token, endpoint, reply, "原事项已失效。", "")
		return recordReply(ctx, tx, reply, attempts, err)
	}
	if err != nil {
		return err
	}
	content, err := cardPayload(title, elements)
	if err != nil {
		return err
	}
	response, err := feishuPost(ctx, endpoint, token, map[string]string{
		"msg_type": "interactive", "content": content, "uuid": reply,
	})
	if err == nil && ticket != "" {
		data, _ := response["data"].(map[string]any)
		sent, _ := data["message_id"].(string)
		if sent == "" {
			err = errors.New("Feishu did not confirm the card's message identifier")
		} else if _, writeErr := tx.Exec(ctx, "update channel_card_actions set message_id=$2 where id=$1", ticket, sent); writeErr != nil {
			return writeErr
		}
	}
	return recordReply(ctx, tx, reply, attempts, err)
}

func (f Feishu) taskContent(ctx context.Context, tx pgx.Tx, reply, actor, project string, private bool) (title string, elements []cardElement, ticket string, err error) {
	if !private && project == "" {
		return "", nil, "", pgx.ErrNoRows
	}
	title = "我的待办"
	stage := ""
	if project != "" {
		var name string
		err = tx.QueryRow(ctx, `select p.title,p.stage from projects p join project_members m on m.project_id=p.id
 where p.id=$1 and m.member_id=$2 for share of p`, project, actor).Scan(&name, &stage)
		if err != nil {
			return
		}
		if !private {
			return "任务卡", []cardElement{cardLink("打开企业工作区", f.Public+"/#"+project)}, "", nil
		}
		title = "任务卡"
		elements = append(elements, cardLine(clipped(name, 100)+" · "+stageName(stage)))
		if stage == "blocked" {
			var waiting []runReceipt
			waiting, err = waitingReceipts(ctx, tx, actor, project)
			if err != nil {
				return
			}
			if len(waiting) > 0 {
				elements = append(elements, cardLine("切换到此任务后，可在本单聊提交资料。"+receiptInstructions(waiting)))
			}
		}
		if stage == "brief" || stage == "plan" {
			var answer *cardElement
			answer, ticket, err = simpleAnswer(ctx, tx, reply, actor, project)
			if err != nil {
				return
			}
			if answer != nil {
				elements = append(elements, *answer, cardActionButton(ticket))
			}
		}
	}
	const visible = 8
	limit := visible + 1
	items, err := projects.ReviewTodos(ctx, tx, actor, project, &limit)
	if err != nil {
		return
	}
	for i, task := range items {
		if i == visible {
			break
		}
		label, anchor := taskLocation(task)
		text := clipped(task.Name, 100)
		if project == "" {
			text = clipped(task.Title, 65) + " · " + text
		}
		target := f.Public + "/#" + task.Project
		if anchor != "" {
			target = f.Public + "/?task=" + url.QueryEscape(anchor) + "#" + task.Project
		}
		elements = append(elements, cardLine(text), cardLink(label, target))
	}
	if project != "" {
		elements = append(elements, cardLink("打开项目", f.Public+"/#"+project))
	}
	if len(items) == 0 && project == "" {
		elements = append(elements, cardLine("当前没有成果待办。"))
	}
	elements = append(elements, cardLink("打开我的待办", f.Public+"/?inbox=review"))
	return
}

// Short answers without a selected source can be presented in full in a private card.
func simpleAnswer(ctx context.Context, tx pgx.Tx, reply, actor, project string) (*cardElement, string, error) {
	var text, request string
	var revision int
	err := tx.QueryRow(ctx, `select q.id,p.revision,q.result->>'text'
 from projects p join current_planning_requests q on q.project_id=p.id
 where p.id=$1 and p.owner=$2 and p.final_reviewer=$2 and q.actor=$2
 and q.project_revision=p.revision-1 and q.current_status='ready' and q.result->>'kind'='answer'
 and coalesce(q.input_sources,'[]'::jsonb)='[]'::jsonb and q.rule_pins=planning_rule_pins(p.id,$2)
 order by q.created_at desc,q.id limit 1`, project, actor).Scan(&request, &revision, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 320 ||
		strings.IndexFunc(text, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return nil, "", nil
	}
	var ticket string
	// A retry uses the same answer and token while they remain current. A
	// failed delivery can never repurpose an issued button for another answer.
	err = tx.QueryRow(ctx, `insert into channel_card_actions(reply_id,request_id,project_revision) values($1,$2,$3)
 on conflict(reply_id) do update set project_revision=excluded.project_revision
 where channel_card_actions.request_id=excluded.request_id and channel_card_actions.project_revision=excluded.project_revision
 and channel_card_actions.expires_at>clock_timestamp() and channel_card_actions.accepted_at is null returning id`, reply, request, revision).Scan(&ticket)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	answer := cardLine(text)
	return &answer, ticket, nil
}

type cardActionEvent struct {
	Operator struct {
		Open   string `json:"open_id"`
		Tenant string `json:"tenant_key"`
	}
	Host   string
	Action struct {
		Tag   string
		Value struct{ Adopt string }
	}
	Context struct {
		Chat    string `json:"open_chat_id"`
		Message string `json:"open_message_id"`
	}
}

func (f Feishu) cardAction(w http.ResponseWriter, r *http.Request, action cardActionEvent) error {
	toast := func(kind, content string) error {
		return web.JSON(w, 200, cardElement{"toast": cardElement{"type": kind, "content": content}})
	}
	if action.Host != "im_message" || action.Action.Tag != "button" || action.Operator.Tenant != f.Tenant ||
		action.Operator.Open == "" || action.Context.Chat == "" || action.Context.Message == "" ||
		!uuidPattern.MatchString(action.Action.Value.Adopt) {
		return toast("warning", "此操作已失效，请重新打开任务卡。")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var actor, project, request string
		var revision int
		var accepted bool
		err := tx.QueryRow(ctx, `select r.member_id,r.project_id,a.request_id,a.project_revision,a.accepted_at is not null
 from channel_card_actions a join channel_replies r on r.id=a.reply_id and r.sent_at is not null and r.private_chat
 join channel_links l on l.binding_id=r.binding_id and l.member_id=r.member_id and l.tenant=$2 and l.open_id=$3
 join members m on m.id=l.member_id and m.enabled
 join project_members pm on pm.project_id=r.project_id and pm.member_id=m.id
 where a.id=$1 and r.chat_id=$4 and a.message_id=$5 and a.expires_at>clock_timestamp() for update of a for share of l,m`,
			action.Action.Value.Adopt, f.Tenant, action.Operator.Open, action.Context.Chat, action.Context.Message).
			Scan(&actor, &project, &request, &revision, &accepted)
		if err != nil || accepted {
			return err
		}
		if err := planner.AdoptAnswer(ctx, tx, project, actor, request, revision); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "update channel_card_actions set accepted_at=clock_timestamp() where id=$1", action.Action.Value.Adopt)
		return err
	})
	var problem web.Problem
	if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) && problem.Status < 500 {
		return toast("warning", "当前答复或处理权限已更新，请重新打开项目核对。")
	}
	if err != nil {
		return toast("error", "暂时无法确认操作结果，请在企业工作区核对后重试。")
	}
	return toast("success", "已采纳这条回答。")
}
