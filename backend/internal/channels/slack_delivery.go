package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Slack) Serve(ctx context.Context) {
	if !s.configured() {
		return
	}
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
				slog.Warn("Slack event processing pending")
			}
			if err := s.deliverNext(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("Slack reply delivery pending")
			}
		case <-maintenance.C:
			if err := s.expireJobs(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("Slack event retention pending")
			}
		}
	}
}

func (s Slack) expireJobs(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `with expired as (
        select event_id,state from im_events where connection=$1 and (
          state='sending' and next_at<=now() or state in ('received','ready') and received_at<=now()-interval '1 hour')
        limit 128 for update skip locked
      ) update im_events e set state=case when x.state='sending' then 'unknown' else 'suppressed' end,
        payload=null,note='' from expired x where (e.connection,e.event_id)=($1,x.event_id)`, s.connection()); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `delete from im_events where connection=$1 and state in ('sent','unknown','failed','suppressed')
        and received_at<=now()-interval '7 days'`, s.connection()); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, "delete from im_binding_codes where connection=$1 and expires_at<=now()", s.connection())
	return err
}

func (s Slack) processNext(ctx context.Context) error {
	return database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var job slackJob
		err := tx.QueryRow(ctx, `select e.event_id,e.chat_id,e.thread_id,e.external_user,e.binding_id,e.payload
          from im_events e where e.connection=$1 and e.state='received' and e.next_at<=now() and e.received_at>now()-interval '1 hour'
            and not exists(select from im_events earlier where earlier.state='received'
              and (earlier.connection,earlier.chat_id,earlier.thread_id)=(e.connection,e.chat_id,e.thread_id)
              and earlier.received_at>now()-interval '1 hour'
              and (earlier.received_at,earlier.event_id)<(e.received_at,e.event_id))
          order by e.received_at,e.event_id limit 1 for update of e skip locked`, s.connection()).
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
		var reply slackReply
		err = pgx.BeginFunc(ctx, tx, func(inner pgx.Tx) error {
			var err error
			reply, err = s.command(ctx, inner, &job)
			return err
		})
		var problem web.Problem
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			reply = slackReply{note: "项目或待办不可用，请在企业网页刷新。"}
		case errors.As(err, &problem) && problem.Status < 500:
			reply = slackReply{note: problem.Message}
		case err != nil:
			slog.Warn("Slack command transaction will be retried")
			_, err := tx.Exec(ctx, `update im_events set process_attempts=process_attempts+1,
                next_at=clock_timestamp()+make_interval(secs=>power(2,process_attempts+1)),
                state=case when process_attempts>=3 then 'failed' else 'received' end,
                payload=case when process_attempts>=3 then null else payload end
                where (connection,event_id)=($1,$2)`, s.connection(), job.id)
			return err
		}
		_, err = tx.Exec(ctx, `update im_events set binding_id=$3,project_id=nullif($4,'')::uuid,reply_kind=$5,note=$6,
          state='ready',payload=null,next_at=clock_timestamp() where (connection,event_id)=($1,$2)`,
			s.connection(), job.id, job.binding, reply.project, reply.kind, reply.note)
		return err
	})
}

