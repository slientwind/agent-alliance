package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (s Server) costAccess(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Member, Reason string
		Expected       int
		Allowed        *bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Allowed == nil || web.Blank(in.Reason) {
		return web.Fail(422, "请明确授权范围及变更原因")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "需要项目发起人授权")
		}
		if p.Revision != in.Expected || in.Member == p.Owner {
			return web.Fail(409, "请核对项目版本与目标成员")
		}
		tag, err := tx.Exec(ctx, "update project_members set can_view_costs=$1 where project_id=$2 and member_id=$3", *in.Allowed, p.ID, in.Member)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(404, "未找到项目成员")
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.cost_access", p.ID, map[string]any{"member": in.Member, "allowed": *in.Allowed, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
