package channels

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type slackJob struct {
	id, chat, thread, sender string
	binding, project         *string
	text                     string
	sealed                   []byte
	replyKind                string
	attempts                 int
}

type slackReply struct{ project, kind, note string }

const slackThreadGuide = "发送“新建 工作目标”开始，或发送“切换 项目ID”接续原项目。后续在同一 Slack 线程发送“进度”“待办”或“补充 内容”。"

func (s Slack) boundMember(ctx context.Context, tx pgx.Tx, job slackJob) (string, error) {
	var actor string
	err := tx.QueryRow(ctx, `select m.id from im_bindings b join members m on m.id=b.member_id
        where (b.connection,b.external_user,b.id)=($1,$2,$3) and m.enabled and m.kind='person' and m.departed_at is null
        for share of b,m`, s.connection(), job.sender, job.binding).Scan(&actor)
	return actor, err
}

func (s Slack) bind(ctx context.Context, tx pgx.Tx, job *slackJob, code string) (slackReply, error) {
	var member string
	err := tx.QueryRow(ctx, `delete from im_binding_codes where connection=$1 and token_hash=$2 and expires_at>now()
        returning member_id`, s.connection(), web.Hash(code)).Scan(&member)
	if errors.Is(err, pgx.ErrNoRows) {
		return slackReply{note: "绑定码已失效，请到企业网页重新领取。"}, nil
	}
	if err != nil {
		return slackReply{}, err
	}
	err = tx.QueryRow(ctx, `insert into im_bindings(connection,external_user,member_id)
        select $1,$2,m.id from members m where m.id=$3 and m.enabled and m.kind='person' and m.departed_at is null
        for share of m on conflict do nothing returning id::text`, s.connection(), job.sender, member).Scan(&job.binding)
	if errors.Is(err, pgx.ErrNoRows) {
		return slackReply{note: "当前无法绑定，请在企业网页核对账号状态。"}, nil
	}
	if err != nil {
		return slackReply{}, err
	}
	if err := database.Audit(ctx, tx, member, "channel.bind", "slack", map[string]any{}); err != nil {
		return slackReply{}, err
	}
	return slackReply{note: "Slack 身份已绑定。\n" + slackThreadGuide}, nil
}

func (s Slack) command(ctx context.Context, tx pgx.Tx, job *slackJob) (slackReply, error) {
	verb, value, _ := strings.Cut(job.text, " ")
	value = strings.TrimSpace(value)
	if job.binding == nil {
		if verb == "绑定" && value != "" {
			return s.bind(ctx, tx, job, value)
		}
		return slackReply{note: "请从企业网页的「账号设置 → Slack 账号绑定」获取绑定码，并在单聊发送绑定指令。"}, nil
	}
	actor, err := s.boundMember(ctx, tx, *job)
	if err != nil {
		return slackReply{}, err
	}
	if verb == "绑定" {
		return slackReply{note: "Slack 身份已绑定。"}, nil
	}
	var project string
	if verb == "新建" || verb == "切换" {
		if verb == "新建" {
			if value == "" || utf8.RuneCountInString(value) > 16000 {
				return slackReply{note: "请发送“新建 工作目标”，目标限 16000 字。"}, nil
			}
			project, err = projects.CreateProjectDraft(ctx, tx, actor, "", value)
		} else {
			if !uuidPattern.MatchString(value) {
				return slackReply{note: "请发送“切换 项目ID”。"}, nil
			}
			project, err = slackReadableProject(ctx, tx, value, actor)
		}
		if err != nil {
			return slackReply{}, err
		}
		_, err = tx.Exec(ctx, `insert into im_threads(binding_id,chat_id,thread_id,project_id) values($1,$2,$3,$4)
           on conflict(binding_id,chat_id,thread_id) do update set project_id=excluded.project_id`, job.binding, job.chat, job.thread, project)
		return slackReply{project: project, kind: "project", note: "已关联此 Slack 线程。\n在此线程回复“进度”“待办”或“补充 内容”。"}, err
	}
	err = tx.QueryRow(ctx, `select t.project_id from im_threads t join project_members p on(p.project_id,p.member_id)=(t.project_id,$4)
	       where (t.binding_id,t.chat_id,t.thread_id)=($1,$2,$3)`, job.binding, job.chat, job.thread, actor).Scan(&project)
	if errors.Is(err, pgx.ErrNoRows) {
		return slackReply{note: slackThreadGuide}, nil
	}
	if err != nil {
		return slackReply{}, err
	}
	switch verb {
	case "进度":
		return slackReply{project: project, kind: "progress"}, nil
	case "待办":
		return slackReply{project: project, kind: "todos"}, nil
	case "补充":
		if value == "" || utf8.RuneCountInString(value) > 16000 {
			return slackReply{note: "请发送“补充 内容”，消息限 16000 字。"}, nil
		}
		err := projects.AppendProjectMessage(ctx, tx, project, actor, value)
		return slackReply{project: project, kind: "project", note: "消息已加入原项目对话。任务所需原件及正式补件可在企业网页提交。"}, err
	case "复查":
		return s.recheck(ctx, tx, job.id, actor, project, value)
	default:
		return slackReply{project: project, kind: "project", note: "请发送“进度”“待办”或“补充 内容”。"}, nil
	}
}

