package maintenance

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
)

type resource struct {
	db    *pgxpool.Pool
	table string
	kind  string
}

func AgentRoutes(m *http.ServeMux, g identity.Server, db *pgxpool.Pool) {
	resource{db, "agents", "agent"}.routes(m, g, "/api/agents/{id}/maintenance")
}
func RuleRoutes(m *http.ServeMux, g identity.Server, db *pgxpool.Pool) {
	resource{db, "maintained_rules", "rule"}.routes(m, g, "/api/teams/{team}/rules/{id}/maintenance")
}
func (c resource) routes(m *http.ServeMux, g identity.Server, path string) {
	m.Handle("GET "+path, g.Protect(false, c.maintenance))
	m.Handle("POST "+path, g.Protect(false, c.changeMaintenance))
}

// The only table names are the two application constants above. The optional team
// path is checked even when the caller has a membership in both teams.
const scope = " and ($3='' or a.team_id=nullif($3,'')::uuid)"

func (c resource) maintenance(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var data json.RawMessage
	err := c.db.QueryRow(r.Context(), `select jsonb_build_object('owner',o.display_name,'available',active.ok,'version',a.maintenance_version,
 'canNominate',((a.owner=$2 and not a.maintenance_vacant) or (m.role='admin' and not active.ok)),'pending',a.proposed_owner is not null,
 'target',n.display_name,'canAccept',coalesce(a.proposed_owner=$2,false),
 'canCancel',coalesce(a.proposed_owner=$2 or (a.owner=$2 and not a.maintenance_vacant) or a.proposed_by=$2 or (m.role='admin' and not active.ok),false),
 'reason',case when ($2=a.owner and not a.maintenance_vacant) or $2 in(a.proposed_owner,a.proposed_by) or (m.role='admin' and not active.ok) then a.transfer_reason end)
 from `+c.table+` a join team_members tm on tm.team_id=a.team_id and tm.member_id=$2 join members m on m.id=tm.member_id and m.enabled
 join members o on o.id=a.owner left join members n on n.id=a.proposed_owner
 cross join lateral(select not a.maintenance_vacant and o.enabled and exists(select from team_members t where(t.team_id,t.member_id)=(a.team_id,a.owner)) as ok) active where a.id=$1 and not a.removed`+scope, r.PathValue("id"), u.ID, r.PathValue("team")).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}

func (c resource) changeMaintenance(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected              int
		Action, Email, Reason string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Expected < 1 || in.Reason == "" || len(in.Reason) > 4096 {
		return web.Fail(422, "请核对交接版本并说明原因")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := database.WithTx(ctx, c.db, func(tx pgx.Tx) error {
		var team, owner, role string
		var target, proposer *string
		var version int
		var oldAvailable, vacant bool
		err := tx.QueryRow(ctx, `select a.team_id,a.owner,m.role,a.proposed_owner,a.proposed_by,a.maintenance_version,a.maintenance_vacant,
  not a.maintenance_vacant and o.enabled and exists(select from team_members t where t.team_id=a.team_id and t.member_id=a.owner)
  from `+c.table+` a join team_members tm on tm.team_id=a.team_id and tm.member_id=$2 join members m on m.id=tm.member_id and m.enabled join members o on o.id=a.owner
  where a.id=$1 and not a.removed`+scope+` for update of a for share of tm,m,o`, id, u.ID, r.PathValue("team")).Scan(&team, &owner, &role, &target, &proposer, &version, &vacant, &oldAvailable)
		if err != nil {
			return err
		}
		if version != in.Expected {
			return web.Fail(409, "交接已变化，请重新核对")
		}
		canAssign := owner == u.ID && !vacant || role == "admin" && !oldAvailable
		details := map[string]any{"previous": owner, "version": version + 1, "reason": in.Reason}
		switch in.Action {
		case "nominate":
			if !canAssign {
				return web.Fail(403, "需要当前维护者或有权处理缺位的管理员")
			}
			if target != nil {
				return web.Fail(409, "请先处理已有交接")
			}
			var nominee string
			err = tx.QueryRow(ctx, `select m.id from members m join team_members tm on tm.member_id=m.id where tm.team_id=$1 and m.email=$2 and m.enabled and m.kind='person' for share of m,tm`, team, strings.ToLower(strings.TrimSpace(in.Email))).Scan(&nominee)
			if err != nil {
				return err
			}
			if nominee == owner && !vacant {
				return web.Fail(422, "请选择另一位有效团队成员")
			}
			_, err = tx.Exec(ctx, "update "+c.table+" set proposed_owner=$2,proposed_by=$3,transfer_reason=$4,maintenance_version=maintenance_version+1 where id=$1", id, nominee, u.ID, in.Reason)
			details["target"] = nominee
		case "accept", "cancel":
			if target == nil || proposer == nil {
				return web.Fail(409, "当前没有待接受的交接")
			}
			details["target"] = *target
			next := owner
			if in.Action == "accept" {
				if u.ID != *target {
					return web.Fail(403, "仅接任人可接受维护职责")
				}
				var authorRole string
				err = tx.QueryRow(ctx, `select m.role from members m join team_members tm on tm.member_id=m.id where tm.team_id=$1 and m.id=$2 and m.enabled for share of tm,m`, team, *proposer).Scan(&authorRole)
				if err == pgx.ErrNoRows {
					return web.Fail(409, "交接发起人权限已变化")
				}
				if err != nil {
					return err
				}
				if (*proposer != owner || vacant) && (authorRole != "admin" || oldAvailable) {
					return web.Fail(409, "原维护者已恢复或发起人权限变化，请重新交接")
				}
				next = u.ID
			} else if !canAssign && u.ID != *target && u.ID != *proposer {
				return web.Fail(403, "没有撤销此交接的权限")
			}
			_, err = tx.Exec(ctx, "update "+c.table+" set owner=$2,maintenance_vacant=(maintenance_vacant and $3),proposed_owner=null,proposed_by=null,transfer_reason=null,maintenance_version=maintenance_version+1 where id=$1", id, next, in.Action == "cancel")
		default:
			return web.Fail(422, "未知交接操作")
		}
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, c.kind+".maintenance."+in.Action, id, details)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
