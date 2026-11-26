package identity

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type teamSpaceMember struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (s Server) teamSpaceRoles(w http.ResponseWriter, r *http.Request, _ Member) error {
	team := r.PathValue("id")
	if !handoffTeamID(team) {
		return web.Fail(404, "找不到团队空间")
	}
	rows, err := s.DB.Query(r.Context(), `select m.id,m.display_name,sm.role
	 from spaces s join team_members tm on tm.team_id=s.team_id
	 join space_members sm on (sm.space_id,sm.member_id)=(s.id,tm.member_id)
	 join members m on m.id=tm.member_id and m.enabled and m.departed_at is null and m.kind='person'
	 where s.team_id=$1 and s.kind='team' and s.archived_at is null and sm.role in ('member','space_admin')
	 order by m.display_name,m.id`, team)
	if err != nil {
		return err
	}
	members, err := pgx.CollectRows(rows, pgx.RowToStructByPos[teamSpaceMember])
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, members)
}

func (s Server) setTeamSpaceRole(w http.ResponseWriter, r *http.Request, actor Member) error {
	team, member := r.PathValue("id"), r.PathValue("member")
	if !handoffTeamID(team) || uuid.Validate(member) != nil {
		return web.Fail(404, "找不到在职团队成员")
	}
	var in struct{ Role, Previous, Reason string }
	if err := web.ReadLimited(w, r, &in, 8192); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if (in.Role != "member" && in.Role != "space_admin") ||
		(in.Previous != "member" && in.Previous != "space_admin") ||
		in.Reason == "" || utf8.RuneCountInString(in.Reason) > 1000 || strings.ContainsRune(in.Reason, 0) {
		return web.Fail(422, "请核对团队空间角色并填写变更原因（最多 1000 字）")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := RequireMemberAdmin(ctx, tx, actor.ID, member); err != nil {
			return err
		}
		var space, role string
		err := tx.QueryRow(ctx, `select s.id,sm.role from spaces s
		 join team_members tm on tm.team_id=s.team_id and tm.member_id=$2
		 join space_members sm on (sm.space_id,sm.member_id)=(s.id,tm.member_id)
		 join members m on m.id=tm.member_id and m.enabled and m.departed_at is null and m.kind='person'
		 where s.kind='team' and s.team_id=$1 and s.archived_at is null and sm.role in ('member','space_admin')
		 for no key update of s,sm`, team, member).Scan(&space, &role)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(404, "找不到在职团队成员")
		}
		if err != nil {
			return err
		}
		if role != in.Previous {
			return web.Fail(409, "空间角色已变化，请刷新后核对")
		}
		if role == in.Role {
			return nil
		}
		if role == "space_admin" {
			var hasOther bool
			if err := tx.QueryRow(ctx, `select exists(select from space_members sm
			 join team_members tm on tm.team_id=$2 and tm.member_id=sm.member_id
			 join members m on m.id=sm.member_id and m.enabled and m.departed_at is null and m.kind='person'
			 where sm.space_id=$1 and sm.role='space_admin' and sm.member_id<>$3)`, space, team, member).
				Scan(&hasOther); err != nil {
				return err
			}
			if !hasOther {
				return web.Fail(409, "至少保留一名在职空间管理员")
			}
		}
		if _, err := tx.Exec(ctx, `update space_members set role=$3,granted_by=$4 where (space_id,member_id)=($1,$2)`,
			space, member, in.Role, actor.ID); err != nil {
			return err
		}
		return database.Audit(ctx, tx, actor.ID, "space.member.role", space,
			map[string]string{"member": member, "previous": role, "role": in.Role, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
