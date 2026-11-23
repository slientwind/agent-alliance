package knowledge

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/maintenance"
	"alliance/internal/web"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func (s Server) rulesRoutes(m *http.ServeMux, g identity.Server) {
	maintenance.RuleRoutes(m, g, s.DB)
	m.Handle("GET /api/rules/maintenance-inbox", g.Protect(false, s.ruleInbox))
	m.Handle("GET /api/teams/{id}/rules/{rule}/handoff-history", g.Protect(false, s.ruleHandoffHistory))
	m.Handle("GET /api/teams/{id}/rules/{rule}/references", g.Protect(false, s.ruleReferences))
	m.Handle("GET /api/teams/{id}/rules", g.Protect(false, s.rules))
	m.Handle("POST /api/teams/{id}/rules", g.Protect(false, s.writeRule))
	m.Handle("POST /api/teams/{id}/rules/{rule}/publish", g.Protect(true, s.publishRule))
	m.Handle("POST /api/teams/{id}/rules/{rule}/review", g.Protect(false, s.reviewRule))
	m.Handle("GET /api/projects/{id}/search", g.Protect(false, s.search))
}
func (s Server) rules(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select jsonb_build_object('id',r.id,'revision',v.revision,'body',v.body,'expiresAt',v.expires_at,
 'published',coalesce(r.published_revision,0),'publishedBody',pv.body,'publishedExpiresAt',pv.expires_at,'reviewAt',pv.review_at,
 'expired',coalesce(pv.expires_at<=statement_timestamp(),false),'due',coalesce(pv.review_at<=statement_timestamp(),false),
 'lifecycle',r.lifecycle,'disabled',r.disabled,'owner',o.display_name,'mine',r.maintainer=$1 and not r.maintenance_vacant,'creator',creator.display_name,'invited',coalesce(r.proposed_owner=$1,false),'maintenanceVersion',r.maintenance_version,'ownerAvailable',not r.maintenance_vacant and o.enabled and exists(select from team_members ot where (ot.team_id,ot.member_id)=(r.team_id,r.maintainer)))
 from team_rules r join members o on o.id=r.maintainer join members creator on creator.id=r.author join team_members tm on tm.team_id=r.team_id and tm.member_id=$1
 join lateral(select * from rule_versions where rule_id=r.id order by revision desc limit 1)v on true
 left join rule_versions pv on(pv.rule_id,pv.revision)=(r.id,r.published_revision) where r.team_id=$2 order by v.created_at desc,r.id`, u.ID, r.PathValue("id"))
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
func (s Server) writeRule(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		ID, Body  string
		Expected  int
		ExpiresAt *time.Time
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return web.Fail(422, "临时规则截止时间须在未来")
	}
	if web.Blank(in.Body) || len(in.Body) > 4096 {
		return web.Fail(422, "规则正文限 4096 字节")
	}
	id, team := in.ID, r.PathValue("id")
	ctx := r.Context()
	revision := 0
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var teamID string
		if err := tx.QueryRow(ctx, "select team_id from team_members where member_id=$1 and team_id=$2 for share", u.ID, team).Scan(&teamID); err != nil {
			return err
		}
		if id == "" {
			if err := tx.QueryRow(ctx, "insert into team_rules(team_id,author) values($1,$2) returning id", team, u.ID).Scan(&id); err != nil {
				return err
			}
		} else {
			var locked string
			if err := tx.QueryRow(ctx, "select id from team_rules where id=$1 and team_id=$2 for update", id, team).Scan(&locked); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, "select max(revision) from rule_versions where rule_id=$1", id).Scan(&revision); err != nil {
				return err
			}
		}
		if revision != in.Expected {
			return web.Fail(409, "规则草稿已有新版本")
		}
		revision++
		if _, err := tx.Exec(ctx, "insert into rule_versions(rule_id,revision,body,author,expires_at) values($1,$2,$3,$4,$5)", id, revision, in.Body, u.ID, in.ExpiresAt); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "rule.draft", id, map[string]int{"revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]any{"id": id, "revision": revision})
}
func (s Server) publishRule(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Revision int
		Disabled bool
		Expected *int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	id := r.PathValue("rule")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		var lifecycle int
		var expired bool
		var published int
		if err := tx.QueryRow(ctx, `select r.lifecycle,coalesce(v.expires_at<=statement_timestamp(),false),coalesce(r.published_revision,0)
   from team_rules r join rule_versions v on(v.rule_id,v.revision)=(r.id,$4)
   join team_members tm on tm.team_id=r.team_id and tm.member_id=$3
   where r.id=$1 and r.team_id=$2 for update of r for share of tm`, id, r.PathValue("id"), u.ID, in.Revision).Scan(&lifecycle, &expired, &published); err != nil {
			return err
		}
		if in.Expected != nil && *in.Expected != lifecycle {
			return web.Fail(409, "规则状态已更新，请刷新后核对")
		}
		if in.Disabled && in.Revision != published {
			return web.Fail(409, "请停用当前发布版本")
		}
		if !in.Disabled && expired {
			return web.Fail(409, "此版本已经到期，请创建新草稿")
		}
		if _, err := tx.Exec(ctx, `update team_rules set published_revision=$2,disabled=$3,lifecycle=lifecycle+1 where id=$1`, id, in.Revision, in.Disabled); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update rule_versions set review_at=coalesce(review_at,now()+interval '90 days') where rule_id=$1 and revision=$2`, id, in.Revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "rule.publish", id, map[string]any{"revision": in.Revision, "disabled": in.Disabled})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) reviewRule(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Revision, Expected int
		Reason             string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) || len(in.Reason) > 1024 {
		return web.Fail(422, "请填写复查依据，限 1024 字节")
	}
	ctx := r.Context()
	id := r.PathValue("rule")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var lifecycle, revision int
		var usable bool
		if err := tx.QueryRow(ctx, `select r.lifecycle,coalesce(r.published_revision,0),rule_usable(r.id,r.published_revision,$3)
   from team_rules r join team_members tm on tm.team_id=r.team_id and tm.member_id=$3
   where r.id=$1 and r.team_id=$2 and r.maintainer=$3 and not r.maintenance_vacant for update of r for share of tm`, id, r.PathValue("id"), u.ID).Scan(&lifecycle, &revision, &usable); err != nil {
			return err
		}
		if !usable || revision != in.Revision || lifecycle != in.Expected {
			return web.Fail(409, "此版本已变化、停用或到期，请重新核对")
		}
		if _, err := tx.Exec(ctx, "update team_rules set lifecycle=lifecycle+1 where id=$1", id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update rule_versions set review_at=now()+interval '90 days' where rule_id=$1 and revision=$2", id, revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "rule.review", id, map[string]any{"revision": revision, "reason": strings.TrimSpace(in.Reason)})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) search(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 160 {
		return web.Fail(422, "请输入 1 至 160 字节的检索词")
	}
	rows, err := s.DB.Query(r.Context(), `with matches as(select s.id,s.name,v.version,v.extracted,strpos(lower(v.extracted),lower($3)) pos from sources s join source_versions v on(v.source_id,v.version)=(s.id,s.current_version) join project_members m on m.project_id=s.project_id where m.member_id=$1 and s.project_id=$2 and s.revoked_at is null and v.extracted is not null) select id,name,version,pos,substring(extracted from greatest(1,pos-45) for 180) from matches where pos>0 order by pos,name,id limit 20`, u.ID, r.PathValue("id"), q)
	if err != nil {
		return err
	}
	type hit struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Version int    `json:"version"`
		Offset  int    `json:"offset"`
		Excerpt string `json:"excerpt"`
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[hit])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}

