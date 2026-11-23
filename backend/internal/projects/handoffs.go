package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const unavailableReviewer = `not exists(select from members cur join project_members pm on pm.member_id=cur.id where cur.id=p.final_reviewer and cur.enabled and pm.project_id=p.id and pm.can_review)`

func nominateReviewer(ctx context.Context, tx pgx.Tx, p Project, actor, successor, reason, end string, emergency bool) (string, error) {
	if p.Stage == "delivered" || p.Stage == "adopted" || p.Stage == "cancelled" || p.Stage == "failed" {
		return "", web.Fail(409, "项目已结束")
	}
	reason = strings.TrimSpace(reason)
	end = strings.TrimSpace(end)
	if reason == "" || utf8.RuneCountInString(reason) > 2000 || utf8.RuneCountInString(end) > 500 || emergency && end == "" {
		return "", web.Fail(422, "请填写转交原因，异常接管还需说明结束条件")
	}
	if successor == p.Reviewer {
		return "", web.Fail(422, "接任人必须与当前确认人不同")
	}
	var eligible bool
	err := tx.QueryRow(ctx, `select exists(select from project_members pm join members m on m.id=pm.member_id where pm.project_id=$1 and pm.member_id=$2 and m.enabled and m.kind='person' for share of m,pm)`, p.ID, successor).Scan(&eligible)
	if err != nil {
		return "", err
	}
	if !emergency && eligible {
		var missing bool
		if err = tx.QueryRow(ctx, "select "+unavailableReviewer+" from projects p where p.id=$1", p.ID).Scan(&missing); err != nil {
			return "", err
		}
		if missing {
			return "", web.Fail(409, "原确认人已无法履职，请由管理员显式接管")
		}
	}
	if !eligible {
		return "", web.Fail(409, "接任人须为已获该项目访问权的在职成员")
	}
	if _, err = tx.Exec(ctx, `update reviewer_handoffs set state='superseded',decided_at=now(),decision_by=$2,decision_reason='新提名替换' where project_id=$1 and state='pending'`, p.ID, actor); err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `insert into reviewer_handoffs(project_id,previous_reviewer,successor,proposer,emergency,reason,end_condition) values($1,$2,$3,$4,$5,$6,$7) returning id`, p.ID, p.Reviewer, successor, actor, emergency, reason, end).Scan(&id)
	if err != nil {
		return "", err
	}
	if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
		return "", err
	}
	return id, database.Audit(ctx, tx, actor, "reviewer.nominate", p.ID, map[string]any{"handoff": id, "previous": p.Reviewer, "successor": successor, "emergency": emergency, "reason": reason, "endCondition": end})
}

// Only the affected team's administrators with their own project access can initiate this recovery.
func (s Server) takeover(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected                     int
		Member, Reason, EndCondition string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	var id string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Revision != in.Expected {
			return web.Fail(409, "请重新核对当前项目")
		}
		var allowed bool
		if err = tx.QueryRow(ctx, `select exists(select from projects p join team_members tm on tm.team_id=p.team_id where p.id=$1 and tm.member_id=$2 and `+unavailableReviewer+` for share of tm)`, p.ID, u.ID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return web.Fail(403, "异常接管要求本团队管理权限且原确认人已无法履职")
		}
		id, err = nominateReviewer(ctx, tx, p, u.ID, in.Member, in.Reason, in.EndCondition, true)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": id, "state": "pending"})
}

func (s Server) decideHandoff(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected       int
		Action, Reason string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Action != "accept" && in.Action != "decline" && in.Action != "withdraw" {
		return web.Fail(422, "请选择接受、谢绝或撤回")
	}
	if in.Action != "accept" && (web.Blank(in.Reason) || utf8.RuneCountInString(in.Reason) > 2000) {
		return web.Fail(422, "请填写处理原因")
	}
	ctx := r.Context()
	id := r.PathValue("handoff")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		var old, next, author, state string
		var emergency bool
		err = tx.QueryRow(ctx, "select previous_reviewer,successor,proposer,state,emergency from reviewer_handoffs where id=$1 and project_id=$2 and recovery_until is null for update", id, p.ID).Scan(&old, &next, &author, &state, &emergency)
		if err != nil {
			return err
		}
		if in.Action == "withdraw" {
			if u.ID != author && u.ID != p.Reviewer && u.ID != p.Owner {
				return web.Fail(403, "需要提名人或项目负责人的权限")
			}
		} else if u.ID != next {
			return web.Fail(403, "仅被提名人可承接或谢绝")
		}
		if state != "pending" || old != p.Reviewer || p.Revision != in.Expected {
			return web.Fail(409, "转交状态或项目版本已变化，请重新核对")
		}
		switch in.Action {
		case "accept":
			var eligible bool
			if err = tx.QueryRow(ctx, "select exists(select from members m join project_members pm on pm.member_id=m.id where m.id=$1 and pm.project_id=$2 and m.enabled and m.kind='person' for share of m,pm)", next, p.ID).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return web.Fail(403, "项目参与资格已失效")
			}
			if !emergency {
				if err = tx.QueryRow(ctx, `select exists(select from projects p join project_members pm on pm.project_id=p.id join members m on m.id=pm.member_id where p.id=$1 and m.id=$2 and m.enabled and m.id in(p.owner,p.final_reviewer) and not `+unavailableReviewer+` for share of pm,m)`, p.ID, author).Scan(&eligible); err != nil {
					return err
				}
				if !eligible {
					return web.Fail(409, "原职责或提名权限已变化，请重新交接")
				}
			}
			if emergency {
				if err = tx.QueryRow(ctx, `select exists(select from members m join team_members tm on tm.member_id=m.id join projects p on p.team_id=tm.team_id join project_members pm on pm.project_id=p.id and pm.member_id=m.id where p.id=$1 and m.id=$2 and m.enabled and m.role='admin' and `+unavailableReviewer+` for share of m,tm,pm)`, p.ID, author).Scan(&eligible); err != nil {
					return err
				}
				if !eligible {
					return web.Fail(409, "接管条件已变化，请重新核对原负责人和提名人")
				}
			}
			if _, err = tx.Exec(ctx, "update project_members set can_review=true where project_id=$1 and member_id=$2", p.ID, next); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "update projects set final_reviewer=$1,reviewer_assignment=$2 where id=$3", next, id, p.ID); err != nil {
				return err
			}
			state = "accepted"
		case "decline":
			state = "declined"
		case "withdraw":
			state = "withdrawn"
		}
		if _, err = tx.Exec(ctx, "update reviewer_handoffs set state=$2,decided_at=now(),decision_by=$3,decision_reason=$4 where id=$1", id, state, u.ID, strings.TrimSpace(in.Reason)); err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "reviewer."+state, p.ID, map[string]any{"handoff": id, "previous": old, "successor": next, "emergency": emergency, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s Server) handoffQueue(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('project',p.id,'title',p.title,'offered',h.successor=$1,'unavailable',`+unavailableReviewer+`) order by p.updated_at desc,p.id),'[]')
 from projects p join project_members pm on pm.project_id=p.id and pm.member_id=$1 join members m on m.id=pm.member_id and m.enabled
 left join reviewer_handoffs h on h.project_id=p.id and h.state='pending' and h.recovery_until is null
 where p.stage not in('cancelled','delivered','adopted','failed') and (h.successor=$1 or (m.role='admin' and exists(select from team_members t where t.team_id=p.team_id and t.member_id=$1) and `+unavailableReviewer+`))`, u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}
