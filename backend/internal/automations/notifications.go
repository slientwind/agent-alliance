package automations

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"alliance/internal/channels"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Notifications struct {
	DB     *pgxpool.Pool
	Feishu func(context.Context, string, string, string, string) (string, error)
}

type notificationView struct {
	ID         string     `json:"id"`
	Channel    string     `json:"channel"`
	State      string     `json:"state"`
	Revision   int        `json:"revision"`
	AcceptedAt *time.Time `json:"acceptedAt,omitempty"`
	Receipt    string     `json:"receipt,omitempty"`
}

func enqueueNotifications(ctx context.Context, tx pgx.Tx, id, occurrence string) error {
	_, err := tx.Exec(ctx, `insert into automation_notifications(schedule_id,occurrence,channel)
		select r.schedule_id,r.occurrence,'feishu' from automation_runs r join automation_schedules a on a.id=r.schedule_id
		where (r.schedule_id,r.occurrence)=($1,$2) and r.state='completed' and r.notify_feishu=a.notify_feishu and a.state<>'deleted'
		on conflict do nothing`, id, occurrence)
	return err
}

func (n Notifications) Serve(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := n.deliverNext(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("automation notification processing pending")
			}
		}
	}
}

func (n Notifications) deliverNext(ctx context.Context) error {
	var id, channel string
	var revision int
	err := database.WithTx(ctx, n.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update automation_notifications set state='unknown',revision=revision+1,lease_until=null
			where id in (select id from automation_notifications where state='sending' and lease_until<=now()
			order by lease_until limit 30 for update skip locked)`); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `update automation_notifications n set state='sending',revision=revision+1,
			attempts=attempts+1,lease_until=now()+interval '45 seconds'
			where n.id=(select id from automation_notifications where state='pending' order by queued_at,id
				limit 1 for update skip locked) returning id,channel,revision`).Scan(&id, &channel, &revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || id == "" {
		return err
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	outcome, receipt := "failed", ""
	if channel == "feishu" && n.Feishu != nil {
		var actor, binding, project string
		err = n.DB.QueryRow(short, "select member,binding,project from automation_feishu_destination($1)", id).
			Scan(&actor, &binding, &project)
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = "cancelled"
		} else if err != nil {
			return err
		} else {
			var sendErr error
			receipt, sendErr = n.Feishu(short, id, actor, binding, project)
			outcome = deliveryState(receipt, sendErr)
		}
	}
	_, err = n.DB.Exec(ctx, `update automation_notifications set state=$3,revision=revision+1,lease_until=null,
		receipt=case when $3='accepted' then $4 else '' end,accepted_at=case when $3='accepted' then now() end
		where id=$1 and revision=$2 and state='sending'`, id, revision, outcome, receipt)
	return err
}

func deliveryState(receipt string, err error) string {
	switch {
	case receipt != "":
		return "accepted"
	case errors.Is(err, channels.ErrNoticeRevoked):
		return "cancelled"
	case errors.Is(err, channels.ErrNoticeUnknown), err == nil:
		return "unknown"
	default:
		return "failed"
	}
}

func (s Server) notificationChannels(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	binding, err := s.Feishu.AutomationBinding(r.Context(), s.DB, actor.ID)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]bool{"feishu": binding != ""})
}

func (s Server) selectedFeishu(ctx context.Context, tx pgx.Tx, actor string, choice *bool, prior *string) (*string, error) {
	if choice == nil {
		return prior, nil
	}
	if !*choice {
		return nil, nil
	}
	binding, err := s.Feishu.AutomationBinding(ctx, tx, actor)
	if err != nil {
		return nil, err
	}
	if binding == "" {
		return nil, web.Fail(409, "请先在工作区绑定飞书账号")
	}
	return &binding, nil
}

func (s Server) retryNotification(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id, notice := r.PathValue("id"), r.PathValue("notice")
	for _, raw := range []string{id, notice} {
		if _, err := uuid.Parse(raw); err != nil {
			return web.Fail(404, "找不到这项通知")
		}
	}
	var in struct{ Expected int }
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update automation_notifications set state='pending',queued_at=now(),revision=revision+1
			where schedule_id=$1 and id=$2 and revision=$3 and state='failed'
			 and exists(select from automation_feishu_destination($2) where member=$4)`, id, notice, in.Expected, actor.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "这项通知已更新或无法补发")
		}
		return database.Audit(ctx, tx, actor.ID, "automation.notification_retry", notice, map[string]string{"schedule": id})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
