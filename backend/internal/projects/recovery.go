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

const orphanProject = `p.stage not in('adopted','cancelled','delivered','failed') and not exists(
 select from project_members pm join members m on m.id=pm.member_id where pm.project_id=p.id and m.enabled and m.kind='person')
 and (exists(select from members departed where departed.id in(p.owner,p.final_reviewer) and departed.departed_at is not null)
 or exists(select from reviewer_handoffs previous where previous.id=p.reviewer_assignment and previous.recovery_until is not null))`

// Recovery discovery exposes only routing metadata until the nominated member accepts this exact scope.
func (s Server) recoveryList(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var out json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('project',p.id,'team',t.name,'revision',p.revision,
 'offer',h.id,'state',coalesce(h.state,'available'),'mine',coalesce(h.successor=$1,false),'until',h.recovery_until,'reason',h.reason,
 'nominee',candidate.display_name,'proposer',sponsor.display_name) order by p.updated_at desc,p.id),'[]')
 from team_members tm join teams t on t.id=tm.team_id join projects p on p.team_id=t.id join members me on me.id=tm.member_id and me.enabled
 left join lateral(select h.* from reviewer_handoffs h where h.project_id=p.id and h.recovery_until>statement_timestamp() and h.access_ended_at is null
 and (h.state='pending' or (h.state='accepted' and h.id=p.reviewer_assignment)) order by (h.state='pending') desc limit 1)h on true
 left join members candidate on candidate.id=h.successor left join members sponsor on sponsor.id=h.proposer
 where tm.member_id=$1 and not personal_project(p.id) and ((me.role='admin' and (`+orphanProject+` or h.id is not null)) or h.successor=$1)`, u.ID).Scan(&out)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, out)
}

type recoveryProject struct {
	team, reviewer, stage string
	revision              int
}

func lockRecovery(ctx context.Context, tx pgx.Tx, id, actor string) (recoveryProject, error) {
	var p recoveryProject
	err := tx.QueryRow(ctx, `select p.team_id,p.final_reviewer,p.stage,p.revision from projects p join team_members tm on tm.team_id=p.team_id and tm.member_id=$2 where p.id=$1 and not personal_project(p.id) for update of p for share of tm`, id, actor).Scan(&p.team, &p.reviewer, &p.stage, &p.revision)
	return p, err
}
func requireOrphan(ctx context.Context, tx pgx.Tx, id string, expected, current int) error {
	if expected != current {
		return web.Fail(409, "项目已变化，请刷新恢复清单")
	}
	var eligible bool
	if err := tx.QueryRow(ctx, "select "+orphanProject+" from projects p where p.id=$1", id).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return web.Fail(409, "项目已恢复有效成员、已结束或缺少离职依据")
	}
	return nil
}

func (s Server) recoveryOffer(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Email, Reason   string
		Hours, Expected int
		GrantAccess     bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !in.GrantAccess || in.Hours < 1 || in.Hours > 168 || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 2000 {
		return web.Fail(422, "请明确授权范围，填写理由和 1 至 168 小时的期限")
	}
	id := r.PathValue("id")
	ctx := r.Context()
	var offer string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		p, err := lockRecovery(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		if err = requireOrphan(ctx, tx, id, in.Expected, p.revision); err != nil {
			return err
		}
		var nominee string
		if err = tx.QueryRow(ctx, `select m.id from members m join team_members tm on tm.member_id=m.id join projects p on p.team_id=tm.team_id
   where p.id=$1 and m.email=$2 and m.enabled and m.kind='person' and m.id<>p.owner for share of m,tm`, id, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&nominee); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update reviewer_handoffs set state='superseded',decided_at=now(),decision_by=$2,decision_reason='替换恢复提名' where project_id=$1 and state='pending'`, id, u.ID); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `insert into reviewer_handoffs(project_id,previous_reviewer,successor,proposer,emergency,reason,end_condition,recovery_until)
   values($1,$2,$3,$4,true,$5,'到期、撤权、职责转交或项目结束',clock_timestamp()+make_interval(hours=>$6)) returning id`, id, p.reviewer, nominee, u.ID, in.Reason, in.Hours).Scan(&offer)
		if err != nil {
			return err
		}
		if err = Touch(ctx, tx, id, p.stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.recovery.offer", id, map[string]any{"offer": offer, "member": nominee, "hours": in.Hours, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": offer})
}

