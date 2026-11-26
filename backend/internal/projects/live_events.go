package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"alliance/internal/hotstream"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type liveWindow struct {
	run      string
	control  int
	attempts []draftAttempt
}

func (window liveWindow) scope() string {
	return fmt.Sprintf("%s/%d/%v", window.run, window.control, window.attempts)
}

func (window liveWindow) attempt(event hotstream.Event) string {
	if window.run != event.Run || window.control != event.Control {
		return ""
	}
	for _, attempt := range window.attempts {
		if attempt.Step == event.Step && attempt.Number == event.Attempt {
			return attempt.Status
		}
	}
	return ""
}

func (s Server) liveWindow(ctx context.Context, project, member string) (liveWindow, error) {
	var window liveWindow
	err := pgx.BeginTxFunc(ctx, s.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var status string
		var general, mayRead bool
		err := tx.QueryRow(ctx, `select coalesce(r.id::text,''),coalesce(r.status,''),coalesce(r.control_revision,0),g.run_id is not null,
		 coalesce(g.run_id is not null and general_run_inputs_usable(r.id) and general_delegate_observations_usable(r.id)
		  and run_review_notes_readable(r.id,$2)
		  and space_role(p.space_id,$2) in ('space_admin','member')
		  and (g.studio_release is null or studio_release_usable(g.studio_release,$2,p.id))
		  and not exists(select from plan_rules pr where (pr.project_id,pr.plan_version)=(p.id,r.plan_version)
		   and not rule_usable(pr.rule_id,pr.rule_revision,$2))
		  and not exists(select from general_agent_actions a join general_external_agents e on (e.run_id,e.agent_id)=(a.run_id,a.external_agent)
		   where a.run_id=r.id and a.state='completed' and not external_admission_can_use(e.agent_id,e.revision,$2))
		  and (r.actor=$2 or (not exists(select from plan_bound_preferences pref where (pref.project_id,pref.plan_version)=(p.id,r.plan_version))
		   and (exists(select from team_members t where (t.team_id,t.member_id)=(r.charge_team,$2)) or
		    not exists(select from general_agent_actions a where a.run_id=r.id and a.mcp_server is not null and a.state='completed')))),false)
		 from projects p join project_members pm on pm.project_id=p.id and pm.member_id=$2
		 join members u on u.id=pm.member_id and u.enabled
		 left join lateral (select id,status,control_revision,plan_version,actor,charge_team from runs r where r.project_id=p.id and r.service_id is null
		  and not exists(select from change_batches c where c.run_id=r.id)
		  and not exists(select from general_anchor_edits a where a.run_id=r.id)
		  and not exists(select from general_intake_work w where w.run_id=r.id) order by created_at desc,id desc limit 1) r on true
		 left join general_agent_runs g on g.run_id=r.id where p.id=$1`, project, member).
			Scan(&window.run, &status, &window.control, &general, &mayRead)
		if err != nil {
			return err
		}
		if status != "running" && (status != "reconcile" || general) {
			window = liveWindow{}
			return nil
		}
		if !general {
			legacy, err := authorizeDraftWindow(ctx, tx, project, window.run, member, true)
			window.attempts = legacy.attempts
			return err
		}
		if !mayRead {
			window = liveWindow{}
			return nil
		}
		rows, err := tx.Query(ctx, `select turn,1,'submitted'::text from general_agent_turns
		 where run_id=$1 and state='submitted' order by turn`, window.run)
		if err != nil {
			return err
		}
		window.attempts, err = pgx.CollectRows(rows, pgx.RowToStructByPos[draftAttempt])
		return err
	})
	return window, err
}

