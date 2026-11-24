package channels

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"alliance/internal/database"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type imSender func(context.Context, imJob, string) (string, time.Duration)
type imFormatter func(string, string) string

func newIMHTTPClient() *http.Client {
	return &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s imChannel) serve(ctx context.Context, send imSender, format imFormatter) {
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	maintenance := time.NewTicker(time.Minute)
	defer maintenance.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if err := s.processNext(ctx); err != nil && ctx.Err() == nil {
				slog.Warn(s.name + " event processing pending")
			}
			if err := s.deliverNext(ctx, send, format); err != nil && ctx.Err() == nil {
				slog.Warn(s.name + " reply delivery pending")
			}
		case <-maintenance.C:
			if err := s.expireJobs(ctx); err != nil && ctx.Err() == nil {
				slog.Warn(s.name + " event retention pending")
			}
		}
	}
}

func (s imChannel) expireJobs(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `with expired as (
        select event_id,state from im_events where connection=$1 and (
          state='sending' and next_at<=now() or state in ('received','ready') and
          (received_at<=now()-interval '1 hour' or reply_expires_at<=now()))
        limit 128 for update skip locked
      ) update im_events e set state=case when x.state='sending' then 'unknown' else 'suppressed' end,
        payload=null,reply_target=null,note='' from expired x where (e.connection,e.event_id)=($1,x.event_id)`, s.key); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `delete from im_events where connection=$1 and state in ('sent','unknown','failed','suppressed')
        and received_at<=now()-interval '7 days'`, s.key); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, "delete from im_binding_codes where connection=$1 and expires_at<=now()", s.key)
	return err
}

func (s imChannel) processNext(ctx context.Context) error {
	return database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var job imJob
		err := tx.QueryRow(ctx, `select e.event_id,e.chat_id,e.thread_id,e.external_user,e.binding_id,e.payload
          from im_events e where e.connection=$1 and e.state='received' and e.next_at<=now() and e.received_at>now()-interval '1 hour'
            and (e.reply_expires_at is null or e.reply_expires_at>now())
            and not exists(select from im_events earlier where earlier.state='received'
              and (earlier.connection,earlier.chat_id,earlier.thread_id)=(e.connection,e.chat_id,e.thread_id)
              and earlier.received_at>now()-interval '1 hour'
              and (earlier.reply_expires_at is null or earlier.reply_expires_at>now())
              and (earlier.received_at,earlier.event_id)<(e.received_at,e.event_id))
          order by e.received_at,e.event_id limit 1 for update of e skip locked`, s.key).
			Scan(&job.id, &job.chat, &job.thread, &job.sender, &job.binding, &job.sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job.text, err = s.vault.Open(job.sealed, s.eventScope(job.id))
		if err != nil {
			return s.finishReply(ctx, tx, job.id, "failed", 0)
		}
		var reply imReply
		err = pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
			var err error
			reply, err = s.command(ctx, inner, &job)
			return err
		})
		var problem web.Problem
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			reply = imReply{note: "项目或待办不可用，请在企业网页刷新。"}
		case errors.As(err, &problem) && problem.Status < 500:
			reply = imReply{note: problem.Message}
		case err != nil:
			slog.Warn(s.name + " command transaction will be retried")
			_, err := tx.Exec(ctx, `update im_events set process_attempts=process_attempts+1,
                next_at=clock_timestamp()+make_interval(secs=>power(2,process_attempts+1)),
                state=case when process_attempts>=3 then 'failed' else 'received' end,
                payload=case when process_attempts>=3 then null else payload end,
                reply_target=case when process_attempts>=3 then null else reply_target end
                where (connection,event_id)=($1,$2)`, s.key, job.id)
			return err
		}
		_, err = tx.Exec(ctx, `update im_events set binding_id=$3,project_id=nullif($4,'')::uuid,reply_kind=$5,note=$6,
          state='ready',payload=null,next_at=clock_timestamp() where (connection,event_id)=($1,$2)`,
			s.key, job.id, job.binding, reply.project, reply.kind, reply.note)
		return err
	})
}

func imRateLimited(after string, attempts int) (string, time.Duration) {
	seconds, err := strconv.Atoi(after)
	if err != nil || seconds < 1 {
		seconds = 2
	}
	if attempts >= 4 || seconds >= 3600 {
		return "failed", 0
	}
	return "ready", time.Duration(seconds) * time.Second
}

func (s imChannel) deliverNext(ctx context.Context, send imSender, format imFormatter) error {
	var id string
	// Commit the dispatch intent before calling Slack. If a process stops
	// after a send might have happened, the lease expires into unknown.
	err := s.db.QueryRow(ctx, `with due as (
        select e.event_id from im_events e where e.connection=$1 and e.state='ready' and e.next_at<=now()
          and e.received_at>now()-interval '1 hour' and e.attempts<4
          and (e.reply_expires_at is null or e.reply_expires_at>now())
          and not exists(select from im_events older where older.state in ('ready','sending')
            and (older.connection,older.chat_id,older.thread_id)=(e.connection,e.chat_id,e.thread_id)
            and (older.received_at,older.event_id)<(e.received_at,e.event_id)
            and older.received_at>now()-interval '1 hour'
            and (older.reply_expires_at is null or older.reply_expires_at>now()))
        order by e.next_at,e.received_at limit 1 for update of e skip locked
      ) update im_events e set state='sending',attempts=attempts+1,next_at=clock_timestamp()+interval '30 seconds'
        from due where (e.connection,e.event_id)=($1,due.event_id) returning e.event_id`, s.key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var job imJob
		job.id = id
		err := tx.QueryRow(ctx, `select chat_id,thread_id,external_user,binding_id,project_id,note,reply_kind,attempts,reply_target,reply_expires_at
          from im_events where (connection,event_id)=($1,$2) and state='sending' and next_at>now() for update`, s.key, id).
			Scan(&job.chat, &job.thread, &job.sender, &job.binding, &job.project, &job.text, &job.replyKind, &job.attempts, &job.replyTarget, &job.replyExpiry)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		text, err := s.authorizeReply(ctx, tx, job, format)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.finishReply(ctx, tx, id, "suppressed", 0)
		}
		if err != nil {
			return err
		}
		state, delay := send(ctx, job, text)
		return s.finishReply(ctx, tx, id, state, delay)
	})
}

func (s imChannel) authorizeReply(ctx context.Context, tx pgx.Tx, job imJob, format imFormatter) (string, error) {
	var actor string
	if job.binding != nil {
		var err error
		actor, err = s.boundMember(ctx, tx, job)
		if err != nil {
			return "", err
		}
	}
	if job.replyKind == "" {
		return format(job.text, ""), nil
	}
	if job.project == nil || actor == "" {
		return "", pgx.ErrNoRows
	}
	if _, err := imReadableProject(ctx, tx, *job.project, actor); err != nil {
		return "", err
	}
	text := job.text
	if job.replyKind == "progress" || job.replyKind == "todos" {
		var err error
		text, err = imProjectReply(ctx, tx, actor, *job.project, job.replyKind)
		if err != nil {
			return "", err
		}
	}
	return format(text, s.public+"/#"+*job.project), nil
}

func (s imChannel) finishReply(ctx context.Context, tx pgx.Tx, id, state string, delay time.Duration) error {
	_, err := tx.Exec(ctx, `update im_events set state=$3,payload=null,note=case when $3='ready' then note else '' end,
        reply_target=case when $3='ready' then reply_target end,
        next_at=clock_timestamp()+make_interval(secs=>$4) where (connection,event_id)=($1,$2)`, s.key, id, state, delay.Seconds())
	return err
}
