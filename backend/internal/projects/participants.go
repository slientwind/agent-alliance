package projects

import (
	"context"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func inviteCollaborator(ctx context.Context, tx pgx.Tx, project, member string) error {
	tag, err := tx.Exec(ctx, `insert into project_memberships(project_id,member_id,can_review)
		select $1,id,false from members where id=$2 and enabled and kind='person'
		on conflict(project_id,member_id) do update set recovery_assignment=null,can_review=false,can_view_costs=false
		where project_memberships.recovery_assignment is not null`, project, member)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	var permanent bool
	err = tx.QueryRow(ctx, `select exists(select from project_memberships pm join members m on m.id=pm.member_id
		where pm.project_id=$1 and pm.member_id=$2 and pm.recovery_assignment is null and m.enabled and m.kind='person')`, project, member).Scan(&permanent)
	if err == nil && !permanent {
		err = web.Fail(404, "成员不可用")
	}
	return err
}

func (s Server) removeParticipant(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Expected int }
	if err := web.ReadLimited(w, r, &in, 1024); err != nil {
		return err
	}
	ctx := r.Context()
	member := r.PathValue("member")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "仅发起人可以移除项目成员")
		}
		if member == p.Owner {
			return web.Fail(409, "发起人需要保留项目访问权")
		}
		if in.Expected != p.Revision {
			return web.Fail(409, "成员或项目已更新，请刷新后再移除")
		}
		tag, err := tx.Exec(ctx, "delete from project_memberships where project_id=$1 and member_id=$2", p.ID, member)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		if _, err = tx.Exec(ctx, `delete from trace_grants g using runs r
			where g.run_id=r.id and r.project_id=$1 and g.member_id=$2`, p.ID, member); err != nil {
			return err
		}
		approvalReturned := member == p.Reviewer
		if approvalReturned {
			if _, err = tx.Exec(ctx, "update projects set final_reviewer=owner,reviewer_assignment=null where id=$1", p.ID); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `update reviewer_handoffs set state='superseded',decided_at=now(),decision_by=$2,
				decision_reason='最终确认权已交回发起人' where project_id=$1 and state='pending'`, p.ID, u.ID); err != nil {
				return err
			}
		}
		// Unfinished reviews remain visible in the owner's inbox, with
		// their original findings and version history preserved.
		if _, err = tx.Exec(ctx, `with reassigned as (
			update feedback f set reviewer=$2,revision=f.revision+1 from artifacts a
			where a.id=f.artifact_id and a.project_id=$1 and f.reviewer=$3
			and f.state in ('suggested','required','investigate','verification')
			returning f.id,f.revision,a.current_version
		)
		insert into feedback_events(feedback_id,revision,actor,action,reason,artifact_version)
		select id,revision,$2,'transfer','原审核人成员权限已撤回，交由发起人处理',current_version from reassigned`, p.ID, u.ID, member); err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.revoke", p.ID, map[string]any{"member": member, "approvalReturned": approvalReturned})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