func (s Server) recoveryDecision(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason string
		Expected       int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Action != "accept" && in.Action != "decline" && in.Action != "end" {
		return web.Fail(422, "请选择有效的恢复操作")
	}
	if in.Action != "accept" && (web.Blank(in.Reason) || utf8.RuneCountInString(in.Reason) > 2000) {
		return web.Fail(422, "请填写处理依据")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	offer := r.PathValue("offer")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lockRecovery(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		var author, nominee, state string
		err = tx.QueryRow(ctx, `select proposer,successor,state from reviewer_handoffs where id=$1 and project_id=$2
   and recovery_until>clock_timestamp() and access_ended_at is null and state in('pending','accepted') and
   (successor=$3 or exists(select from members where id=$3 and enabled and role='admin')) for update`, offer, id, u.ID).Scan(&author, &nominee, &state)
		if err != nil {
			return err
		}
		if p.revision != in.Expected {
			return web.Fail(409, "恢复状态已变化，请刷新")
		}
		switch in.Action {
		case "accept":
			if u.ID != nominee {
				return web.Fail(403, "只有接任人可接受授权")
			}
			if state != "pending" {
				return web.Fail(409, "恢复提名已处理")
			}
			if err = requireOrphan(ctx, tx, id, in.Expected, p.revision); err != nil {
				return err
			}
			var valid bool
			if err = tx.QueryRow(ctx, `select exists(select from members a join team_members tm on tm.member_id=a.id join members n on n.id=$2 and n.enabled and n.kind='person'
   where a.id=$1 and tm.team_id=$3 and a.enabled and a.role='admin' for share of a,tm,n)`, author, nominee, p.team).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return web.Fail(409, "提名人或接任人资格已变更")
			}
			tag, err := tx.Exec(ctx, `insert into project_memberships(project_id,member_id,can_review,recovery_assignment) values($1,$2,true,$3)
    on conflict(project_id,member_id) do update set can_review=true,can_view_costs=false,recovery_assignment=excluded.recovery_assignment where project_memberships.recovery_assignment is not null`, id, nominee, offer)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "成员授权已变化，请重新核对")
			}
			if _, err = tx.Exec(ctx, "update projects set final_reviewer=$2,reviewer_assignment=$3 where id=$1", id, nominee, offer); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "update reviewer_handoffs set state='accepted',decided_at=now(),decision_by=$2 where id=$1", offer, u.ID); err != nil {
				return err
			}
		case "decline", "end":
			if u.ID != nominee {
				if in.Action == "decline" {
					return web.Fail(403, "只有接任人可谢绝")
				}
				if err = identity.RequireAdmin(ctx, tx, u.ID); err != nil {
					return err
				}
			}
			if in.Action == "decline" && state != "pending" {
				return web.Fail(409, "职责已承接，请结束临时访问")
			}
			if state == "pending" {
				next := "withdrawn"
				if in.Action == "decline" {
					next = "declined"
				}
				_, err = tx.Exec(ctx, "update reviewer_handoffs set state=$2,decided_at=now(),decision_by=$3,decision_reason=$4 where id=$1", offer, next, u.ID, in.Reason)
			} else {
				_, err = tx.Exec(ctx, "update reviewer_handoffs set access_ended_at=now(),access_end_reason=$2 where id=$1", offer, in.Reason)
			}
			if err != nil {
				return err
			}
		}
		if err = Touch(ctx, tx, id, p.stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.recovery."+in.Action, id, map[string]any{"offer": offer, "member": nominee, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
