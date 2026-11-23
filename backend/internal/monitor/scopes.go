package monitor

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

//go:embed scope_report.sql
var scopeReportQuery string

//go:embed grant_catalog.sql
var grantCatalogQuery string

func (s Server) scopeRoutes(m *http.ServeMux, g identity.Server) {
	m.Handle("GET /api/admin/management-scopes", g.Protect(true, s.adminScopes))
	m.Handle("POST /api/admin/management-grants", g.Protect(true, s.grant))
	m.Handle("PATCH /api/admin/management-grants/{id}/export", g.Protect(true, s.exportPolicy))
	m.Handle("DELETE /api/admin/management-grants/{id}", g.Protect(true, s.revoke))
	m.Handle("PATCH /api/admin/management-grants/{id}/details", g.Protect(true, s.detailPolicy))
	m.Handle("GET /api/management-scopes/{id}/members", g.Protect(false, s.detailMembers))
	m.Handle("GET /api/management-scopes/{id}/tasks", g.Protect(false, s.detailTasks))
	m.Handle("GET /api/management-scopes", g.Protect(false, s.myScopes))
	m.Handle("GET /api/management-scopes/{id}/exports", g.Protect(false, s.recentExports))
	m.Handle("POST /api/management-scopes/{id}/exports", g.Protect(false, s.createExport))
	m.Handle("GET /api/management-scopes/{id}/exports/{export}", g.Protect(false, s.downloadExport))
	m.Handle("GET /api/management-scopes/{id}/summary", g.Protect(false, s.scopeReport))
}

func (s Server) adminScopes(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), grantCatalogQuery, u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) grant(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Member, Kind, Scope, Reason                          string
		Costs, Export, Details, DetailCosts, FinancialExport bool
		Days                                                 int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if in.FinancialExport && !in.Costs {
		return web.Fail(422, "逐笔费用导出须同时开放该范围费用")
	}
	if in.DetailCosts && !in.Details {
		return web.Fail(422, "查看逐任务费用需要成员与任务明细授权")
	}
	if (in.Kind != "team" && in.Kind != "department" && in.Kind != "enterprise") || (in.Kind == "enterprise" && in.Scope != "enterprise") || web.Blank(in.Scope) || web.Blank(in.Member) || web.Blank(in.Reason) || len(in.Reason) > 2000 || in.Days < 1 || in.Days > 365 {
		return web.Fail(422, "需要明确成员、范围、授权原因及 1 至 365 天的期限")
	}
	var id string
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireMemberAdmin(ctx, tx, u.ID, in.Member); err != nil {
			return err
		}
		var member string
		if err := tx.QueryRow(ctx, "select id from members where enabled and kind='person' and id=$1", in.Member).Scan(&member); err != nil {
			return err
		}
		var team, dept *string
		var enterprise *bool
		switch in.Kind {
		case "team":
			team = &in.Scope
		case "department":
			dept = &in.Scope
		case "enterprise":
			instance := true
			enterprise = &instance
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select from teams where id=$1)
			or exists(select from departments where id=$2)
			or exists(select from enterprise where id=$3)`, team, dept, enterprise).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return web.Fail(404, "范围不存在")
		}
		if _, err := tx.Exec(ctx, `update management_grants set revoked_at=expires_at,revoked_reason='到期后重新授权'
			where member_id=$1 and (team_id=$2 or department_id=$3 or enterprise_id=$4) and revoked_at is null and expires_at<=now()`, member, team, dept, enterprise); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `insert into management_grants(member_id,team_id,department_id,include_costs,allow_export,reason,granted_by,expires_at,allow_details,detail_costs,enterprise_id,financial_export)
			values($1,$2,$3,$4,$5,$6,$7,now()+make_interval(days=>$8),$9,$10,$11,$12) returning id`, member, team, dept, in.Costs, in.Export, in.Reason, u.ID, in.Days, in.Details, in.DetailCosts, enterprise, in.FinancialExport).Scan(&id)
		if err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "management.grant", id, in)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": id})
}

func (s Server) revoke(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Reason string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) || len(in.Reason) > 2000 {
		return web.Fail(422, "请填写撤销原因")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, "update management_grants set revoked_at=now(),revoked_reason=$2 where id=$1 and revoked_at is null", id, in.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "授权已结束或不存在")
		}
		return database.Audit(ctx, tx, u.ID, "management.revoke", id, in)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s Server) myScopes(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object('id',g.id,'name',n.name,
		'kind',n.kind,'costs',g.include_costs,'export',g.allow_export,'financialExport',g.financial_export,'details',g.allow_details,'detailCosts',g.detail_costs,'expiresAt',g.expires_at)
		order by (g.enterprise_id is true),n.name,g.id),'[]')
		from management_grants g join members m on m.id=g.member_id and m.enabled
		join management_grant_names n on n.id=g.id
		where g.member_id=$1 and g.revoked_at is null and g.expires_at>now()`, u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) scopeReport(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	for k := range r.URL.Query() {
		if k != "from" && k != "to" && k != "days" {
			return web.Fail(422, "范围汇总仅支持时间筛选")
		}
	}
	start, end, err := usageWindow(r)
	if err != nil {
		return err
	}
	var result json.RawMessage
	err = s.DB.QueryRow(r.Context(), scopeReportQuery, r.PathValue("id"), u.ID, start, end).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}
