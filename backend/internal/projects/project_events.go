package projects

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Project events invalidate the authorized detail snapshot. A reconnect
// compares the last seen token against the current committed state.
const projectEventStateSQL = `
select jsonb_build_array(p.revision,p.stage,m.can_review,m.can_view_costs,
 (select max(seq) from messages where project_id=p.id),
 (select jsonb_agg(jsonb_build_array(r.id,r.status,r.control_revision,
   exists(select from lookup_turns t where t.run_id=r.id and t.decision is not null),
   exists(select from run_working_sets c where c.run_id=r.id and c.invalidated_at is not null),
   (select jsonb_agg(jsonb_build_array(i.ordinal,i.status,i.attempt_no,i.remote_status,i.result is not null)
     order by i.ordinal) from invocations i where i.run_id=r.id))
   order by r.created_at,r.id)
  from (select id,status,control_revision,created_at from runs where project_id=p.id
        order by created_at desc,id desc limit 12) r))::text
from projects p join project_members m on m.project_id=p.id
where p.id=$1 and m.member_id=$2`

func (s Server) events(g identity.Server) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		return streamProjectEvents(w, r, time.Second, func(r *http.Request) (string, error) {
			u, err := g.Authenticate(r)
			if err != nil {
				return "", err
			}
			var state string
			err = s.DB.QueryRow(r.Context(), projectEventStateSQL, r.PathValue("id"), u.ID).Scan(&state)
			return state, err
		})
	}
}

func streamProjectEvents(w http.ResponseWriter, r *http.Request, interval time.Duration, state func(*http.Request) (string, error)) error {
	cursor := r.Header.Get("Last-Event-ID")
	if cursor != "" {
		if value, err := hex.DecodeString(cursor); err != nil || len(value) != sha256.Size {
			return web.Fail(422, "事件游标无效")
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	controller := http.NewResponseController(w)
	started := false
	var lastWrite time.Time
	for {
		readCtx, stop := context.WithTimeout(ctx, 3*time.Second)
		current, err := state(r.WithContext(readCtx))
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
		currentCursor := fmt.Sprintf("%x", sha256.Sum256([]byte(current)))
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
		if cursor != currentCursor || time.Since(lastWrite) >= 10*time.Second {
			_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if cursor != currentCursor {
				_, err = fmt.Fprintf(w, "id: %s\nevent: project.changed\ndata: {}\n\n", currentCursor)
				cursor = currentCursor
			} else {
				_, err = io.WriteString(w, ": connected\n\n")
			}
			if err != nil || controller.Flush() != nil {
				return nil
			}
			lastWrite = time.Now()
			// Clear the previous deadline while waiting for the next heartbeat.
			_ = controller.SetWriteDeadline(time.Time{})
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
