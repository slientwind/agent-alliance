package channels

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// Dynamic replies resolve names, permissions and proposal expiry immediately before sending.
type channelReply struct{ project, note, kind, argument string }

func (f Feishu) handleCommand(ctx context.Context, tx pgx.Tx, actor, binding, event, text string, msg channelMessage) (channelReply, error) {
	chat, private := msg.Chat, msg.Type == "p2p"
	verb, value, _ := strings.Cut(text, " ")
	value = strings.TrimSpace(value)
	if !private && (verb == "服务" || verb == "发起" || verb == "确认" || verb == "反馈" || verb == "补件" || verb == "远端补件" || verb == "确认远端补件" || verb == "绑定") {
		return channelReply{note: "请在与机器人的单聊中查看服务、授权费用或绑定身份。"}, nil
	}
	switch verb {
	case "远端补件":
		return f.proposeRemoteInput(ctx, tx, actor, binding, msg, value)
	case "确认远端补件":
		return f.confirmRemoteInput(ctx, tx, actor, binding, chat, value)
	case "补件":
		return f.proposeRunReceipt(ctx, tx, actor, msg, value)
	case "反馈":
		return f.proposeFeedback(ctx, tx, actor, chat, value)
	case "任务卡":
		if value != "" && uuidPattern.MatchString(value) {
			var project string
			err := tx.QueryRow(ctx, "select project_id from project_members where member_id=$1 and project_id=$2",
				actor, value).Scan(&project)
			if errors.Is(err, pgx.ErrNoRows) {
				return channelReply{note: "对话不可用。"}, nil
			}
			return channelReply{project: project, kind: taskCard}, err
		}
		if !private || value != "" {
			return channelReply{note: "请发送“任务卡 项目ID”。"}, nil
		}
		fallthrough
	case "待办", "待办文字":
		project, note, err := f.command(ctx, tx, actor, chat, "待办")
		if !private {
			return channelReply{project: project, note: note}, err
		}
		kind := taskCard
		if verb == "待办文字" {
			kind = "待办"
		}
		return channelReply{project: project, note: note, kind: kind}, err
	case "服务":
		if utf8.RuneCountInString(value) > 64 {
			return channelReply{note: "服务搜索词请控制在 64 字内。"}, nil
		}
		return channelReply{kind: "catalog", argument: value}, nil
	case "发起":
		service, input, _ := strings.Cut(value, " ")
		input = strings.TrimSpace(input)
		if !uuidPattern.MatchString(service) || input == "" || utf8.RuneCountInString(input) > 1000 {
			return channelReply{note: "请输入“发起 服务ID 工作目标”，目标限 1000 字。"}, nil
		}
		var id string
		err := tx.QueryRow(ctx, `insert into channel_proposals(tenant,chat_id,member_id,service_id,version,calls,input,origin_message_id,origin_time,
    await_lookup_receipt,await_calculation_receipt)
   select $1,$2,$3,s.id,s.current_version,count(n.ordinal),$5,$6,$7,
    coalesce(bool_or(n.lookup_phase='answer'),false),coalesce(bool_or(n.lookup_phase='answer_calc'),false)
   from services s join team_members tm on tm.team_id=s.team_id and tm.member_id=$3
   join service_nodes n on (n.service_id,n.version)=(s.id,s.current_version)
   where s.id=$4 and s.enabled group by s.id`+replaceProposalSQL, f.Tenant, chat, actor, service, input, msg.ID, sentAt(msg)).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return channelReply{note: "服务不可用，请重新查询“服务”。"}, nil
		}
		return channelReply{kind: "proposal", argument: id}, err
	case "确认":
		return f.confirmProposal(ctx, tx, actor, binding, event, chat, value)
	case "进度":
		if private {
			var importing bool
			if err := tx.QueryRow(ctx, `select exists(select from channel_attachment_jobs j
 left join channel_contexts c on(c.tenant,c.chat_id,c.member_id)=(j.tenant,j.chat_id,j.member_id)
 where j.tenant=$1 and j.chat_id=$2 and j.member_id=$3 and j.binding_id=$4 and j.expires_at>clock_timestamp()
 and ((j.receipt_run is null and (c.changed_at is null or c.changed_at<j.accepted_at))
   or (j.receipt_run is not null and c.project_id=(select project_id from runs where id=j.receipt_run))))`, f.Tenant, chat, actor, binding).Scan(&importing); err != nil {
				return channelReply{}, err
			}
			if importing {
				return channelReply{note: "正在读取已确认的资料。受理后会在本单聊回复项目入口。"}, nil
			}
		}
		project, note, err := f.command(ctx, tx, actor, chat, text)
		reply := channelReply{project: project, note: note}
		if private && project != "" {
			reply.kind = "progress"
		}
		return reply, err
	default:
		project, note, err := f.command(ctx, tx, actor, chat, text)
		reply := channelReply{project: project, note: note}
		if private && project != "" && verb == "成果" {
			reply.kind = verb
		}
		return reply, err
	}
}

