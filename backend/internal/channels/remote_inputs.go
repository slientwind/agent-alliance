package channels

import (
	"alliance/internal/projects"
	"alliance/internal/resources"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const remoteInputPreview = "remote_input_preview"
const expiredRemoteInput = "远端补件预览已失效，请发送“进度”查看当前任务。"

type remoteInputProposal struct {
	project, inputID, status string
	target                   projects.RemoteInputTarget
	body                     []byte
}

func (f Feishu) proposeRemoteInput(ctx context.Context, tx pgx.Tx, actor, binding string, msg channelMessage, text string) (channelReply, error) {
	step := 0
	if strings.HasPrefix(text, "#") {
		number, rest, _ := strings.Cut(text[1:], " ")
		var err error
		step, err = strconv.Atoi(number)
		if err != nil || step < 1 || step > 12 {
			return channelReply{note: "请从“进度”核对步骤编号，然后发送“远端补件 #步骤号 补充文字”。"}, nil
		}
		text = rest
	}
	var project string
	var changed time.Time
	err := tx.QueryRow(ctx, `select c.project_id,c.changed_at from channel_contexts c
		join project_members pm on(pm.project_id,pm.member_id)=(c.project_id,c.member_id)
		where (c.tenant,c.chat_id,c.member_id)=($1,$2,$3) for share of c`,
		f.Tenant, msg.Chat, actor).Scan(&project, &changed)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: "请先用“切换 项目ID”打开有权处理的原任务。"}, nil
	}
	if err != nil {
		return channelReply{}, err
	}
	targets, err := projects.AvailableRemoteInputs(ctx, tx, actor, project)
	if err != nil {
		return channelReply{}, err
	}
	if len(targets) == 0 {
		return channelReply{project: project, note: "当前没有可由你补件的远端步骤，请核对原任务。"}, nil
	}
	var target projects.RemoteInputTarget
	for _, candidate := range targets {
		if step == 0 || step == candidate.Step {
			if target.Run != "" {
				return channelReply{project: project, note: "有多个待补件步骤，请按“进度”指定步骤号。同号的不同运行在网页分别处理。"}, nil
			}
			target = candidate
		}
	}
	if target.Run == "" {
		return channelReply{project: project, note: "此步骤当前不可补件，请发送“进度”刷新。"}, nil
	}
	input, err := resources.RemoteInput(text)
	if err != nil {
		return attachmentReply(err)
	}
	id := uuid.NewString()
	body, err := f.Vault.Seal(input, "feishu-remote-preview/"+id)
	if err != nil {
		return channelReply{}, err
	}
	tag, err := tx.Exec(ctx, `insert into channel_remote_input_proposals
		(id,tenant,chat_id,member_id,binding_id,context_changed_at,run_id,ordinal,control_revision,input_revision,origin_time,encrypted_body)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		on conflict(tenant,chat_id,member_id) do update set id=excluded.id,binding_id=excluded.binding_id,
		  context_changed_at=excluded.context_changed_at,run_id=excluded.run_id,ordinal=excluded.ordinal,
		  control_revision=excluded.control_revision,input_revision=excluded.input_revision,origin_time=excluded.origin_time,
		  encrypted_body=excluded.encrypted_body,input_id=null,expires_at=excluded.expires_at
		where channel_remote_input_proposals.origin_time<=excluded.origin_time`,
		id, f.Tenant, msg.Chat, actor, binding, changed, target.Run, target.Step, target.Control, target.InputRevision, sentAt(msg), body)
	if err != nil {
		return channelReply{}, err
	}
	if tag.RowsAffected() == 0 {
		return channelReply{note: "已有较新的补件预览，请核对最新消息。"}, nil
	}
	return channelReply{project: project, kind: remoteInputPreview, argument: id}, nil
}

func (f Feishu) readRemoteInputProposal(ctx context.Context, tx pgx.Tx, actor, binding, chat, id string, confirm bool) (remoteInputProposal, error) {
	var proposal remoteInputProposal
	if !uuidPattern.MatchString(id) {
		return proposal, pgx.ErrNoRows
	}
	query := `select r.project_id,p.run_id,p.ordinal,p.control_revision,p.input_revision,
		p.encrypted_body,coalesce(q.id::text,''),coalesce(q.state,'')
		from channel_remote_input_proposals p join runs r on r.id=p.run_id
		join channel_contexts c on(c.tenant,c.chat_id,c.member_id,c.project_id,c.changed_at)=
			(p.tenant,p.chat_id,p.member_id,r.project_id,p.context_changed_at)
		join project_members pm on(pm.project_id,pm.member_id)=(r.project_id,p.member_id)
		left join remote_task_inputs q on q.id=p.input_id
		where (p.id,p.tenant,p.chat_id,p.member_id,p.binding_id)=($1,$2,$3,$4,$5) and p.expires_at>now()`
	if confirm {
		query += " for update of p for share of c"
	}
	err := tx.QueryRow(ctx, query, id, f.Tenant, chat, actor, binding).Scan(&proposal.project, &proposal.target.Run, &proposal.target.Step,
		&proposal.target.Control, &proposal.target.InputRevision, &proposal.body, &proposal.inputID, &proposal.status)
	return proposal, err
}

