package projects

import (
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

const sessionBatchSize = 32

type sessionCursor struct {
	Seq   int64  `json:"s"`
	Scope string `json:"v"`
}

func openSessionCursor(vault resources.Vault, project, value string) (sessionCursor, error) {
	var cursor sessionCursor
	if value == "" {
		return cursor, nil
	}
	if len(value) > 256 {
		return cursor, web.Fail(422, "会话事件游标无效")
	}
	blob, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		var body string
		body, err = vault.Open(blob, "project-session/"+project)
		if err == nil {
			err = json.Unmarshal([]byte(body), &cursor)
		}
	}
	if err != nil || cursor.Seq < 0 || len(cursor.Scope) != sha256.Size*2 {
		return sessionCursor{}, web.Fail(422, "会话事件游标无效")
	}
	return cursor, nil
}

func sealSessionCursor(vault resources.Vault, project string, cursor sessionCursor) (string, error) {
	body, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	blob, err := vault.Seal(string(body), "project-session/"+project)
	return base64.RawURLEncoding.EncodeToString(blob), err
}

type sessionRecord struct {
	seq   int64
	event string
	data  any
}

type sessionBatch struct {
	next        sessionCursor
	records     []sessionRecord
	more        bool
	activeDraft bool
}

type sessionSnapshot struct {
	Run    string          `json:"run"`
	Drafts []draftFragment `json:"drafts"`
}

type sessionDelta struct {
	Run string `json:"run"`
	draftFragment
}

func (s Server) sessionEvents(g identity.Server) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		project := r.PathValue("id")
		cursor, err := openSessionCursor(s.Vault, project, r.Header.Get("Last-Event-ID"))
		if err != nil {
			return err
		}
		return streamSessionEvents(w, r, cursor, 300*time.Millisecond,
			func(cursor sessionCursor) (string, error) { return sealSessionCursor(s.Vault, project, cursor) },
			func(r *http.Request, cursor sessionCursor) (sessionBatch, error) {
				u, err := g.Authenticate(r)
				if err != nil {
					return sessionBatch{}, err
				}
				var batch sessionBatch
				ctx := r.Context()
				err = pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
					var readErr error
					batch, readErr = readSessionBatch(ctx, tx, project, u.ID, cursor)
					return readErr
				})
				return batch, err
			})
	}
}

