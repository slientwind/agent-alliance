package projects

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// A contributor can propose only an already published revision they can use.
// The proposal discloses a business description to this project, never instructions or secrets.
func (s Server) offerAgent(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Agent, Description    string
		Revision, Days, Calls int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Description = strings.TrimSpace(in.Description)
	if in.Description == "" || utf8.RuneCountInString(in.Description) > 1000 || in.Days < 1 || in.Days > 365 || in.Calls < 1 || in.Calls > 2147483647 || in.Revision < 1 {
		return web.Fail(422, "请填写业务用途、有效天数（1 至 365）及本项目最多可调用的次数")
	}
	project := r.PathValue("id")
	ctx := r.Context()
	var grant string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, project, u.ID)
		if err != nil {
			return err
		}
		if p.Stage == "delivered" || p.Stage == "cancelled" || p.Stage == "adopted" || p.Stage == "failed" {
			return web.Fail(409, "请选择仍在进行的项目")
		}
		var valid bool
		err = tx.QueryRow(ctx, `select exists(select from agents a join projects p on p.id=$1 where a.id=$2 and a.team_id<>p.team_id and agent_admissible(a.id,$3,$4)
 and (a.published_revision=$3 or exists(select from project_agent_grants prior where (prior.project_id,prior.agent_id,prior.revision)=(p.id,a.id,$3)
 and prior.resource_approver is not null and prior.budget_approver is not null)))`, project, in.Agent, in.Revision, u.ID).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return web.Fail(403, "请从自己可使用的其他团队能力中选择已发布修订")
		}
		if _, err = tx.Exec(ctx, "update project_agent_grants set revoked_at=now(),generation=generation+1 where project_id=$1 and agent_id=$2 and revoked_at is null and expires_at<=now()", project, in.Agent); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `insert into project_agent_grants(project_id,agent_id,revision,description,requested_by,expires_at,call_limit)
 values($1,$2,$3,$4,$5,now()+make_interval(days=>$6),$7) on conflict do nothing returning id`, project, in.Agent, in.Revision, in.Description, u.ID, in.Days, in.Calls).Scan(&grant)
		if err == pgx.ErrNoRows {
			return web.Fail(409, "此项目已有该 Agent 的授权，请先核对原授权再更换修订")
		}
		if err != nil {
			return err
		}
		if err = Touch(ctx, tx, project, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.agent.offer", grant, map[string]any{"project": project, "agent": in.Agent, "revision": in.Revision, "calls": in.Calls, "days": in.Days})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": grant})
}

func (s Server) listAgentGrants(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.readAgentGrants(w, r, u, false)
}

func (s Server) pendingAgentGrants(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	return s.readAgentGrants(w, r, u, true)
}

