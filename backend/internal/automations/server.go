package automations

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}

func (s Server) Routes(mux *http.ServeMux, auth identity.Server) {
	mux.Handle("GET /api/automations", auth.Protect(false, s.list))
	mux.Handle("POST /api/automations", auth.Protect(false, s.create))
	mux.Handle("PUT /api/automations/{id}", auth.Protect(false, s.update))
	mux.Handle("PATCH /api/automations/{id}", auth.Protect(false, s.setActive))
	mux.Handle("DELETE /api/automations/{id}", auth.Protect(false, s.remove))
	mux.Handle("GET /api/automations/{id}/runs", auth.Protect(false, s.runs))
}

func goalScope(id string) string { return "automation/" + id + "/goal" }

const owned = `a.owner=$1 and a.state<>'deleted' and exists(
	select from team_members tm join members m on m.id=tm.member_id and m.enabled and m.kind='person'
	where (tm.team_id,tm.member_id)=(a.team_id,a.owner))`

type scheduleView struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Goal     string     `json:"goal"`
	Team     string     `json:"team"`
	Tier     string     `json:"tier"`
	Zone     string     `json:"zone"`
	Cron     *string    `json:"cron"`
	OnceAt   *time.Time `json:"onceAt"`
	State    string     `json:"state"`
	Revision int        `json:"revision"`
	Sync     string     `json:"sync"`
	Cursor   *time.Time `json:"cursor"`
}