// Both previews occupy the same private chat slot. A new preview replaces
// the prior input and attachment together and invalidates its confirmation.
const replaceProposalSQL = `
 on conflict(tenant,chat_id,member_id) do update set id=gen_random_uuid(),service_id=excluded.service_id,version=excluded.version,
 calls=excluded.calls,input=excluded.input,expires_at=excluded.expires_at,origin_message_id=excluded.origin_message_id,origin_time=excluded.origin_time,
 await_lookup_receipt=excluded.await_lookup_receipt,await_calculation_receipt=excluded.await_calculation_receipt,
 receipt_run=excluded.receipt_run,receipt_step=excluded.receipt_step,receipt_revision=excluded.receipt_revision,receipt_kind=excluded.receipt_kind,
 receipt_question_revision=excluded.receipt_question_revision,
 file_message_id='',file_key='',file_name='',file_time=0
 where channel_proposals.origin_time<=excluded.origin_time returning id`

func sentAt(msg channelMessage) int64 {
	created, _ := strconv.ParseInt(msg.Created, 10, 64)
	if created <= 0 {
		return time.Now().UnixMilli()
	}
	return created
}

func (f Feishu) setProject(ctx context.Context, tx pgx.Tx, chat, actor, project string) error {
	_, err := tx.Exec(ctx, `insert into channel_contexts(tenant,chat_id,member_id,project_id) values($1,$2,$3,$4)
 on conflict(tenant,chat_id,member_id) do update set project_id=excluded.project_id,changed_at=excluded.changed_at`, f.Tenant, chat, actor, project)
	return err
}

func memberServiceCatalog(ctx context.Context, tx pgx.Tx, actor, arg string) (string, error) {
	rows, err := tx.Query(ctx, `select s.id,s.name,s.current_version from services s join team_members tm on tm.team_id=s.team_id
    where tm.member_id=$1 and s.enabled and strpos(lower(s.name),lower($2))>0 order by s.name,s.id limit 9`, actor, arg)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, name string
		var v int
		if err = rows.Scan(&id, &name, &v); err != nil {
			return "", err
		}
		if len(lines) == 8 {
			lines = append(lines, "更多结果请发送“服务 关键词”。")
			break
		}
		lines = append(lines, fmt.Sprintf("%s · v%d\n%s", name, v, id))
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "当前没有匹配的已发布服务。", nil
	}
	return strings.Join(lines, "\n") + "\n发送“发起 服务ID 工作目标”查看本次调用范围。", nil
}

func (f Feishu) serviceMessage(ctx context.Context, tx pgx.Tx, actor, kind, arg string) (string, error) {
	if kind == "catalog" {
		return memberServiceCatalog(ctx, tx, actor, arg)
	}
	var name, team, input, reviewer, brief, service, fileName string
	var version, calls int
	var needsSource, needsCSV, needsPictures, awaitLookup, awaitCalculation bool
	err := tx.QueryRow(ctx, `select s.name,t.name,p.input,r.display_name,v.instructions,s.id,p.version,p.calls,p.file_name,
 exists(select from service_nodes n where (n.service_id,n.version)=(s.id,p.version) and n.lookup_phase='search'),
 service_requires_csv_choice(s.id,p.version),
 exists(select from service_nodes n join agent_revisions a on (a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
   where (n.service_id,n.version)=(s.id,p.version) and agent_uses_picture(a)),
 p.await_lookup_receipt,p.await_calculation_receipt from channel_proposals p
 join services s on s.id=p.service_id and s.enabled and s.current_version=p.version join service_versions v on(v.service_id,v.version)=(s.id,p.version)
 join teams t on t.id=s.team_id join team_members tm on tm.team_id=t.id and tm.member_id=p.member_id
 join members r on r.id=v.reviewer and r.enabled join team_members rt on rt.team_id=t.id and rt.member_id=r.id
 where p.id=$1 and p.member_id=$2 and p.tenant=$3 and p.expires_at>now() and not exists(
 select from service_nodes n where(n.service_id,n.version)=(s.id,p.version) and not agent_admissible(n.agent_id,n.agent_revision,$2) union all select from service_tool_choices c where (c.service_id,c.version)=(s.id,p.version) and not agent_admissible(c.alternative,c.alternative_revision,$2))`, arg, actor, f.Tenant).Scan(&name, &team, &input, &reviewer, &brief, &service, &version, &calls, &fileName, &needsSource, &needsCSV, &needsPictures, &awaitLookup, &awaitCalculation)
	if errors.Is(err, pgx.ErrNoRows) {
		return "该预览已失效或服务暂不可用，请重新查询并发起。", nil
	}
	if err != nil {
		return "", err
	}
	if chars := []rune(brief); len(chars) > 300 {
		brief = string(chars[:300]) + "…"
	}
	limits := "一份不超过 96 KiB 的 UTF-8 TXT、Markdown、CSV 或 JSON 文件"
	if needsCSV {
		limits = "一份不超过 96 KiB 的 UTF-8 CSV 文件"
	}
	summary := fmt.Sprintf("%s · v%d\n任务：%s\n服务说明：%s\n最多授权 %d 次调用，金额未封顶，实际费用由%s承担。最终确认人：%s。\n查看完整服务：%s/?service=%s", name, version, input, brief, calls, team, reviewer, f.Public, service)
	if needsPictures {
		return summary + "\n此服务需要图片。请打开企业网页附上 PNG 或 JPEG 后确认。", nil
	}
	if awaitLookup || awaitCalculation {
		summary += "\n如果问答或核算缺少依据，任务会等待补件。可在本单聊发送“进度”继续，无需重新发起。"
	}
	if fileName == "" && (needsSource || needsCSV) {
		return summary + "\n请在本单聊发送" + limits + "。收到资料预览后再确认。也可打开企业网页附上要核对的资料。", nil
	}
	if fileName == "" {
		summary += "\n如需附资料，先在本单聊发送" + limits + "。"
	} else {
		summary += "\n本次资料：" + fileName + "。确认后从飞书读取原件并用于此任务。"
	}
	return summary + "\n同意后在本单聊发送：确认 " + arg, nil
}

