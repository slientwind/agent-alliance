package identity

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"net/http"
)

func (s Server) orgRoutes(m *http.ServeMux) {
	m.Handle("GET /api/teams", s.Protect(false, s.teams))
	m.Handle("POST /api/admin/teams", s.Protect(true, s.newTeam))
	m.Handle("PUT /api/admin/teams/{id}/members/{member}", s.Protect(true, s.assign))
	m.Handle("GET /api/admin/teams/{id}/space-roles", s.Protect(true, s.teamSpaceRoles))
	m.Handle("PUT /api/admin/teams/{id}/space-roles/{member}", s.Protect(true, s.setTeamSpaceRole))
	m.Handle("POST /api/admin/departments", s.Protect(true, s.newDepartment))
	m.Handle("PUT /api/admin/members/{id}/department", s.Protect(true, s.department))
	m.Handle("GET /api/directory", s.Protect(false, s.directory))
}
func (s Server) teams(w http.ResponseWriter, r *http.Request, u Member) error {
	rows, err := s.DB.Query(r.Context(), `select t.id,t.name,t.id=e.default_team,t.budget_owner,case when t.budget_owner=$1 then (select count(*) from budget_requests br where br.team_id=t.id and br.state='pending') else 0 end,exists(select from team_members tm where tm.team_id=t.id and tm.member_id=$1) from teams t cross join enterprise e where $2='admin' or exists(select from team_members tm where tm.team_id=t.id and tm.member_id=$1) order by t.name`, u.ID, u.Role)
	if err != nil {
		return err
	}
	type team struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Default     bool    `json:"default"`
		BudgetOwner *string `json:"budgetOwner"`
		Pending     int     `json:"budgetPending"`
		Joined      bool    `json:"joined"`
	}
	v, err := pgx.CollectRows(rows, pgx.RowToStructByPos[team])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, v)
}
func (s Server) newTeam(w http.ResponseWriter, r *http.Request, u Member) error {
	return s.newOrg(w, r, u, true)
}
func (s Server) newDepartment(w http.ResponseWriter, r *http.Request, u Member) error {
	return s.newOrg(w, r, u, false)
}
func (s Server) newOrg(w http.ResponseWriter, r *http.Request, u Member, team bool) error {
	var in struct{ Name string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Name) || len(in.Name) > 180 {
		return web.Fail(422, "需要有效名称")
	}
	ctx := r.Context()
	var id string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		sql := "insert into departments(name) values($1) returning id"
		if team {
			sql = "insert into teams(name) values($1) returning id"
		}
		if err := tx.QueryRow(ctx, sql, in.Name).Scan(&id); err != nil {
			return err
		}
		if team {
			if _, err := tx.Exec(ctx, "insert into team_members values($1,$2)", id, u.ID); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "organization.create", id, map[string]bool{"team": team})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": id})
}
func (s Server) assign(w http.ResponseWriter, r *http.Request, u Member) error {
	var in struct {
		Joined bool
		Reason string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Reason) {
		return web.Fail(422, "请说明团队成员变更原因")
	}
	ctx := r.Context()
	team, member := r.PathValue("id"), r.PathValue("member")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := RequireMemberAdmin(ctx, tx, u.ID, member); err != nil {
			return err
		}
		if in.Joined {
			tag, err := tx.Exec(ctx, "insert into team_members select t.id,m.id from teams t,members m where t.id=$1 and m.id=$2 and m.enabled and m.kind='person' on conflict do nothing", team, member)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return web.Fail(409, "成员已在团队中或不可用")
			}
		} else {
			if _, err := tx.Exec(ctx, "delete from team_members where team_id=$1 and member_id=$2", team, member); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, u.ID, "team.membership", team, map[string]any{"member": member, "joined": in.Joined, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) department(w http.ResponseWriter, r *http.Request, u Member) error {
	var in struct {
		Department, Reason string
		Previous           *string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := RequireMemberAdmin(ctx, tx, u.ID, r.PathValue("id")); err != nil {
			return err
		}
		if in.Previous != nil && web.Blank(in.Reason) {
			return web.Fail(422, "请说明部门调整原因")
		}
		var current string
		if err := tx.QueryRow(ctx, "select coalesce(department_id::text,'') from members where id=$1 and enabled and kind='person'", r.PathValue("id")).Scan(&current); err != nil {
			return err
		}
		if in.Previous != nil && *in.Previous != current {
			return web.Fail(409, "部门归属已变化，请刷新后核对")
		}
		if in.Department != "" {
			var exists bool
			if err := tx.QueryRow(ctx, "select exists(select from departments where id=$1)", in.Department).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return web.Fail(422, "部门不存在")
			}
		}
		tag, err := tx.Exec(ctx, "update members set department_id=nullif($1,'')::uuid where id=$2", in.Department, r.PathValue("id"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		return database.Audit(ctx, tx, u.ID, "department.assign", r.PathValue("id"), map[string]string{"department": in.Department, "previous": current, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) directory(w http.ResponseWriter, r *http.Request, u Member) error {
	rows, err := s.DB.Query(r.Context(), `select id,display_name from members where enabled and kind='person' order by display_name,id`)
	if err != nil {
		return err
	}
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	v, err := pgx.CollectRows(rows, pgx.RowToStructByPos[entry])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, v)
}
