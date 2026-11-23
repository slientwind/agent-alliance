package monitor

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (s Server) policies(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var rows json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('id',t.id,'name',t.name,'days',t.trace_days,'manager',t.trace_manager,'revision',t.trace_policy_revision) order by t.name),'[]') from teams t where $2='admin' or (t.trace_manager=$1 and exists(select from team_members tm where tm.member_id=$1 and tm.team_id=t.id))`, u.ID, u.Role).Scan(&rows)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, rows)
}
func (s Server) policy(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected, Days int
		Manager        *string
		Reason         string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.Days < 1 || in.Days > 365 || web.Blank(in.Reason) {
		return web.Fail(422, "请填写1至365天的保留期限及原因")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var admin, manager bool
		var rev int
		err := tx.QueryRow(ctx, `select m.role='admin',coalesce(t.trace_manager=m.id,false) and exists(select from team_members tm where tm.team_id=t.id and tm.member_id=m.id),t.trace_policy_revision from teams t join members m on m.id=$2 and m.enabled where t.id=$1 for update of t`, id, u.ID).Scan(&admin, &manager, &rev)
		if err != nil {
			return err
		}
		if !admin && (!manager || in.Manager != nil) {
			return web.Fail(403, "需要团队日志策略授权")
		}
		if rev != in.Expected {
			return web.Fail(409, "日志策略已更新，请刷新")
		}
		if in.Manager != nil && *in.Manager != "" {
			var ok bool
			err = tx.QueryRow(ctx, "select exists(select from team_members tm join members m on m.id=tm.member_id and m.enabled and m.kind='person' where tm.team_id=$1 and m.id=$2)", id, *in.Manager).Scan(&ok)
			if err != nil {
				return err
			}
			if !ok {
				return web.Fail(422, "负责人必须是当前团队的有效成员")
			}
		}
		_, err = tx.Exec(ctx, `update teams set trace_days=$1,trace_manager=case when $2::text is null then trace_manager else nullif($2,'')::uuid end,trace_policy_revision=trace_policy_revision+1 where id=$3`, in.Days, in.Manager, id)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "team.trace_policy", id, map[string]any{"revision": rev + 1, "days": in.Days, "managerChange": in.Manager, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) telemetry(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var state json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('waiting',count(*) filter(where outcome='pending' and attempts=0),'retrying',count(*) filter(where outcome='pending' and attempts>0),'delivered',count(*) filter(where outcome='delivered'),'rejected',count(*) filter(where outcome in ('partial','rejected'))) from telemetry_outbox o join runs r on r.id=o.run_id where r.trace_expires_at is null or r.trace_expires_at>now()`).Scan(&state)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, state)
}
