package channels

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/policy"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type eventNotice struct{ ID, Channel, Phase, Project, Recipient, Member, Binding string }

func serveEventNotices(ctx context.Context, db *pgxpool.Pool, feishu Feishu, ding DingTalk) {
	lark, dingKey := feishu.CommentConnection(), ding.CommentConnection()
	if lark == "" && dingKey == "" {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := deliverEventNotice(ctx, db, lark, dingKey, func(ctx context.Context, notice eventNotice) commentResult {
			return sendEventNotice(ctx, feishu, ding, notice)
		}); err != nil && ctx.Err() == nil {
			slog.Warn("business event notice pending", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func sendEventNotice(ctx context.Context, feishu Feishu, ding DingTalk, notice eventNotice) commentResult {
	title := "业务事件复盘已启动"
	if notice.Phase == "completed" {
		title = "业务事件复盘已生成"
	}
	text := "请在团队空间查看事件依据、复盘结论和待确认事项。"
	if notice.Channel == "dingtalk" {
		return ding.privateActionCard(ctx, notice.Recipient, title, text, "查看团队复盘",
			strings.TrimRight(ding.public, "/")+"/chat/"+url.PathEscape(notice.Project))
	}
	token, err := feishu.accessToken(ctx)
	if err != nil {
		return commentResult{state: "pending"}
	}
	card, err := cardPayload(title, []cardElement{cardLine(text),
		cardLink("查看团队复盘", strings.TrimRight(feishu.Public, "/")+"/chat/"+url.PathEscape(notice.Project))})
	if err != nil {
		return commentResult{state: "failed"}
	}
	response, err := feishuPost(ctx, "im/v1/messages?receive_id_type=open_id", token, map[string]string{
		"receive_id": notice.Recipient, "uuid": notice.ID, "msg_type": "interactive", "content": card,
	})
	if err != nil {
		return commentResult{state: "unknown"}
	}
	data, _ := response["data"].(map[string]any)
	receipt, _ := data["message_id"].(string)
	if receipt == "" || len(receipt) > 256 {
		return commentResult{state: "unknown"}
	}
	return commentResult{state: "sent", receipt: receipt}
}

func deliverEventNotice(ctx context.Context, db *pgxpool.Pool, feishu, ding string, send func(context.Context, eventNotice) commentResult) error {
	var notice eventNotice
	err := db.QueryRow(ctx, `with expired as (
	  update event_trigger_notices set state='unknown' where state='sending' and next_at<=clock_timestamp()
	 ), next as (
	  select n.id,r.project_id,coalesce(f.open_id,d.external_user) recipient,coalesce(f.binding_id,d.id) binding
	  from event_trigger_notices n join event_trigger_batches b on b.id=n.batch_id join runs r on r.id=b.run_id
	  left join channel_links f on n.channel='feishu' and (f.member_id,f.tenant)=(n.member_id,$1)
	  left join im_bindings d on n.channel='dingtalk' and (d.member_id,d.connection)=(n.member_id,$2)
	  where n.state='pending' and n.next_at<=clock_timestamp() and coalesce(f.binding_id,d.id) is not null
	  order by n.next_at,n.id limit 1 for update of n skip locked
	 ) update event_trigger_notices n set state='sending',next_at=clock_timestamp()+interval '1 minute',attempts=n.attempts+1
	  from next where n.id=next.id returning n.id,n.channel,n.phase,next.project_id,next.recipient,n.member_id,next.binding`, feishu, ding).
		Scan(&notice.ID, &notice.Channel, &notice.Phase, &notice.Project, &notice.Recipient, &notice.Member, &notice.Binding)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var valid bool
		var attempts int
		err := tx.QueryRow(ctx, `select event_run_current(r.id) and coalesce(space_role(t.space_id,n.member_id) in ('space_admin','member'),false)
		 and exists(select from project_members where (project_id,member_id)=(r.project_id,n.member_id)),n.attempts
		 from event_trigger_notices n join event_trigger_batches b on b.id=n.batch_id join event_triggers t on t.id=b.trigger_id
		 join runs r on r.id=b.run_id where n.id=$1 and n.state='sending' for update of n`, notice.ID).Scan(&valid, &attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		result := commentResult{state: "revoked"}
		if valid {
			query, key := "select 1 from channel_links where (tenant,binding_id,member_id,open_id)=($1,$2,$3,$4) for share", feishu
			if notice.Channel == "dingtalk" {
				query, key = "select 1 from im_bindings where (connection,id,member_id,external_user)=($1,$2,$3,$4) for share", ding
			}
			err := tx.QueryRow(ctx, query, key, notice.Binding, notice.Member, notice.Recipient).Scan(new(int))
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				decision, err := policy.DecideTx(ctx, tx, policy.Request{Purpose: policy.Notification, Actor: notice.Member, Project: notice.Project})
				if err != nil {
					return err
				}
				if decision.Allowed {
					sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
					result = send(sendCtx, notice)
					cancel()
				}
			}
		}
		if result.state == "pending" && attempts >= 4 {
			result.state = "failed"
		}
		_, err = tx.Exec(ctx, `update event_trigger_notices set state=$2,receipt=nullif($3,''),next_at=clock_timestamp()+interval '30 seconds'
		 where id=$1 and state='sending'`, notice.ID, result.state, result.receipt)
		return err
	})
}
