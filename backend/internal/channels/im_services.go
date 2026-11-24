package channels

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"alliance/internal/services"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const (
	imServiceCatalog = "service_catalog"
	imServicePreview = "service_preview"
)

func (s imChannel) serviceCommand(ctx context.Context, tx pgx.Tx, scope services.ChannelConversation, actor, verb, value string) (imReply, error) {
	switch verb {
	case "服务":
		if utf8.RuneCountInString(value) > 64 {
			return imReply{note: "服务搜索词请控制在 64 字内。"}, nil
		}
		return imReply{kind: imServiceCatalog, argument: value}, nil
	case "发起":
		service, goal, _ := strings.Cut(value, " ")
		goal = strings.TrimSpace(goal)
		if !uuidPattern.MatchString(service) || goal == "" || utf8.RuneCountInString(goal) > 1000 {
			return imReply{note: "请发送“发起 服务ID 工作目标”，目标限 1000 字。"}, nil
		}
		id, err := services.PrepareChannelLaunch(ctx, tx, s.vault, scope, actor, service, goal)
		if errors.Is(err, pgx.ErrNoRows) {
			err = web.Fail(404, "服务不可用，请重新查询“服务”。")
		}
		return imReply{kind: imServicePreview, argument: id}, err
	case "确认":
		if !uuidPattern.MatchString(value) {
			return imReply{note: "请在原会话使用当前服务预览中的确认指令。"}, nil
		}
		receipt, err := services.ConfirmChannelLaunch(ctx, tx, s.vault, scope, actor, value)
		if errors.Is(err, pgx.ErrNoRows) {
			err = web.Fail(404, "服务预览已失效或不属于当前会话。请重新查询“服务”。")
		}
		if err != nil {
			return imReply{}, err
		}
		return imReply{project: receipt.Project, kind: "project", note: "服务任务已受理。发送“进度”查看当前会话关联的项目，或打开原任务。"}, nil
	}
	return imReply{}, nil
}

func (s imChannel) serviceReply(ctx context.Context, tx pgx.Tx, job imJob, actor string, format imFormatter) (string, error) {
	if actor == "" {
		return "", pgx.ErrNoRows
	}
	arg, err := s.vault.Open(job.replyArgument, s.eventScope(job.id)+"/reply-argument")
	if err != nil {
		return "", err
	}
	if job.replyKind == imServiceCatalog {
		text, err := memberServiceCatalog(ctx, tx, actor, arg)
		return format(text, ""), err
	}
	scope := services.ChannelConversation{Binding: *job.binding, Chat: job.chat, Thread: job.thread}
	view, err := services.ReadChannelPreview(ctx, tx, s.vault, scope, actor, arg)
	var problem web.Problem
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return format("该预览已失效，请重新查询“服务”。", ""), nil
	case errors.As(err, &problem) && problem.Status < 500:
		return format(problem.Message, ""), nil
	case err != nil:
		return "", err
	}
	if view.Receipt != nil {
		return format("服务任务已受理。", s.public+"/#"+view.Receipt.Project), nil
	}
	text := fmt.Sprintf("%s · v%d\n任务：%s\n最多授权 %d 次调用，金额未封顶，实际费用由%s承担。最终确认人：%s。",
		view.Name, view.Version, view.Goal, view.Calls, view.Team, view.Reviewer)
	text += serviceDeliveryPreview(view.Delivery)
	if view.WebOnlyReason == "" {
		text += "\n同意后，十分钟内在本" + s.conversation + "发送：确认 " + arg + "\n也可在网页补充资料后确认同一任务。"
	} else {
		text += "\n" + view.WebOnlyReason
	}
	query := url.Values{"service": {view.Service}, "channelLaunch": {arg}}
	return format(text, s.public+"/?"+query.Encode()), nil
}
