package channels

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// Match the exact binding, responsibility, version and reminder phase again when
// retrying. Only a generic authenticated inbox link leaves the instance.
const currentReminderItems = `select from review_notification_items i
 join review_reminder_targets t using(clock,clock_revision,phase)
 where i.batch=n.id and i.superseded_at is null and t.recipient=n.recipient`

func (f Feishu) reminderPreference(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	if r.Method == "PUT" {
		var in struct {
			Binding string
			Enabled *bool
		}
		if err := web.Read(r, &in); err != nil {
			return err
		}
		if in.Enabled == nil {
			return web.Fail(422, "请明确提醒设置")
		}
		err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, "update channel_links set review_reminders=$1 where tenant=$2 and member_id=$3 and binding_id=$4", *in.Enabled, f.Tenant, u.ID, in.Binding)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return web.Fail(409, "飞书绑定已变化，请刷新")
			}
			if !*in.Enabled {
				if _, err = tx.Exec(ctx, "update review_notifications set status='obsolete' where binding=$1 and status in ('pending','failed')", in.Binding); err != nil {
					return err
				}
			}
			return database.Audit(ctx, tx, u.ID, "channel.review_reminders", in.Binding, map[string]bool{"enabled": *in.Enabled})
		})
		if err != nil {
			return err
		}
		w.WriteHeader(204)
		return nil
	}
	var binding string
	var enabled bool
	var latest, failed *string
	err := f.DB.QueryRow(ctx, `select l.binding_id,l.review_reminders,
      (select status from review_notifications where binding=l.binding_id order by created_at desc,id desc limit 1),
      (select n.id from review_notifications n where n.binding=l.binding_id and n.status='failed' and exists(`+currentReminderItems+`) order by created_at desc limit 1)
      from channel_links l where l.member_id=$1 and tenant=$2`, u.ID, f.Tenant).Scan(&binding, &enabled, &latest, &failed)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"binding": binding, "enabled": enabled, "latest": latest, "retry": failed})
}