func (s Slack) deliverNext(ctx context.Context) error {
	var id string
	// Commit the dispatch intent before calling Slack. If a process stops
	// after a send might have happened, the lease expires into unknown.
	err := s.db.QueryRow(ctx, `with due as (
        select e.event_id from im_events e where e.connection=$1 and e.state='ready' and e.next_at<=now()
          and e.received_at>now()-interval '1 hour' and e.attempts<4
          and not exists(select from im_events older where older.state in ('ready','sending')
            and (older.connection,older.chat_id,older.thread_id)=(e.connection,e.chat_id,e.thread_id)
            and (older.received_at,older.event_id)<(e.received_at,e.event_id)
            and older.received_at>now()-interval '1 hour')
        order by e.next_at,e.received_at limit 1 for update of e skip locked
      ) update im_events e set state='sending',attempts=attempts+1,next_at=clock_timestamp()+interval '30 seconds'
        from due where (e.connection,e.event_id)=($1,due.event_id) returning e.event_id`, s.connection()).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return database.WithTx(ctx, s.db, func(tx pgx.Tx) error {
		var job slackJob
		job.id = id
		err := tx.QueryRow(ctx, `select chat_id,thread_id,external_user,binding_id,project_id,note,reply_kind,attempts
          from im_events where (connection,event_id)=($1,$2) and state='sending' and next_at>now() for update`, s.connection(), id).
			Scan(&job.chat, &job.thread, &job.sender, &job.binding, &job.project, &job.text, &job.replyKind, &job.attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		text, err := s.authorizeReply(ctx, tx, job)
		if errors.Is(err, pgx.ErrNoRows) {
			return s.finishReply(ctx, tx, id, "suppressed", 0)
		}
		if err != nil {
			return err
		}
		state, delay := s.postReply(ctx, job, text)
		return s.finishReply(ctx, tx, id, state, delay)
	})
}

var slackEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func (s Slack) authorizeReply(ctx context.Context, tx pgx.Tx, job slackJob) (string, error) {
	var actor string
	if job.binding != nil {
		var err error
		actor, err = s.boundMember(ctx, tx, job)
		if err != nil {
			return "", err
		}
	}
	if job.replyKind == "" {
		return slackEscape.Replace(job.text), nil
	}
	if job.project == nil || actor == "" {
		return "", pgx.ErrNoRows
	}
	if _, err := slackReadableProject(ctx, tx, *job.project, actor); err != nil {
		return "", err
	}
	text := job.text
	if job.replyKind == "progress" || job.replyKind == "todos" {
		var err error
		text, err = slackProjectReply(ctx, tx, actor, *job.project, job.replyKind)
		if err != nil {
			return "", err
		}
	}
	return slackEscape.Replace(text) + "\n<" + slackEscape.Replace(s.public) + "/#" + *job.project + "|打开企业网页中的原任务>", nil
}

func (s Slack) postReply(ctx context.Context, job slackJob, text string) (string, time.Duration) {
	payload := struct {
		Channel     string `json:"channel"`
		Thread      string `json:"thread_ts"`
		Text        string `json:"text"`
		Parse       string `json:"parse"`
		LinkNames   bool   `json:"link_names"`
		UnfurlLinks bool   `json:"unfurl_links"`
		UnfurlMedia bool   `json:"unfurl_media"`
	}{Channel: job.chat, Thread: job.thread, Text: text, Parse: "none"}
	data, err := json.Marshal(payload)
	if err != nil {
		return "failed", 0
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(data))
	if err != nil {
		return "failed", 0
	}
	req.Header.Set("Authorization", "Bearer "+s.bot)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := s.client.Do(req)
	if err != nil {
		return "unknown", 0
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		seconds, err := strconv.Atoi(res.Header.Get("Retry-After"))
		if err != nil || seconds < 1 {
			seconds = 2
		}
		if job.attempts >= 4 || seconds >= 3600 {
			return "failed", 0
		}
		return "ready", time.Duration(seconds) * time.Second
	}
	if res.StatusCode >= 500 {
		return "unknown", 0
	}
	if res.StatusCode != http.StatusOK {
		return "failed", 0
	}
	var response struct {
		OK      *bool  `json:"ok"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&response) != nil || response.OK == nil {
		return "unknown", 0
	}
	if !*response.OK {
		return "failed", 0
	}
	if response.Channel != job.chat || !slackTimestamp.MatchString(response.TS) {
		return "unknown", 0
	}
	return "sent", 0
}

func (s Slack) finishReply(ctx context.Context, tx pgx.Tx, id, state string, delay time.Duration) error {
	_, err := tx.Exec(ctx, `update im_events set state=$3,payload=null,note=case when $3='ready' then note else '' end,
        next_at=clock_timestamp()+make_interval(secs=>$4) where (connection,event_id)=($1,$2)`, s.connection(), id, state, delay.Seconds())
	return err
}