func (f Feishu) proposeFeedback(ctx context.Context, tx pgx.Tx, actor, chat, value string) (channelReply, error) {
	artifact, rest, _ := strings.Cut(value, " ")
	token, body, _ := strings.Cut(strings.TrimSpace(rest), " ")
	body = strings.TrimSpace(body)
	version, err := strconv.Atoi(strings.TrimPrefix(token, "v"))
	if err != nil || !strings.HasPrefix(token, "v") || version < 1 || !uuidPattern.MatchString(artifact) || body == "" || utf8.RuneCountInString(body) > 4000 {
		return channelReply{note: "请按“反馈 成果ID v版本号 具体意见”发送，意见限 4000 字。发送“成果”查看当前版本。"}, nil
	}
	var project string
	err = tx.QueryRow(ctx, `select c.project_id from channel_contexts c join project_members pm on(pm.project_id,pm.member_id)=(c.project_id,c.member_id)
 where c.tenant=$1 and c.chat_id=$2 and c.member_id=$3 for share of c,pm`, f.Tenant, chat, actor).Scan(&project)
	if err == pgx.ErrNoRows {
		return channelReply{note: "请先切换至有权访问的项目。"}, nil
	}
	if err != nil {
		return channelReply{}, err
	}
	err = pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
		_, err := projects.ProposeRevision(ctx, inner, project, artifact, actor, version, "", body)
		return err
	})
	var p web.Problem
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: "当前项目没有该成果或已无权访问。"}, nil
	}
	if errors.As(err, &p) && p.Status < 500 {
		return channelReply{note: p.Message}, nil
	}
	return channelReply{project: project, note: fmt.Sprintf("已记录针对 v%d 的修改建议，可在网页查看审核人的处理结果。", version)}, err
}

func (f Feishu) reviewMessage(ctx context.Context, tx pgx.Tx, actor, project, kind string) (string, error) {
	var lines []string
	if kind == "待办" {
		limit := 9
		items, err := projects.ReviewTodos(ctx, tx, actor, project, &limit)
		if err != nil {
			return "", err
		}
		for i, t := range items {
			if i == 8 {
				lines = append(lines, "更多待办请在网页查看。")
				break
			}
			switch t.Kind {
			case "distribution":
				lines = append(lines, "交付分发受限 · 请在网页核对资料撤权影响")
			case "deliver":
				lines = append(lines, "待确认最终交付")
			case "coverage":
				lines = append(lines, "必交项和缺项安排待核对")
			case "review":
				lines = append(lines, fmt.Sprintf("待审核 · %s v%d", t.Name, t.Version))
			default:
				lines = append(lines, fmt.Sprintf("待处理反馈 · %s v%d", t.Name, t.Version))
			}
		}
		if len(lines) == 0 {
			return "本项目当前没有分配给你的成果审核待办。", nil
		}
	} else {
		rows, err := tx.Query(ctx, `select a.id,a.name,a.current_version,not artifact_rule_draft_readable(a.id,a.current_version)
   from artifacts a join project_members m on m.project_id=a.project_id
   where a.project_id=$1 and m.member_id=$2 order by a.ordinal,a.id limit 9`, project, actor)
		if err != nil {
			return "", err
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			var version int
			var needsRebuild bool
			if err = rows.Scan(&id, &name, &version, &needsRebuild); err != nil {
				return "", err
			}
			if len(lines) == 8 {
				lines = append(lines, "更多成果请在网页查看。")
				break
			}
			line := fmt.Sprintf("%s v%d · %s", name, version, id)
			if needsRebuild {
				line += " · 待重做，请在网页核对"
			}
			lines = append(lines, line)
		}
		if err = rows.Err(); err != nil {
			return "", err
		}
		if len(lines) == 0 {
			return "当前还没有成果。", nil
		}
	}
	return strings.Join(lines, "\n"), nil
}