func (f Feishu) retryReminder(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Batch string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var binding string
		err := tx.QueryRow(ctx, "select binding_id from channel_links where tenant=$1 and member_id=$2 and review_reminders for update", f.Tenant, u.ID).Scan(&binding)
		if err != nil {
			return err
		}
		var batch string
		err = tx.QueryRow(ctx, `insert into review_notifications(tenant,binding,recipient,retry_of)
            select n.tenant,n.binding,n.recipient,n.id from review_notifications n where n.id=$1 and n.binding=$2 and n.status='failed'
            and exists(`+currentReminderItems+`) returning id`, in.Batch, binding).Scan(&batch)
		if err == pgx.ErrNoRows {
			return web.Fail(409, "事项已变化，请刷新后查看")
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into review_notification_items(batch,clock,clock_revision,phase) select $1,i.clock,i.clock_revision,i.phase
          from review_notification_items i join review_reminder_targets t using(clock,clock_revision,phase) where i.batch=$2 and i.superseded_at is null and t.recipient=$3`, batch, in.Batch, u.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update review_notifications set status='obsolete' where id=$1", in.Batch); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "channel.review_retry", batch, map[string]string{"previous": in.Batch})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(202)
	return nil
}

func (f Feishu) ReviewMonitor(ctx context.Context) {
	scan := time.NewTicker(10 * time.Second)
	defer scan.Stop()
	dispatch := time.NewTicker(time.Second)
	defer dispatch.Stop()
	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-scan.C:
			err = (projects.Server{DB: f.DB}).SyncReviewClocks(ctx)
			if err == nil && f.enabled() {
				err = f.queueReviews(ctx)
			}
		case <-dispatch.C:
			if f.enabled() {
				err = f.dispatchReview(ctx)
			}
		}
		if err != nil && ctx.Err() == nil {
			slog.Error("review reminders", "error", err)
		}
	}
}

func (f Feishu) queueReviews(ctx context.Context) error {
	return database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var leader bool
		if err := tx.QueryRow(ctx, "select pg_try_advisory_xact_lock(17952179)").Scan(&leader); err != nil || !leader {
			return err
		}
		// One digest per current recipient and binding, even when several tasks
		// resume together. Failed phases require explicit resubmission.
		_, err := tx.Exec(ctx, `with eligible as materialized (
          select l.tenant,l.binding_id,l.member_id,t.clock,t.clock_revision,t.phase
          from channel_links l join review_reminder_targets t on t.recipient=l.member_id
          where l.tenant=$1 and l.review_reminders and not exists(
            select from review_notification_items i join review_notifications n on n.id=i.batch
            where n.binding=l.binding_id and i.superseded_at is null and n.status in('pending','accepted','failed') and
             (i.clock,i.clock_revision,i.phase)=(t.clock,t.clock_revision,t.phase))
        ), batches as (
          insert into review_notifications(tenant,binding,recipient)
          select distinct tenant,binding_id,member_id from eligible
          on conflict(binding) where status='pending' do nothing returning id,binding
        ) insert into review_notification_items(batch,clock,clock_revision,phase)
          select b.id,e.clock,e.clock_revision,e.phase from batches b join eligible e on e.binding_id=b.binding`, f.Tenant)
		return err
	})
}

func (f Feishu) dispatchReview(ctx context.Context) error {
	var id string
	var attempt int
	// Commit the attempt and lease BEFORE making an external request. After a
	// process restart the next attempt reuses this digest's provider UUID.
	err := database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var exhausted bool
		err := tx.QueryRow(ctx, `select id,attempts>=3 or coalesce(first_attempt<now()-interval '55 minutes',false)
           from review_notifications where tenant=$1 and status='pending' and next_at<=now()
           order by next_at,id limit 1 for update skip locked`, f.Tenant).Scan(&id, &exhausted)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if exhausted {
			_, err = tx.Exec(ctx, `update review_notifications n set status=case when exists(`+currentReminderItems+`) then 'failed' else 'obsolete' end,
               last_error='渠道未确认接收' where id=$1`, id)
			id = ""
			return err
		}
		err = tx.QueryRow(ctx, `update review_notifications set attempts=attempts+1,first_attempt=coalesce(first_attempt,now()),
           next_at=now()+interval '20 seconds' where id=$1 returning attempts`, id).Scan(&attempt)
		return err
	})
	if err != nil || id == "" {
		return err
	}
	token, authErr := f.accessToken(ctx)
	return database.WithTx(ctx, f.DB, func(tx pgx.Tx) error {
		var who string
		// Match the lock order used by opt-out and explicit retry.
		err := tx.QueryRow(ctx, `select l.open_id from channel_links l join review_notifications n on n.binding=l.binding_id
           join members m on m.id=l.member_id and m.enabled where n.id=$1 and n.tenant=l.tenant and n.recipient=l.member_id and l.review_reminders for share of l,m`, id).Scan(&who)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		var owned bool
		err = tx.QueryRow(ctx, "select true from review_notifications where id=$1 and status='pending' and attempts=$2 for update", id, attempt).Scan(&owned)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		var active bool
		err = tx.QueryRow(ctx, `with current_items as materialized (
             select i.clock from review_notifications n join review_notification_items i on n.id=i.batch
             join review_reminder_targets t using(clock,clock_revision,phase)
             where n.id=$1 and t.recipient=n.recipient and i.superseded_at is null
           ), retired as (
             update review_notification_items i set superseded_at=now() where i.batch=$1 and i.superseded_at is null
             and not exists(select from current_items c where c.clock=i.clock)
           ) select exists(select from current_items)`, id).Scan(&active)
		if err != nil {
			return err
		}
		if !active || who == "" {
			_, err = tx.Exec(ctx, "update review_notifications set status='obsolete' where id=$1", id)
			return err
		}
		if authErr != nil || sendFeishuText(ctx, token, "im/v1/messages?receive_id_type=open_id", id, "你有待处理或需协调的审核事项。登录工作区查看当前待办。\n"+f.Public+"/?inbox=review", who) != nil {
			_, err = tx.Exec(ctx, `update review_notifications set status=case when attempts>=3 then 'failed' else 'pending' end,
               last_error='渠道未确认接收',next_at=now()+make_interval(secs=>5*attempts) where id=$1`, id)
			return err
		}
		_, err = tx.Exec(ctx, "update review_notifications set status='accepted',accepted_at=now(),last_error='' where id=$1", id)
		return err
	})
}

// Channel operations expose delivery counts; business titles and recipients
// remain in the authorized project inbox.
func (f Feishu) reminderHealth(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var counts struct {
		Pending  int `json:"pending"`
		Failed   int `json:"failed"`
		Accepted int `json:"accepted"`
		Obsolete int `json:"obsolete"`
	}
	err := f.DB.QueryRow(r.Context(), `select count(*) filter(where status='pending'),count(*) filter(where status='failed'),
 count(*) filter(where status='accepted'),count(*) filter(where status='obsolete') from review_notifications where tenant=$1`, f.Tenant).Scan(&counts.Pending, &counts.Failed, &counts.Accepted, &counts.Obsolete)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, counts)
}
