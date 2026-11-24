package knowledge

import (
	"context"
	"net/http"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type Preference struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	Revision  int       `json:"revision"`
	Enabled   bool      `json:"enabled"`
	Review    time.Time `json:"reviewAt"`
	Expires   time.Time `json:"expiresAt"`
	Lifecycle int       `json:"lifecycle"`
	State     string    `json:"state"`
	PurgeDue  time.Time `json:"purgeDue"`
	Agent     *string   `json:"agent,omitempty"`
	AgentName *string   `json:"agentName,omitempty"`
}

func (s Server) preferences(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	filter := "l.state in ('active','disabled')"
	if r.URL.Query().Get("view") == "recycle" {
		filter = "l.state in ('deleted','expired','departed') and l.purge_due>now()"
	}
	// A returning employee may inspect metadata before authenticating to recover a quarantined entry.
	rows, err := s.DB.Query(r.Context(), `select l.id,case when l.departure_guard then '' else l.body end,l.revision,l.enabled,
 l.review_at,l.expires_at,l.lifecycle_revision,l.state,l.purge_due,
 case when not l.departure_guard then f.agent_scope end,
 case when not l.departure_guard then published.name end
 from preference_lifecycle l join preferences f on f.id=l.id
 left join agents a on a.id=f.agent_scope and not a.disabled and exists(
     select from team_members m where (m.team_id,m.member_id)=(a.team_id,$1))
 left join agent_revisions published on(published.agent_id,published.revision)=(a.id,a.published_revision)
 where l.member_id=$1 and `+filter+" order by l.review_at,l.id", u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Preference])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}

func lockPreferenceOwner(ctx context.Context, tx pgx.Tx, member string) (string, error) {
	var hash string
	err := tx.QueryRow(ctx, "select password_hash from members where id=$1 and enabled and departed_at is null for share", member).Scan(&hash)
	return hash, err
}

func (s Server) savePreference(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Body     string
		Enabled  bool
		Expected int
		Until    *time.Time
		Agent    *string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Body) || len(in.Body) > 4096 {
		return web.Fail(422, "个人偏好限 4096 字节")
	}
	if in.Agent != nil && *in.Agent != "" {
		agent, err := uuid.Parse(*in.Agent)
		if err != nil {
			return web.Fail(422, "请选择适用的已发布 Agent")
		}
		scope := agent.String()
		in.Agent = &scope
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := lockPreferenceOwner(ctx, tx, u.ID); err != nil {
			return err
		}
		if in.Agent != nil && *in.Agent != "" {
			var valid bool
			if err := tx.QueryRow(ctx, `select exists(select from agents a join team_members m on (m.team_id,m.member_id)=(a.team_id,$1)
 join agent_revisions pub on(pub.agent_id,pub.revision)=(a.id,a.published_revision)
 where a.id=$2 and not a.disabled and pub.tool is null)
 or exists(select from preferences f where f.id=nullif($3,'')::uuid and f.member_id=$1 and f.agent_scope=$2)`,
				u.ID, *in.Agent, id).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return web.Fail(409, "所选 Agent 不可用于个人偏好")
			}
		}
		if id == "" {
			err := tx.QueryRow(ctx, `insert into preferences(member_id,body,enabled,expires_at,agent_scope)
    select $1,$2,$3,coalesce($4,now()+interval '365 days'),nullif($5,'')::uuid
    where $4::timestamptz is null or ($4>now() and $4<=now()+interval '365 days') returning id`, u.ID, in.Body, in.Enabled, in.Until, in.Agent).Scan(&id)
			if err == pgx.ErrNoRows {
				return web.Fail(422, "临时截止时间须在未来 365 天以内")
			}
			if err != nil {
				return err
			}
		} else {
			if in.Until != nil {
				return web.Fail(422, "调整期限请使用明确续期操作")
			}
			tag, err := tx.Exec(ctx, `update preferences set body=$1,enabled=$2,revision=revision+1,lifecycle_revision=lifecycle_revision+1,
    agent_scope=case when $6::text is null then agent_scope else nullif($6,'')::uuid end
    where id=$3 and member_id=$4 and revision=$5 and deleted_at is null and purged_at is null and expires_at>now()`, in.Body, in.Enabled, id, u.ID, in.Expected, in.Agent)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return web.Fail(409, "偏好版本已变化或不可用")
			}
		}
		return database.Audit(ctx, tx, u.ID, "preference.save", id, map[string]bool{"enabled": in.Enabled})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"id": id})
}