func (s Server) list(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select a.id,a.title,a.goal_cipher,a.team_id,a.tier,a.time_zone,a.cron,a.once_at,
		a.state,a.revision,case when a.synced_revision=a.revision then 'ready' when a.sync_failed then 'retrying' else 'pending' end,a.cursor_at
		from automation_schedules a where `+owned+` order by a.updated_at desc,a.id limit 250`, actor.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := make([]scheduleView, 0)
	for rows.Next() {
		var item scheduleView
		var goal []byte
		if err := rows.Scan(&item.ID, &item.Title, &goal, &item.Team, &item.Tier, &item.Zone, &item.Cron, &item.OnceAt,
			&item.State, &item.Revision, &item.Sync, &item.Cursor); err != nil {
			return err
		}
		item.Goal, err = s.Vault.Open(goal, goalScope(item.ID))
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, items)
}

func (s Server) create(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := uuid.NewString()
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		parsed, err := uuid.Parse(key)
		if err != nil || parsed == uuid.Nil {
			return web.Fail(422, "提交编号无效")
		}
		id = parsed.String()
	}
	var in config
	if err := web.ReadLimited(w, r, &in, 24<<10); err != nil {
		return err
	}
	if err := in.validate(time.Now()); err != nil {
		return err
	}
	cipher, err := s.Vault.Seal(in.Goal, goalScope(id))
	if err != nil {
		return err
	}
	ctx := r.Context()
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into automation_schedules(id,owner,team_id,title,goal_cipher,tier,cron,once_at,time_zone)
			select $1,$2,$3,$4,$5,$6,$7,$8,$9 from team_members t join members m on m.id=t.member_id
			where (t.team_id,t.member_id)=($3,$2) and m.enabled and m.kind='person'
			on conflict(id) do nothing`, id, actor.ID, in.Team, in.Title, cipher, in.Tier, in.Cron, in.OnceAt, in.Zone)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var previous []byte
			err := tx.QueryRow(ctx, `select goal_cipher from automation_schedules a where `+owned+` and a.id=$2 and a.team_id=$3 and
				a.title=$4 and a.tier=$5 and a.cron is not distinct from $6::text and a.once_at is not distinct from $7::timestamptz
				and a.time_zone=$8 and a.revision=1`, actor.ID, id, in.Team, in.Title, in.Tier, in.Cron, in.OnceAt, in.Zone).Scan(&previous)
			if errors.Is(err, pgx.ErrNoRows) {
				return web.Fail(409, "团队不可用或该提交编号已使用")
			}
			if err != nil {
				return err
			}
			text, err := s.Vault.Open(previous, goalScope(id))
			if err != nil {
				return err
			}
			if text != in.Goal {
				return web.Fail(409, "该提交编号已使用")
			}
			return nil
		}
		return database.Audit(ctx, tx, actor.ID, "automation.create", id, map[string]string{"team": in.Team})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s Server) update(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in config
	if err := web.ReadLimited(w, r, &in, 24<<10); err != nil {
		return err
	}
	if err := in.validate(time.Now()); err != nil {
		return err
	}
	return s.change(w, r, actor, in.Expected, "edit", func(ctx context.Context, tx pgx.Tx, id, team string, _ *time.Time) error {
		if team != in.Team {
			return web.Fail(422, "请选择此定时委托所属团队")
		}
		cipher, err := s.Vault.Seal(in.Goal, goalScope(id))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update automation_schedules set title=$2,goal_cipher=$3,tier=$4,cron=$5,once_at=$6,time_zone=$7 where id=$1`,
			id, in.Title, cipher, in.Tier, in.Cron, in.OnceAt, in.Zone)
		return err
	})
}

func (s Server) setActive(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		Expected int
		Active   *bool
	}
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	if in.Active == nil {
		return web.Fail(422, "请选择启用或暂停")
	}
	state := "paused"
	if *in.Active {
		state = "active"
	}
	return s.setState(w, r, actor, in.Expected, state)
}

func (s Server) remove(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	expected, _ := strconv.Atoi(r.URL.Query().Get("expected"))
	return s.setState(w, r, actor, expected, "deleted")
}

func (s Server) setState(w http.ResponseWriter, r *http.Request, actor identity.Member, expected int, state string) error {
	return s.change(w, r, actor, expected, state, func(ctx context.Context, tx pgx.Tx, id, _ string, onceAt *time.Time) error {
		if state == "active" && onceAt != nil && !onceAt.After(time.Now()) {
			return web.Fail(409, "原执行时间已过，请修改时间后启用")
		}
		_, err := tx.Exec(ctx, `update automation_schedules set state=$2 where id=$1`, id, state)
		return err
	})
}

func (s Server) change(w http.ResponseWriter, r *http.Request, actor identity.Member, expected int, action string,
	apply func(context.Context, pgx.Tx, string, string, *time.Time) error) error {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return web.Fail(404, "找不到这项定时委托")
	}
	if expected < 1 {
		return web.Fail(422, "请刷新定时委托后再修改")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var team string
		var revision int
		var onceAt *time.Time
		err := tx.QueryRow(ctx, `select a.team_id,a.revision,a.once_at from automation_schedules a where `+owned+
			` and a.id=$2 for update of a`, actor.ID, id).Scan(&team, &revision, &onceAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(404, "找不到这项定时委托")
		}
		if err != nil {
			return err
		}
		if revision != expected {
			return web.Fail(409, "定时委托已更新，请刷新")
		}
		if err := apply(ctx, tx, id, team, onceAt); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update automation_schedules set revision=revision+1,updated_at=now(),sync_next_at=now(),
			sync_failed=false where id=$1`, id); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "automation."+action, id, map[string]int{"revision": revision + 1})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type runView struct {
	Occurrence string     `json:"occurrence"`
	Scheduled  time.Time  `json:"scheduled"`
	State      string     `json:"state"`
	Project    string     `json:"project,omitempty"`
	Cursor     *time.Time `json:"cursor"`
}

func (s Server) runs(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	id := r.PathValue("id")
	if _, err := uuid.Parse(id); err != nil {
		return web.Fail(404, "找不到这项定时委托")
	}
	rows, err := s.DB.Query(r.Context(), `select r.occurrence,r.scheduled_at,r.state,
		coalesce((select p.id::text from project_members p where (p.project_id,p.member_id)=(r.project_id,$1)),''),r.cursor_at
		from automation_runs r join automation_schedules a on a.id=r.schedule_id where `+owned+
		` and a.id=$2 order by r.scheduled_at desc,r.occurrence limit 50`, actor.ID, id)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[runView])
	if err != nil {
		return err
	}
	if items == nil {
		items = []runView{}
	}
	return web.JSON(w, http.StatusOK, items)
}
