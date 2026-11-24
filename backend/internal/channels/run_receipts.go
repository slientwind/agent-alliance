package channels

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type runReceipt struct {
	run                      string
	step, revision, question int
	kind                     projects.ReceiptKind
}

const receiptContextSQL = ` and (receipt_run is null or exists(
 select from channel_contexts c join runs r on r.project_id=c.project_id
 where (c.tenant,c.chat_id,c.member_id)=(channel_proposals.tenant,channel_proposals.chat_id,channel_proposals.member_id)
 and r.id=channel_proposals.receipt_run))`

const eligibleReceiptSQL = `
 from runs r join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
 join current_source_receipt_requests cr on cr.run_id=r.id
 join project_members m on (m.project_id,m.member_id)=(p.id,r.actor)
 where r.actor=$1 and p.owner=$1 and r.app_id is null
 and ((cr.kind='lookup' and r.await_lookup_receipt) or (cr.kind='calculation' and r.await_calculation_receipt))`

func (r runReceipt) currentProject(ctx context.Context, tx pgx.Tx, actor string) (string, error) {
	var project string
	err := tx.QueryRow(ctx, `select p.id`+eligibleReceiptSQL+`
 and r.id=$2 and r.control_revision=$3 and cr.ordinal=$4 and cr.kind=$5 and coalesce(cr.request_revision,0)=$6`,
		actor, r.run, r.revision, r.step, r.kind, r.question).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", web.Fail(409, "此任务的待补件步骤已变化或不再可用")
	}
	if err != nil {
		return "", err
	}
	var granted bool
	if err := tx.QueryRow(ctx, identity.RunGrantSQL, r.run).Scan(&granted); err != nil {
		return "", err
	}
	if !granted {
		return "", web.Fail(403, "此任务的服务授权已失效")
	}
	return project, nil
}

func waitingReceipts(ctx context.Context, tx pgx.Tx, actor, project string) ([]runReceipt, error) {
	rows, err := tx.Query(ctx, `select r.id,cr.ordinal,r.control_revision,cr.kind,coalesce(cr.request_revision,0)`+eligibleReceiptSQL+
		` and p.id=$2 order by cr.ordinal`, actor, project)
	if err != nil {
		return nil, err
	}
	waiting, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (runReceipt, error) {
		var receipt runReceipt
		err := row.Scan(&receipt.run, &receipt.step, &receipt.revision, &receipt.kind, &receipt.question)
		return receipt, err
	})
	if err != nil || len(waiting) == 0 {
		return waiting, err
	}
	// All candidates belong to the live plan. Filter stale or revoked grants
	// before offering a private instruction that cannot be acted upon.
	available := waiting[:0]
	for _, receipt := range waiting {
		_, err := receipt.currentProject(ctx, tx, actor)
		var denied web.Problem
		if errors.As(err, &denied) && denied.Status < 500 {
			continue
		}
		if err != nil {
			return nil, err
		}
		available = append(available, receipt)
	}
	return available, nil
}

func (r runReceipt) title() string {
	if r.kind == projects.CalculationReceipt {
		return fmt.Sprintf("步骤 %d · 金额核算", r.step)
	}
	return fmt.Sprintf("步骤 %d · 原文问答", r.step)
}

func receiptInstructions(waiting []runReceipt) string {
	var lines []string
	for _, receipt := range waiting {
		lines = append(lines, receipt.title())
	}
	command := "补件"
	if len(waiting) > 1 {
		command = fmt.Sprintf("补件 #%d", waiting[0].step)
	}
	return "待补件：" + strings.Join(lines, "、") + "。发送“" + command + "”并上传原件。多段资料请在指令后加空格并附上要引用的原句。"
}

func stepWaitProgress(ctx context.Context, tx pgx.Tx, actor, project string) (string, error) {
	waiting, err := waitingReceipts(ctx, tx, actor, project)
	if err != nil {
		return "", err
	}
	var notes []string
	if len(waiting) != 0 {
		notes = append(notes, receiptInstructions(waiting))
	}
	rows, err := tx.Query(ctx, `select w.ordinal,w.deadline>statement_timestamp(),'index'
        from lookup_index_waits w join runs r on (r.id,r.plan_version)=(w.run_id,w.plan_version)
        join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
        join project_members pm on (pm.project_id,pm.member_id)=(r.project_id,$1)
        where r.project_id=$2 and r.status='running' and w.active and w.step_wait_revision is not null
        union all
        select w.ordinal,not w.rebuild_required,'rule' from step_rule_waits w
        join runs r on r.id=w.run_id join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
        join project_members pm on (pm.project_id,pm.member_id)=(p.id,$1)
        where p.id=$2 and r.status='running'
        union all
        select w.ordinal,w.step_scoped,case when agent_choice_profile(a)='text' then 'agent' else 'tool' end
        from active_tool_choice_waits w
        join runs r on r.id=w.run_id join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
        join nodes n on(n.project_id,n.plan_version,n.ordinal)=(r.project_id,r.plan_version,w.ordinal)
        join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
        join project_members pm on (pm.project_id,pm.member_id)=(p.id,$1)
        where p.id=$2 order by 1`, actor, project)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var step int
		var resumable bool
		var kind string
		if err := rows.Scan(&step, &resumable, &kind); err != nil {
			return "", err
		}
		switch {
		case kind == "tool" || kind == "agent":
			label := "工具"
			if kind == "agent" {
				label = "文本 Agent"
			}
			note := fmt.Sprintf("步骤 %d 待人工选择%s。", step, label)
			if resumable {
				note += "其他就绪步骤可继续。"
			}
			notes = append(notes, note+"打开原项目查看候选。")
		case kind == "rule" && resumable:
			notes = append(notes, fmt.Sprintf("步骤 %d 的团队规则待核对。打开原项目查看可用版本。", step))
		case kind == "rule":
			notes = append(notes, fmt.Sprintf("步骤 %d 的规则依据已失效。打开原项目查看需要重做的工作。", step))
		case resumable:
			notes = append(notes, fmt.Sprintf("步骤 %d 正在等待资料索引，其他就绪步骤继续。", step))
		default:
			notes = append(notes, fmt.Sprintf("步骤 %d 的自动等待已结束。打开原项目重新核对资料。", step))
		}
	}
	return strings.Join(notes, "\n"), rows.Err()
}