func (s Server) deletePreference(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.applyPreferenceAction(w, r, u, "delete")
}
func (s Server) preferenceAction(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.applyPreferenceAction(w, r, u, "")
}
func (s Server) applyPreferenceAction(w http.ResponseWriter, r *http.Request, u identity.Member, action string) error {
	var in struct {
		Action              string
		Expected, Lifecycle int
		Password            string
		Until               *time.Time
	}
	legacy := action == "delete"
	if !legacy {
		if err := web.Read(r, &in); err != nil {
			return err
		}
		action = in.Action
	}
	id := r.PathValue("id")
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		hash, err := lockPreferenceOwner(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		var rev, life int
		var state string
		var withinRecovery, valid bool
		err = tx.QueryRow(ctx, `select revision,lifecycle_revision,state,purge_due>now(),expires_at>now() from preference_lifecycle where id=$1 and member_id=$2 and purged_at is null for update`, id, u.ID).Scan(&rev, &life, &state, &withinRecovery, &valid)
		if err != nil {
			return err
		}
		if !withinRecovery {
			return web.Fail(410, "偏好回收期已结束")
		}
		if !legacy && (rev != in.Expected || life != in.Lifecycle) {
			return web.Fail(409, "偏好已变化，请核对当前状态")
		}
		using := state == "active" || state == "disabled"
		var sql string
		args := []any{id}
		switch action {
		case "review":
			if !using {
				return web.Fail(409, "该偏好已进入回收区")
			}
			sql = "update preferences set review_at=now()+interval '90 days',lifecycle_revision=lifecycle_revision+1 where id=$1"
		case "renew", "restore":
			if (action == "renew") != using {
				return web.Fail(409, "偏好状态已变化")
			}
			if state == "departed" && (!valid || bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil) {
				return web.Fail(403, "离职隔离偏好须在原有效期内由本人验证密码后恢复")
			}
			if action == "restore" && valid {
				if in.Until != nil {
					return web.Fail(409, "恢复须沿用尚未到期的原截止日期")
				}
				sql = "update preferences set deleted_at=null,departure_guard=false,enabled=true,revision=revision+1,lifecycle_revision=lifecycle_revision+1 where id=$1"
			} else {
				sql = `update preferences set expires_at=coalesce($2::timestamptz,now()+interval '365 days'),review_at=now()+interval '90 days',lifecycle_revision=lifecycle_revision+1`
				if action == "restore" {
					sql += ",deleted_at=null,enabled=true,revision=revision+1"
				}
				sql += " where id=$1 and ($2::timestamptz is null or ($2>now() and $2<=now()+interval '365 days'))"
				args = append(args, in.Until)
			}
		case "delete":
			if !using {
				return web.Fail(409, "偏好已进入回收区")
			}
			sql = "update preferences set deleted_at=now(),enabled=false,revision=revision+1,lifecycle_revision=lifecycle_revision+1 where id=$1"
		case "purge":
			if using {
				return web.Fail(409, "请先移入回收区并核对清除范围")
			}
			sql = "update preferences set body=null,purged_at=now(),enabled=false,revision=revision+1,lifecycle_revision=lifecycle_revision+1 where id=$1"
		default:
			return web.Fail(422, "未知偏好操作")
		}
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(422, "请选择有效截止日期")
		}
		return database.Audit(ctx, tx, u.ID, "preference."+action, id, map[string]int{"previousRevision": rev, "previousLifecycle": life})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
