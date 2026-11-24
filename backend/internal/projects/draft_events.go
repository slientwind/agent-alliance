package projects

import (
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

type draftFragment struct {
	Seq     int64  `json:"-"`
	Step    int    `json:"step"`
	Attempt int    `json:"attempt"`
	Offset  int    `json:"offset"`
	Text    string `json:"text"`
	Status  string `json:"status"`
}

type draftBatch struct {
	head      int64
	scope     string
	snapshot  bool
	fragments []draftFragment
}

func (s Server) draftEvents(g identity.Server) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		return streamDraftEvents(w, r, 300*time.Millisecond, func(r *http.Request, after int64, scope string) (draftBatch, error) {
			u, err := g.Authenticate(r)
			if err != nil {
				return draftBatch{}, err
			}
			var batch draftBatch
			err = pgx.BeginTxFunc(r.Context(), s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
				var err error
				batch, err = readDraftBatch(r.Context(), tx, r.PathValue("id"), r.PathValue("run"), u.ID, after, scope)
				return err
			})
			return batch, err
		})
	}
}

func readDraftBatch(ctx context.Context, tx pgx.Tx, project, run, actor string, after int64, previousScope string) (draftBatch, error) {
	var batch draftBatch
	var state string
	var control int
	err := tx.QueryRow(ctx, `select r.draft_seq,r.status,r.control_revision from runs r
        join project_members m on m.project_id=r.project_id and m.member_id=r.actor
        join members u on u.id=m.member_id and u.enabled
        where r.id=$1 and r.project_id=$2 and r.actor=$3 and r.service_id is null`, run, project, actor).
		Scan(&batch.head, &state, &control)
	if err != nil {
		return batch, err
	}
	type attempt struct {
		Step, Number int
		Status       string
	}
	var attempts []attempt
	if batch.head > 0 && (state == "running" || state == "reconcile") {
		rows, err := tx.Query(ctx, `select i.ordinal,i.attempt_no,i.status
            from invocations i join effective_run_nodes n on(n.run_id,n.ordinal)=(i.run_id,i.ordinal)
            where i.run_id=$1 and i.status in('submitted','unknown')
                  and agent_revision_ready(n.agent_id,n.agent_revision)
                  and project_agent_access($2,n.agent_id,n.agent_revision,$3)
                  and agent_skills_usable(n.agent_id,n.agent_revision,$3)
                  and exists(select from run_draft_fragments f where
                      (f.run_id,f.ordinal,f.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
                      and f.control_revision=$4 and f.byte_offset=0 and f.created_at>now()-interval '1 hour')
            order by i.ordinal`, run, project, actor, control)
		if err != nil {
			return batch, err
		}
		attempts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[attempt])
		if err != nil {
			return batch, err
		}
	}
	if len(attempts) != 0 {
		// Verify all the run's pinned inputs and rules through the same read
		// boundary used before inference. This also checks withdrawn sources.
		if _, _, err := knowledge.RunInputs(ctx, tx, run, 0, false); err != nil {
			var unavailable web.Problem
			if !errors.As(err, &unavailable) {
				return batch, err
			}
			attempts = nil
		}
	}
	// Only the viewer's own active, authorized attempts enter a snapshot.
	batch.scope = fmt.Sprintf("%d/%s/%v", control, state, attempts)
	batch.snapshot = previousScope != batch.scope || after > batch.head
	if len(attempts) == 0 || (!batch.snapshot && after == batch.head) {
		return batch, nil
	}
	steps := make([]int, 0, len(attempts))
	for _, attempt := range attempts {
		steps = append(steps, attempt.Step)
	}
	var rows pgx.Rows
	if batch.snapshot {
		rows, err = tx.Query(ctx, `select max(f.seq),f.ordinal,f.attempt_no,0,
                string_agg(f.body,'' order by f.byte_offset),i.status
            from run_draft_fragments f join invocations i using(run_id,ordinal,attempt_no)
            where f.run_id=$1 and f.ordinal=any($2) and f.control_revision=$3
            group by f.ordinal,f.attempt_no,i.status order by f.ordinal`, run, steps, control)
	} else {
		rows, err = tx.Query(ctx, `select f.seq,f.ordinal,f.attempt_no,f.byte_offset,f.body,i.status
            from run_draft_fragments f join invocations i using(run_id,ordinal,attempt_no)
            where f.run_id=$1 and f.ordinal=any($2) and f.control_revision=$3 and f.seq>$4
            order by f.seq limit 32`, run, steps, control, after)
	}
	if err != nil {
		return batch, err
	}
	batch.fragments, err = pgx.CollectRows(rows, pgx.RowToStructByPos[draftFragment])
	return batch, err
}

func streamDraftEvents(w http.ResponseWriter, r *http.Request, every time.Duration, read func(*http.Request, int64, string) (draftBatch, error)) error {
	var after int64
	if cursor := r.Header.Get("Last-Event-ID"); cursor != "" {
		var err error
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			return web.Fail(422, "事件游标无效")
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	tick := time.NewTicker(every)
	defer tick.Stop()
	controller := http.NewResponseController(w)
	var lastWrite time.Time
	var scope string
	started := false
	emit := func(id int64, kind string, data any) error {
		body, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, kind, body)
		return err
	}
	for {
		readCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		batch, err := read(r.WithContext(readCtx), after, scope)
		stop()
		if err != nil {
			if !started {
				return err
			}
			var problem web.Problem
			if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) && problem.Status == http.StatusUnauthorized {
				_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := io.WriteString(w, "event: access-ended\ndata: {}\n\n"); err == nil {
					_ = controller.Flush()
				}
			}
			return nil
		}
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		scope = batch.scope
		if batch.snapshot || len(batch.fragments) != 0 || time.Since(lastWrite) >= 10*time.Second {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			switch {
			case batch.snapshot:
				// The current snapshot is authoritative even for an old cursor.
				after = batch.head
				fragments := batch.fragments
				if fragments == nil {
					fragments = []draftFragment{}
				}
				err = emit(after, "draft.snapshot", fragments)
			case len(batch.fragments) != 0:
				for _, fragment := range batch.fragments {
					after = fragment.Seq
					if err = emit(after, "draft.delta", fragment); err != nil {
						break
					}
				}
			default:
				_, err = io.WriteString(w, ": connected\n\n")
			}
			if err != nil || controller.Flush() != nil {
				return nil
			}
			lastWrite = time.Now()
			_ = controller.SetWriteDeadline(time.Time{})
		}
		if len(batch.fragments) == 0 || (len(batch.fragments) < 32 && after < batch.head) {
			after = batch.head
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