func (s Server) liveEvents(auth identity.Server) web.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		project := r.PathValue("id")
		member, err := auth.Authenticate(r)
		if err != nil {
			return err
		}
		window, err := s.liveWindow(ctx, project, member.ID)
		if err != nil {
			return err
		}
		var wake <-chan struct{}
		if s.SessionUpdates != nil {
			var close func()
			wake, close = s.SessionUpdates.subscribe(project)
			defer close()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		if !liveFrame(w, controller, "hot.reset", sessionSnapshot{Run: window.run, Drafts: []draftFragment{}}) {
			return nil
		}
		cursor := ""
		opened := time.Now()
		verified := opened
		keepalive := opened
		session := uuid.NewString()
		roster := make(map[string]string)
		lastLease := time.Time{}
		lastRoster := time.Time{}
		joined := false
		defer func() {
			if !joined {
				return
			}
			cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer stop()
			if s.Hot.RemovePresence(cleanup, project, member.ID, session) == nil {
				_ = s.Hot.Publish(cleanup, project, hotstream.Event{Kind: hotstream.Leave, Member: member.ID, Session: session})
			}
		}()
		keep := func() {
			lease, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			if s.Hot.KeepPresence(lease, project, member.ID, session) == nil && !joined {
				joined = true
				_ = s.Hot.Publish(lease, project, hotstream.Event{Kind: hotstream.Join, Member: member.ID, Session: session})
			}
			lastLease = time.Now()
		}
		keep()
		if !s.liveRoster(ctx, w, controller, project, roster) {
			return nil
		}
		lastRoster = time.Now()
		for ctx.Err() == nil {
			batch, err := s.Hot.Read(ctx, project, cursor)
			if err != nil {
				return nil
			}
			refresh := time.Since(verified) >= 10*time.Second || len(batch) > 0
			refreshRoster := time.Since(lastRoster) >= 5*time.Second
			select {
			case <-wake:
				refresh = true
				refreshRoster = true
			default:
			}
			if !refresh && !refreshRoster {
				continue
			}
			before := window.scope()
			currentMember, err := auth.Authenticate(r.WithContext(ctx))
			if err == nil {
				window, err = s.liveWindow(ctx, project, currentMember.ID)
			}
			if err != nil {
				var problem web.Problem
				if errors.Is(err, pgx.ErrNoRows) || errors.As(err, &problem) && (problem.Status == 401 || problem.Status == 403) {
					liveFrame(w, controller, "access-ended", struct{}{})
				}
				return nil
			}
			same := before == window.scope()
			if !same && !liveFrame(w, controller, "hot.reset", sessionSnapshot{Run: window.run, Drafts: []draftFragment{}}) {
				return nil
			}
			if time.Since(keepalive) >= 10*time.Second {
				if !liveFrame(w, controller, "", nil) {
					return nil
				}
				keepalive = time.Now()
			}
			verified = time.Now()
			if time.Since(lastLease) >= 10*time.Second {
				keep()
			}
			for _, record := range batch {
				cursor = record.ID
				event := record.Event
				switch event.Kind {
				case hotstream.Draft, hotstream.Thought:
					status := window.attempt(event)
					if status != "" && !liveFrame(w, controller, event.Kind, sessionDelta{event.Run, draftFragment{
						Step: event.Step, Attempt: event.Attempt, Offset: event.Offset, Text: event.Text, Status: status,
					}}) {
						return nil
					}
				case hotstream.Comment:
					if !record.At.Before(opened) && !liveFrame(w, controller, event.Kind, event) {
						return nil
					}
				case hotstream.Join, hotstream.Leave:
					refreshRoster = true
				}
			}
			if refreshRoster {
				if !s.liveRoster(ctx, w, controller, project, roster) {
					return nil
				}
				lastRoster = time.Now()
			}
		}
		return nil
	}
}

type presenceMember struct {
	ID   string `json:"member"`
	Name string `json:"name"`
}

func (s Server) liveRoster(ctx context.Context, w http.ResponseWriter, controller *http.ResponseController, project string, shown map[string]string) bool {
	read, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ids, err := s.Hot.OnlineMembers(read, project)
	if err != nil {
		return true
	}
	var members []presenceMember
	if len(ids) > 0 {
		rows, err := s.DB.Query(read, `select m.id,m.display_name from project_members p join members m on m.id=p.member_id and m.enabled
		 where p.project_id=$1 and m.id=any($2::uuid[]) order by m.id`, project, ids)
		if err != nil {
			return true
		}
		members, err = pgx.CollectRows(rows, pgx.RowToStructByPos[presenceMember])
		if err != nil {
			return true
		}
	}
	next := make(map[string]string, len(members))
	for _, member := range members {
		next[member.ID] = member.Name
		if old, present := shown[member.ID]; (!present || old != member.Name) && !liveFrame(w, controller, hotstream.Join, member) {
			return false
		}
	}
	for id := range shown {
		if _, present := next[id]; !present && !liveFrame(w, controller, hotstream.Leave, presenceMember{ID: id}) {
			return false
		}
	}
	clear(shown)
	for id, name := range next {
		shown[id] = name
	}
	return true
}

func liveFrame(w http.ResponseWriter, controller *http.ResponseController, kind string, value any) bool {
	_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
	var err error
	if kind == "" {
		_, err = io.WriteString(w, ": connected\n\n")
	} else {
		body, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, body)
	}
	if err != nil || controller.Flush() != nil {
		return false
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return true
}

func (s Server) publishLiveComment(ctx context.Context, project string) {
	if s.Hot == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_ = s.Hot.Publish(ctx, project, hotstream.Event{Kind: hotstream.Comment})
}