// List only pins in the project's adopted plan and only for current project members.
func (s Server) ruleReferences(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select jsonb_build_object('project',p.id,'title',p.title,'plan',pr.plan_version,'revision',pr.rule_revision,'stage',p.stage)
 from team_rules r join team_members tm on(tm.team_id,tm.member_id)=(r.team_id,$3)
 join plan_rules pr on pr.rule_id=r.id join projects p on(p.id,p.plan_version)=(pr.project_id,pr.plan_version)
 join project_members pm on(pm.project_id,pm.member_id)=(p.id,$3)
 where r.team_id=$1 and r.id=$2 order by p.updated_at desc,p.id`, r.PathValue("id"), r.PathValue("rule"), u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}

func (s Server) ruleInbox(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select jsonb_build_object('team',r.team_id,'name',t.name,'id',r.id,'body',left(v.body,90),'action',task.action)
 from team_members tm join members m on m.id=tm.member_id and m.enabled join teams t on t.id=tm.team_id
 join team_rules r on r.team_id=t.id join members o on o.id=r.maintainer
 left join rule_versions published on(published.rule_id,published.revision)=(r.id,r.published_revision)
 join lateral(select body from rule_versions where rule_id=r.id order by (revision=r.published_revision) desc,revision desc limit 1)v on true
 cross join lateral(select case when r.proposed_owner=$1 then 'accept'
 when m.role='admin' and (r.maintenance_vacant or not o.enabled) and r.proposed_owner is null then 'assign'
 when r.maintainer=$1 and not r.maintenance_vacant and not r.disabled then
 case when published.expires_at<=statement_timestamp() then 'expired' when published.review_at<=statement_timestamp() then 'review' end end as action)task
 where tm.member_id=$1 and task.action is not null order by t.name,r.id`, u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
func (s Server) ruleHandoffHistory(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), `select jsonb_build_object('seq',e.seq,'action',e.action,'time',e.at,
 'actor',m.display_name,'target',n.display_name,'reason',case when e.actor=$3 or n.id=$3 then e.detail->>'reason' end)
 from team_rules r join team_members tm on(tm.team_id,tm.member_id)=(r.team_id,$3)
 join audit_events e on e.subject=r.id::text and e.action like 'rule.maintenance.%'
 left join members m on m.id=e.actor left join members n on n.id=coalesce(e.detail->>'target',e.detail->>'member')::uuid
 where r.team_id=$1 and r.id=$2 order by e.seq desc`, r.PathValue("id"), r.PathValue("rule"), u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