func (f Feishu) proposeRunReceipt(ctx context.Context, tx pgx.Tx, actor string, msg channelMessage, text string) (channelReply, error) {
	step := 0
	if strings.HasPrefix(text, "#") {
		number, rest, _ := strings.Cut(text[1:], " ")
		var err error
		step, err = strconv.Atoi(number)
		if err != nil || step < 1 {
			return channelReply{note: "请发送“进度”查看步骤编号，例如“补件 #2 原句”。"}, nil
		}
		text = strings.TrimSpace(rest)
	}
	if utf8.RuneCountInString(text) > 320 || strings.ContainsAny(text, "\r\n") {
		return channelReply{note: "请提供不超过 320 字的连续原句，或在企业网页选择引用位置。"}, nil
	}
	var project string
	err := tx.QueryRow(ctx, `select project_id from channel_contexts where tenant=$1 and chat_id=$2 and member_id=$3`,
		f.Tenant, msg.Chat, actor).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: "请先用“切换 项目ID”打开要补件的任务。"}, nil
	}
	if err != nil {
		return channelReply{}, err
	}
	waiting, err := waitingReceipts(ctx, tx, actor, project)
	if err != nil {
		return channelReply{}, err
	}
	if len(waiting) == 0 {
		return channelReply{project: project, note: "当前没有可由你在飞书补件的步骤。请在企业网页核对任务。"}, nil
	}
	if len(waiting) > 1 && step == 0 {
		return channelReply{project: project, note: receiptInstructions(waiting)}, nil
	}
	var target runReceipt
	for _, receipt := range waiting {
		if step == 0 || step == receipt.step {
			target = receipt
			break
		}
	}
	if target.run == "" {
		return channelReply{project: project, note: receiptInstructions(waiting)}, nil
	}
	var id string
	err = tx.QueryRow(ctx, `insert into channel_proposals(tenant,chat_id,member_id,input,origin_message_id,origin_time,
 receipt_run,receipt_step,receipt_revision,receipt_kind,receipt_question_revision) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,nullif($11,0))`+replaceProposalSQL,
		f.Tenant, msg.Chat, actor, text, msg.ID, sentAt(msg), target.run, target.step, target.revision, target.kind, target.question).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: "该指令早于当前预览，请查看最新消息后继续。"}, nil
	}
	return channelReply{kind: "receipt_proposal", argument: id}, err
}

func (f Feishu) runReceiptMessage(ctx context.Context, tx pgx.Tx, actor, chat, id string) (string, error) {
	var target runReceipt
	var excerpt, file string
	err := tx.QueryRow(ctx, `select receipt_run,receipt_step,receipt_revision,receipt_kind,coalesce(receipt_question_revision,0),input,file_name
 from channel_proposals where tenant=$1 and chat_id=$2 and member_id=$3 and id=$4 and receipt_run is not null and expires_at>now()`+receiptContextSQL,
		f.Tenant, chat, actor, id).Scan(&target.run, &target.step, &target.revision, &target.kind, &target.question, &excerpt, &file)
	if errors.Is(err, pgx.ErrNoRows) {
		return "该补件预览已失效，请发送“进度”查看当前任务。", nil
	}
	if err != nil {
		return "", err
	}
	project, err := target.currentProject(ctx, tx, actor)
	if err != nil {
		reply, err := attachmentReply(err)
		return reply.note, err
	}
	var title string
	if err := tx.QueryRow(ctx, "select title from projects where id=$1", project).Scan(&title); err != nil {
		return "", err
	}
	note := title + " · " + target.title() + "\n仅继续原任务，使用剩余调用授权。\n查看任务：" + f.Public + "/#" + project
	if excerpt == "" {
		note += "\n按只有一处可引用内容的原件处理。"
	} else {
		note += "\n本次选用的原句：" + excerpt
	}
	if file == "" {
		return note + "\n请在本单聊发送一份不超过 96 KiB 的 UTF-8 TXT、Markdown、CSV 或 JSON 原件。", nil
	}
	return note + "\n本次资料：" + file + "。确认后从飞书读取原件。\n同意后在本单聊发送：确认 " + id, nil
}
