package channels

import (
	"context"
	"errors"
	"net/url"

	"alliance/internal/database"
	"github.com/jackc/pgx/v5"
)

var (
	ErrNoticeRevoked = errors.New("notification destination revoked")
	ErrNoticeUnknown = errors.New("notification delivery unknown")
)

func (f Feishu) AutomationBinding(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actor string) (string, error) {
	if !f.enabled() {
		return "", nil
	}
	var binding string
	err := q.QueryRow(ctx, `select binding_id from channel_links where tenant=$1 and member_id=$2`, f.Tenant, actor).Scan(&binding)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return binding, err
}

func (f Feishu) SendAutomationNotice(ctx context.Context, notice, actor, binding, project string) (string, error) {
	if !f.enabled() {
		return "", errors.New("notification channel unavailable")
	}
	token, err := f.accessToken(ctx)
	if err != nil {
		return "", err
	}
	var receipt string
	err = database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var recipient string
		err := tx.QueryRow(ctx, `select l.open_id from automation_feishu_destination($1) d join channel_links l
			on (l.binding_id,l.member_id)=(d.binding,d.member) where (d.member,d.binding,d.project,l.tenant)=($2,$3,$4,$5)
			for share of l`, notice, actor, binding, project, f.Tenant).Scan(&recipient)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoticeRevoked
		}
		if err != nil {
			return err
		}
		response, err := postFeishuText(ctx, token, "im/v1/messages?receive_id_type=open_id", notice,
			"你的定时委托已完成。登录企业工作区查看结果。\n"+f.Public+"/chat/"+url.PathEscape(project), recipient)
		if err != nil {
			var transport *url.Error
			if errors.As(err, &transport) || errors.Is(err, errFeishuUnconfirmed) {
				return ErrNoticeUnknown
			}
			return err
		}
		data, _ := response["data"].(map[string]any)
		receipt, _ = data["message_id"].(string)
		if receipt == "" || len(receipt) > 256 {
			return ErrNoticeUnknown
		}
		return nil
	})
	return receipt, err
}