func (f Feishu) remoteInputPreviewMessage(ctx context.Context, tx pgx.Tx, actor, binding, chat, id string) (string, error) {
	proposal, err := f.readRemoteInputProposal(ctx, tx, actor, binding, chat, id, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return expiredRemoteInput, nil
	}
	if err != nil {
		return "", err
	}
	if proposal.inputID != "" {
		return remoteInputState(proposal.status), nil
	}
	targets, err := projects.AvailableRemoteInputs(ctx, tx, actor, proposal.project)
	if err != nil {
		return "", err
	}
	for _, target := range targets {
		if target.Run == proposal.target.Run && target.Step == proposal.target.Step &&
			target.Control == proposal.target.Control && target.InputRevision == proposal.target.InputRevision {
			return fmt.Sprintf("步骤 %d · 将你刚发的补件文字交给 %s。\n本次只发送一次，费用待核算。请先在原任务核对问题与接收方。\n同意后在本单聊发送：确认远端补件 %s",
				target.Step, clipped(target.Destination, 100), id), nil
		}
	}
	return expiredRemoteInput, nil
}

func (f Feishu) confirmRemoteInput(ctx context.Context, tx pgx.Tx, actor, binding, chat, id string) (channelReply, error) {
	proposal, err := f.readRemoteInputProposal(ctx, tx, actor, binding, chat, id, true)
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: expiredRemoteInput}, nil
	}
	if err != nil {
		return channelReply{}, err
	}
	if proposal.inputID != "" {
		return channelReply{project: proposal.project, note: remoteInputState(proposal.status)}, nil
	}
	input, err := f.Vault.Open(proposal.body, "feishu-remote-preview/"+id)
	if err != nil {
		return channelReply{}, err
	}
	target := proposal.target
	var result projects.RemoteInputReceipt
	err = pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
		var queueErr error
		result, queueErr = projects.QueueRemoteInput(ctx, inner, f.Vault, actor, target.Run, target.Step, "feishu/remote-input/"+id, binding,
			projects.RemoteInputRequest{Expected: target.Control, InputRevision: target.InputRevision, Input: input, AuthorizeOneCall: true})
		if queueErr != nil {
			return queueErr
		}
		_, queueErr = inner.Exec(ctx, "update channel_remote_input_proposals set encrypted_body=null,input_id=$2 where id=$1", id, result.ID)
		return queueErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return channelReply{note: expiredRemoteInput}, nil
	}
	if err != nil {
		return attachmentReply(err)
	}
	return channelReply{project: proposal.project, note: remoteInputState(result.Status)}, nil
}

func remoteInputState(state string) string {
	switch state {
	case "queued":
		return "原任务补件已受理，等待发送。"
	case "submitted":
		return "正在向原任务发送补件。"
	case "received":
		return "合作方补件回执已记录。"
	case "uncertain":
		return "补件回执待核对，请在网页查看原任务。"
	case "rejected":
		return "本次补件已停止，请在网页核对授权或问题版本。"
	default:
		return ""
	}
}

func (f Feishu) remoteInputProgress(ctx context.Context, tx pgx.Tx, actor, project string) (string, error) {
	targets, err := projects.AvailableRemoteInputs(ctx, tx, actor, project)
	if err != nil {
		return "", err
	}
	if len(targets) > 0 {
		visible := min(len(targets), 8)
		steps := make([]string, 0, visible+1)
		for _, target := range targets[:visible] {
			steps = append(steps, fmt.Sprintf("步骤 %d", target.Step))
		}
		if len(targets) > visible {
			steps = append(steps, "其余步骤请在网页核对")
		}
		return "合作方等候补件：" + strings.Join(steps, "、") +
			"。在原任务核对问题后，可在本单聊发送“远端补件 #步骤号 补充文字”。", nil
	}
	var state string
	err = tx.QueryRow(ctx, `select q.state from remote_task_inputs q join runs r on r.id=q.run_id
		where r.project_id=$1 and q.actor=$2 and r.status='reconcile'
		order by q.created_at desc,q.id desc limit 1`, project, actor).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return remoteInputState(state), err
}