func (s Server) readAgentGrants(w http.ResponseWriter, r *http.Request, u identity.Member, pending bool) error {
	var data json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(jsonb_build_object(
 'id',g.id,'project',p.id,'projectName',case when pm.member_id is not null then p.title end,
 'agent',g.agent_id,'name',v.name,'revision',g.revision,'team',t.name,'description',g.description,'calls',g.call_limit,
 'used',project_agent_grant_usage(g.id),'expires',g.expires_at,
 'resourceApproved',g.resource_approver is not null,'budgetApproved',g.budget_approver is not null,
 'canRenew',pm.member_id is not null and tm.member_id is not null and g.resource_approver is not null and g.budget_approver is not null and agent_admissible(a.id,v.revision,u.id),
 'remotePolicy',case when v.card is not null then v.remote_policy end,
 'operation',agent_output_operation(v),'resource',bind.source_name,'estimate',case when v.tool is not null then '0' else mr.estimate_cny::text end,
 'closed',g.revoked_at is not null or g.expires_at<=statement_timestamp(),'generation',g.generation,
 'projectEnded',life.ended,
 'canResource',coalesce(tm.member_id is not null and u.role='admin',false),
 'canBudget',coalesce(tm.member_id=t.budget_owner,false),'canRevoke',p.owner=u.id or tm.member_id is not null and (u.role='admin' or u.id=t.budget_owner)
 ) order by case when $3 then g.expires_at end,g.created_at desc,g.id),'[]') from project_agent_grants g join projects p on p.id=g.project_id
 join agent_revisions v on(v.agent_id,v.revision)=(g.agent_id,g.revision) join agents a on a.id=g.agent_id join teams t on t.id=a.team_id
 join agent_budget_bindings bind on(bind.agent_id,bind.revision)=(a.id,v.revision)
 left join model_revisions mr on(mr.resource_id,mr.revision)=(v.resource_id,v.resource_revision)
 join members u on u.id=$1 and u.enabled left join team_members tm on tm.team_id=t.id and tm.member_id=u.id
 left join project_members pm on pm.project_id=p.id and pm.member_id=u.id
 cross join lateral (select p.stage in ('delivered','adopted','cancelled','failed') as ended) life
 where ((nullif($2,'')::uuid=p.id and pm.member_id is not null) or ($2='' and tm.member_id is not null and (u.role='admin' or u.id=t.budget_owner)))
 and (not $3 or (not life.ended and g.revoked_at is null and g.expires_at>statement_timestamp()
 and ((g.resource_approver is null and u.role='admin') or (g.budget_approver is null and u.id=t.budget_owner))))`, u.ID, r.PathValue("id"), pending).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}

func (s Server) decideAgentGrant(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason string
		Expected       int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || utf8.RuneCountInString(in.Reason) > 1000 {
		return web.Fail(422, "请说明授权决定的原因")
	}
	ctx := r.Context()
	id := r.PathValue("grant")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var project, stage string
		var admin, payer, owner, resourceApproved, budgetApproved, closed bool
		var generation int
		// Serialize project edits first; Worker authorization locks only the grant at dispatch.
		err := tx.QueryRow(ctx, `select p.id,p.stage from projects p join project_agent_grants g on g.project_id=p.id where g.id=$1 for update of p`, id).Scan(&project, &stage)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `select exists(select from team_members tm where tm.team_id=t.id and tm.member_id=u.id for share) and u.role='admin',
 exists(select from team_members tm where tm.team_id=t.id and tm.member_id=u.id for share) and coalesce(t.budget_owner=u.id,false),
 p.owner=u.id and exists(select from project_members pm where pm.project_id=p.id and pm.member_id=u.id),
 g.resource_approver is not null,g.budget_approver is not null,g.revoked_at is not null or g.expires_at<=clock_timestamp(),g.generation
 from project_agent_grants g join projects p on p.id=g.project_id join agents a on a.id=g.agent_id join teams t on t.id=a.team_id join members u on u.id=$2 and u.enabled where g.id=$1 for update of g for share of t,u`, id, u.ID).Scan(&admin, &payer, &owner, &resourceApproved, &budgetApproved, &closed, &generation)
		if err != nil {
			return err
		}
		var change string
		switch in.Action {
		case "resource":
			if !admin {
				return web.Fail(403, "需要提供方团队的资源管理权限")
			}
			if resourceApproved {
				return web.Fail(409, "此修订的资源已授权")
			}
			change = "resource_approver=$2"
		case "budget":
			if !payer {
				return web.Fail(403, "需要提供方团队预算负责人确认")
			}
			if budgetApproved {
				return web.Fail(409, "承担团队已经确认")
			}
			change = "budget_approver=$2"
		case "revoke":
			if !admin && !payer && !owner {
				return web.Fail(403, "需要项目发起人或提供方授权职责")
			}
			change = "revoked_at=now()"
		default:
			return web.Fail(422, "未知授权操作")
		}
		if in.Action != "revoke" && (stage == "delivered" || stage == "adopted" || stage == "cancelled" || stage == "failed") {
			return web.Fail(409, "项目已结束，无法新增授权")
		}
		if generation != in.Expected || closed {
			return web.Fail(409, "授权状态已变化，请重新核对")
		}
		args := []any{id}
		if in.Action != "revoke" {
			args = append(args, u.ID)
		}
		tag, err := tx.Exec(ctx, "update project_agent_grants set "+change+",generation=generation+1 where id=$1 and revoked_at is null and expires_at>clock_timestamp()", args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(409, "授权状态已变化，请重新核对")
		}
		if err = Touch(ctx, tx, project, stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.agent."+in.Action, id, map[string]any{"reason": in.Reason, "generation": generation + 1})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