func readSessionBatch(ctx context.Context, tx pgx.Tx, project, actor string, cursor sessionCursor) (sessionBatch, error) {
	var run string
	var head, floor int64
	var review, costs, owner bool
	err := tx.QueryRow(ctx, `select coalesce(h.seq,0),coalesce(retained.seq-1,h.seq,0),
		m.can_review,m.can_view_costs,p.owner=$2,
		coalesce(case when current_run.actor=$2 and current_run.service_id is null
			and current_run.draft_seq>0 and current_run.status in ('running','reconcile') then current_run.id::text end,'')
		from projects p join project_members m on (m.project_id,m.member_id)=(p.id,$2)
		join members u on u.id=m.member_id and u.enabled
		left join project_session_heads h on h.project_id=p.id
		left join lateral (select seq from project_session_events
			where project_id=p.id and created_at>now()-interval '1 hour' order by seq limit 1) retained on true
		left join lateral (select r.id,r.actor,r.service_id,r.draft_seq,r.status from runs r where r.project_id=p.id
			and not exists(select from change_batches c where c.run_id=r.id) order by r.created_at desc,r.id desc limit 1) current_run on true
		where p.id=$1`, project, actor).Scan(&head, &floor, &review, &costs, &owner, &run)
	if err != nil {
		return sessionBatch{}, err
	}
	var window draftWindow
	if run != "" {
		window, err = readDraftWindow(ctx, tx, project, run, actor)
		if err != nil {
			return sessionBatch{}, err
		}
	}
	draftScope := ""
	if len(window.attempts) > 0 {
		draftScope = run + "/" + window.scope
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s/%t/%t/%t/%s", actor, review, costs, owner, draftScope)))
	scope := hex.EncodeToString(hash[:])
	batch := sessionBatch{next: sessionCursor{Seq: head, Scope: scope}, activeDraft: len(window.attempts) > 0}
	if cursor.Scope != scope || cursor.Seq < floor || cursor.Seq > head {
		drafts, err := window.fragments(ctx, tx, run, 0, true)
		if err != nil {
			return sessionBatch{}, err
		}
		if drafts == nil {
			drafts = []draftFragment{}
		}
		batch.records = []sessionRecord{{head, "session.snapshot", sessionSnapshot{run, drafts}}}
		return batch, nil
	}
	if cursor.Seq == head {
		return batch, nil
	}
	rows, err := tx.Query(ctx, `with next as materialized (
		select seq,run_id,draft_seq from project_session_events
		where project_id=$1 and seq>$2 and created_at>now()-interval '1 hour' order by seq limit $6
	)
	select n.seq,n.run_id is null,coalesce(d.ordinal,0),coalesce(d.attempt_no,0),
		coalesce(d.byte_offset,0),coalesce(d.body,''),coalesce(d.status,'')
	from next n left join lateral (
		select f.ordinal,f.attempt_no,f.byte_offset,f.body,i.status
		from run_draft_fragments f join invocations i using(run_id,ordinal,attempt_no)
		where (f.run_id,f.seq)=(n.run_id,n.draft_seq) and f.run_id=nullif($3,'')::uuid
			and f.ordinal=any($4) and f.control_revision=$5
	) d on true order by n.seq`, project, cursor.Seq, run, window.steps(), window.control, sessionBatchSize)
	if err != nil {
		return sessionBatch{}, err
	}
	defer rows.Close()
	var scanned int
	var last int64
	for rows.Next() {
		var changed bool
		var fragment draftFragment
		if err := rows.Scan(&last, &changed, &fragment.Step, &fragment.Attempt, &fragment.Offset, &fragment.Text, &fragment.Status); err != nil {
			return sessionBatch{}, err
		}
		scanned++
		if changed {
			batch.records = append(batch.records, sessionRecord{last, "project.changed", struct{}{}})
		} else if fragment.Step != 0 {
			batch.records = append(batch.records, sessionRecord{last, "draft.delta", sessionDelta{run, fragment}})
		}
	}
	if scanned == sessionBatchSize && last < head {
		batch.next.Seq, batch.more = last, true
	}
	return batch, rows.Err()
}

func streamSessionEvents(w http.ResponseWriter, r *http.Request, cursor sessionCursor, interval time.Duration,
	seal func(sessionCursor) (string, error), read func(*http.Request, sessionCursor) (sessionBatch, error)) error {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	tick := time.NewTimer(time.Second)
	defer tick.Stop()
	controller := http.NewResponseController(w)
	started := false
	var lastWrite time.Time
	for {
		readCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		batch, err := read(r.WithContext(readCtx), cursor)
		stop()
		if err != nil {
			if !started {
				return err
			}
			var problem web.Problem
			if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) && (problem.Status == http.StatusUnauthorized || problem.Status == http.StatusForbidden) {
				_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.WriteString(w, "event: access-ended\ndata: {}\n\n"); err == nil {
					_ = controller.Flush()
				}
			}
			return nil
		}
		cursor = batch.next
		if len(batch.records) > 0 || time.Since(lastWrite) >= 10*time.Second {
			if !started {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("X-Accel-Buffering", "no")
				started = true
			}
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if len(batch.records) == 0 {
				_, err = io.WriteString(w, ": connected\n\n")
			}
			for _, record := range batch.records {
				id, encodeErr := seal(sessionCursor{Seq: record.seq, Scope: cursor.Scope})
				if encodeErr != nil {
					return nil
				}
				body, encodeErr := json.Marshal(record.data)
				if encodeErr != nil {
					return nil
				}
				if _, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, record.event, body); err != nil {
					break
				}
			}
			if err != nil || controller.Flush() != nil {
				return nil
			}
			lastWrite = time.Now()
			_ = controller.SetWriteDeadline(time.Time{})
		}
		if ctx.Err() != nil {
			return nil
		}
		if batch.more {
			continue
		}
		wait := time.Second
		if batch.activeDraft {
			wait = interval
		}
		tick.Reset(wait)
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
