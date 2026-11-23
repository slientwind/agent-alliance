package billing

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func requireOwner(ctx context.Context, tx pgx.Tx, team, member string) error {
	var id string
	err := tx.QueryRow(ctx, `select t.id from teams t join team_members tm on tm.team_id=t.id and tm.member_id=t.budget_owner join members m on m.id=tm.member_id where t.id=$1 and m.id=$2 and m.enabled and m.kind='person' for share of t,tm,m`, team, member).Scan(&id)
	if err == pgx.ErrNoRows {
		return web.Fail(403, "需要该团队的预算管理授权")
	}
	return err
}

func (s Server) assignOwner(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Member, Previous, Reason string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) {
		return web.Fail(422, "请说明预算负责人变更原因")
	}
	ctx := r.Context()
	team := r.PathValue("team")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if in.Member != "" {
			var active bool
			err := tx.QueryRow(ctx, `select m.enabled and m.kind='person' from team_members tm join members m on m.id=tm.member_id where tm.team_id=$1 and tm.member_id=$2 for share of tm,m`, team, in.Member).Scan(&active)
			if err == pgx.ErrNoRows || err == nil && !active {
				return web.Fail(422, "请选择本团队有效成员")
			}
			if err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `update teams set budget_owner=nullif($1,'')::uuid where id=$2 and budget_owner is not distinct from nullif($3,'')::uuid`, in.Member, team, in.Previous)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "团队或当前预算负责人已变化")
		}
		return database.Audit(ctx, tx, u.ID, "budget.owner", team, map[string]string{"previous": in.Previous, "member": in.Member, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