func slackReadableProject(ctx context.Context, tx pgx.Tx, target, actor string) (string, error) {
	var project string
	err := tx.QueryRow(ctx, `select project_id from project_members where (project_id,member_id)=($1,$2) for share`, target, actor).Scan(&project)
	return project, err
}

func (s Slack) recheck(ctx context.Context, tx pgx.Tx, event, actor, project, text string) (slackReply, error) {
	parts := strings.Fields(text)
	if len(parts) != 4 || !uuidPattern.MatchString(parts[0]) {
		return slackReply{note: "请发送“待办”，并使用当前步骤给出的复查指令。"}, nil
	}
	step, stepErr := strconv.Atoi(parts[1])
	wait, waitErr := strconv.Atoi(parts[2])
	control, controlErr := strconv.Atoi(parts[3])
	if stepErr != nil || waitErr != nil || controlErr != nil {
		return slackReply{note: "请发送“待办”，并使用当前步骤给出的复查指令。"}, nil
	}
	var linked bool
	if err := tx.QueryRow(ctx, "select exists(select from runs where (id,project_id)=($1,$2))", parts[0], project).Scan(&linked); err != nil {
		return slackReply{}, err
	}
	if !linked {
		return slackReply{note: "该待办已失效或不属于本线程的项目。"}, nil
	}
	key := "slack/" + hex.EncodeToString(web.Hash(s.connection()+"/"+event))
	err := projects.RecheckStepDispatch(ctx, tx, parts[0], actor, key,
		projects.DispatchRecheckInput{Step: step, Expected: control, WaitRevision: wait})
	return slackReply{project: project, kind: "project", note: "已提交步骤复查。发送“进度”查看当前结果。"}, err
}

func slackProjectReply(ctx context.Context, tx pgx.Tx, actor, project, kind string) (string, error) {
	var stage, title string
	if err := tx.QueryRow(ctx, "select stage,title from projects where id=$1", project).Scan(&stage, &title); err != nil {
		return "", err
	}
	stages := map[string]string{"brief": "待制定方案", "plan": "待确认方案", "queued": "已排队", "running": "执行中", "paused": "暂停", "blocked": "等待处理", "reconcile": "待核对执行回执", "review": "审核中", "delivered": "已交付", "cancelled": "已取消", "failed": "执行失败"}
	label := stages[stage]
	if label == "" {
		label = "请打开项目查看当前状态"
	}
	lines := []string{title + " · " + label}
	if kind == "progress" {
		waits, err := waitingReceipts(ctx, tx, actor, project)
		if err != nil {
			return "", err
		}
		for _, wait := range waits {
			lines = append(lines, wait.title()+"待补件。请打开原任务选择资料。")
		}
		lines, err = appendStepWaitNotes(ctx, tx, actor, project, lines)
		return strings.Join(lines, "\n"), err
	}
	limit := 8
	todos, err := projects.ReviewTodos(ctx, tx, actor, project, &limit)
	if err != nil {
		return "", err
	}
	for _, todo := range todos {
		switch todo.Kind {
		case "distribution":
			lines = append(lines, "交付分发待核对")
		case "deliver":
			lines = append(lines, "待确认交付")
		case "coverage":
			lines = append(lines, "待核对必交项和缺项安排")
		default:
			lines = append(lines, fmt.Sprintf("%s v%d · 待处理", todo.Name, todo.Version))
		}
	}
	rows, err := tx.Query(ctx, `select r.id,w.ordinal,w.wait_revision,r.control_revision from current_step_dispatch_waits w
        join runs r on r.id=w.run_id join projects p on p.id=r.project_id
        join project_members pm on(pm.project_id,pm.member_id)=(p.id,$2)
        where p.id=$1 and (p.owner=$2 or (r.app_id is not null and r.delegated_by is null and p.final_reviewer=$2 and pm.can_review))
        order by r.created_at desc,w.ordinal limit 12`, project, actor)
	if err != nil {
		return "", err
	}
	type dispatchWait struct {
		Run                 string
		Step, Wait, Control int
	}
	waits, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dispatchWait])
	if err != nil {
		return "", err
	}
	for _, wait := range waits {
		var granted bool
		if err := tx.QueryRow(ctx, identity.RunGrantSQL, wait.Run).Scan(&granted); err != nil {
			return "", err
		}
		if granted {
			lines = append(lines, fmt.Sprintf("步骤 %d 待核对执行条件。处理后在本线程发送：\n复查 %s %d %d %d",
				wait.Step, wait.Run, wait.Step, wait.Wait, wait.Control))
		}
	}
	if len(lines) == 1 {
		lines = append(lines, "当前没有需在此处理的待办。")
	}
	return strings.Join(lines, "\n"), nil
}
