package resources

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (c Catalog) state(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason string
		Expected       *int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) {
		return web.Fail(422, "请填写变更原因")
	}
	var disabled, emergency, removed bool
	switch in.Action {
	case "enable":
	case "disable":
		disabled = true
	case "emergency-stop":
		disabled = true
		emergency = true
	case "remove":
		disabled = true
		removed = true
	default:
		return web.Fail(422, "未知资源动作")
	}
	isModel := r.PathValue("kind") == "models"
	if !isModel && r.PathValue("kind") != "agents" || isModel && removed {
		return web.Fail(422, "资源类型或操作不支持")
	}
	id := r.PathValue("id")
	ctx := r.Context()
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var sql string
		if isModel {
			var current int
			if err := tx.QueryRow(ctx, `select m.lifecycle from model_resources m join team_members tm on tm.team_id=m.team_id and tm.member_id=$2 where m.id=$1 for update of m for share of tm`, id, u.ID).Scan(&current); err != nil {
				return err
			}
			if in.Expected != nil && *in.Expected != current {
				return web.Fail(409, "资源状态已变化，请刷新后核对")
			}
			sql = "update model_resources set enabled=not $1,emergency=$2,lifecycle=lifecycle+1 where id=$3"
		} else {
			sql = "update agents set disabled=$1,emergency=$2,removed=" + map[bool]string{true: "true", false: "false"}[removed] + " where id=$3 and not removed"
		}
		tag, err := tx.Exec(ctx, sql, disabled, emergency, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		return database.Audit(ctx, tx, u.ID, r.PathValue("kind")+"."+in.Action, id, map[string]string{"reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (c Catalog) evidence(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var raw []byte
	err := c.DB.QueryRow(r.Context(), `select coalesce((select jsonb_agg(jsonb_build_object('id',v.id,'revision',v.revision,'status',v.current_status,'remoteState',v.remote_status,'canReuseSample',not a.disabled and (`+reusableTrial+`),'canCheck',v.actor=$2 and v.status='remote','cancelRequested',v.cancel_requested,'createdAt',v.created_at,'endedAt',v.ended_at,'tokens',v.tokens,'reviewDue',case when v.status='passed' then v.ended_at+interval '90 days' end,'withdrawn',wd.reason,'canWithdraw',coalesce(v.status='passed' and wd.trial_id is null and(v.actor=$2 or c.reviewer=$2 or (a.owner=$2 and not a.maintenance_vacant)),false),'checkName',c.name,'verdict',vr.passed,'reason',vr.reason,'reviewedBy',reviewer.display_name,'reviewedAt',vr.created_at,'file',case when v.actor=$2 or c.reviewer=$2 then f.filename end,'picture',case when v.actor=$2 or c.reviewer=$2 then pic.filename end, 'output',case when v.actor=$2 or c.reviewer=$2 then v.evidence end) order by v.created_at desc) from trial_results v left join agent_checks c on c.id=v.check_id left join validation_reviews vr on vr.trial_id=v.id left join members reviewer on reviewer.id=vr.actor left join validation_files f on f.trial_id=v.id left join validation_pictures pic on pic.trial_id=v.id left join validation_withdrawals wd on wd.trial_id=v.id where v.agent_id=a.id),'[]') from agents a join team_members t on t.team_id=a.team_id where a.id=$1 and t.member_id=$2`, r.PathValue("id"), u.ID).Scan(&raw)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(raw)
	return err
}
