package billing

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type increaseRequest struct {
	Scope, Team, Amount, Reason, Work, Alternative, Run string
	Expected, RunRevision                               int
	Resource                                            string `json:",omitempty"`
}

func (s Server) requestRoutes(m *http.ServeMux, g identity.Server) {
	m.Handle("GET /api/projects/{project}/budget-requests", g.Protect(false, s.requests))
	m.Handle("POST /api/projects/{project}/budget-requests", g.Protect(false, s.submitRequest))
	m.Handle("GET /api/teams/{team}/budget-requests", g.Protect(false, s.requests))
	m.Handle("GET /api/budget-inbox", g.Protect(false, s.budgetInbox))
	m.Handle("GET /api/budget-inbox/{request}", g.Protect(false, s.budgetReceipt))
	m.Handle("POST /api/budget-requests/{request}/decision", g.Protect(false, s.decideRequest))
}

func activeProject(stage string) bool {
	return stage != "delivered" && stage != "adopted" && stage != "cancelled" && stage != "failed"
}

func (s Server) submitRequest(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in increaseRequest
	if err := web.Read(r, &in); err != nil {
		return err
	}
	project, key := r.PathValue("project"), r.Header.Get("Idempotency-Key")
	resourceScope := in.Scope == "resource" || in.Scope == "resource_calls"
	if resourceScope != (in.Resource != "") {
		return web.Fail(422, "请指定对应预算资源")
	}
	if in.Scope == "resource_calls" {
		if n, err := strconv.ParseInt(in.Amount, 10, 32); err != nil || n <= 0 {
			return web.Fail(422, "请填写正整数调用次数")
		}
	}
	if (!resourceScope && in.Scope != "team" && in.Scope != "project") || web.Blank(in.Team) || !ValidAmount(in.Amount) || strings.Trim(in.Amount, "0.") == "" || web.Blank(in.Reason) || web.Blank(in.Work) || web.Blank(in.Alternative) || web.Blank(key) || len(key) > 180 {
		return web.Fail(422, "需要正数追加额度、预算对象、不足原因、剩余工作、替代方案及请求标识")
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	signature := web.Hash(project + string(body))
	ctx := r.Context()
	var id string
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var stage string
		if err := tx.QueryRow(ctx, `select p.stage from projects p join project_members pm on pm.project_id=p.id where p.id=$1 and pm.member_id=$2 for share of p,pm`, project, u.ID).Scan(&stage); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1,0))`, u.ID+"/"+key); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `select id,signature=$3 from budget_requests where requester=$1 and request_key=$2`, u.ID, key, signature).Scan(&id, &same)
		if err == nil {
			if !same {
				return web.Fail(409, "请求标识已用于其他预算申请")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if !activeProject(stage) {
			return web.Fail(409, "项目已结束")
		}
		err = tx.QueryRow(ctx, `insert into budget_requests(project_id,scope,team_id,requester,request_key,signature,requested_increase,baseline_revision,baseline_limit,project_used,project_unknown,reason,remaining_work,alternative,resource_budget,resource_name,project_calls)
   select t.project_id,t.scope,t.team_id,$4,$5,$6,$7,t.revision,t.limit_value,
    case when t.scope='project' then pb.used else
     (select coalesce(sum(amount_cny+case when settled then 0 else greatest(coalesce(reserved_cny,0)-amount_cny,0) end),0) from spend where project_id=t.project_id and team_id=t.team_id and (t.resource_budget is null or resource_budget=t.resource_budget)) end,
    (select count(*) from spend where project_id=t.project_id and (t.scope='project' or team_id=t.team_id) and (t.resource_budget is null or resource_budget=t.resource_budget) and reserved_cny is null and not settled),$8,$9,$10,t.resource_budget,t.resource_name,
    (select count(*) from spend where project_id=t.project_id and (t.scope='project' or team_id=t.team_id) and (t.resource_budget is null or resource_budget=t.resource_budget))
   from budget_request_targets t join project_budget_status pb on pb.project_id=t.project_id
   where t.project_id=$1 and t.scope=$2 and t.team_id=$3 and t.limit_value is not null and t.resource_budget is not distinct from nullif($12,'')::uuid and t.revision=$11
   returning id`, project, in.Scope, in.Team, u.ID, key, signature, in.Amount, in.Reason, in.Work, in.Alternative, in.Expected, in.Resource).Scan(&id)
		if err == pgx.ErrNoRows {
			return web.Fail(409, "预算范围或额度已变化，请重新核对")
		}
		if err != nil {
			return err
		}
		if in.Run != "" {
			tag, err := tx.Exec(ctx, `update budget_requests q set run_id=b.id,control_revision=b.control_revision from budget_blocked_runs b where q.id=$1 and b.id=$2 and b.project_id=q.project_id and b.control_revision=$3`, id, in.Run, in.RunRevision)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "所选运行已离开预算阻塞状态")
			}
		}
		return database.Audit(ctx, tx, u.ID, "budget.request.submit", id, map[string]string{"project": project, "team": in.Team, "scope": in.Scope, "increase": in.Amount})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"id": id})
}

func (s Server) decideRequest(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason string
		Expected       int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if (in.Action != "approve" && in.Action != "reject" && in.Action != "withdraw") || web.Blank(in.Reason) {
		return web.Fail(422, "需要处理动作与依据")
	}
	state := map[string]string{"approve": "approved", "reject": "rejected", "withdraw": "withdrawn"}[in.Action]
	ctx, id := r.Context(), r.PathValue("request")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var project, team, scope, requester, current, amount, who, reason, resource string
		// Finishing a project takes its row lock before retiring its pending requests.
		// Use that order here as well so approval and delivery have one outcome.
		var stage string
		if err := tx.QueryRow(ctx, `select p.stage from budget_requests br join projects p on p.id=br.project_id where br.id=$1 for share of p`, id).Scan(&stage); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `select project_id,team_id,scope,requester,state,requested_increase::text,coalesce(decided_by::text,''),coalesce(decision_reason,''),coalesce(resource_budget::text,'') from budget_requests where id=$1 for update`, id).Scan(&project, &team, &scope, &requester, &current, &amount, &who, &reason, &resource)
		if err != nil {
			return err
		}
		if in.Action == "withdraw" {
			var joined string
			if requester != u.ID {
				return web.Fail(403, "仅申请人可撤回")
			}
			if err := tx.QueryRow(ctx, "select member_id from project_members where project_id=$1 and member_id=$2 for share", project, u.ID).Scan(&joined); err != nil {
				return err
			}
		} else if err := requireOwner(ctx, tx, team, u.ID); err != nil {
			return err
		}
		if current != "pending" {
			if current == state && who == u.ID && reason == in.Reason {
				return nil
			}
			return web.Fail(409, "申请已有处理结论")
		}
		var limit *string
		var revision *int
		if in.Action == "approve" {
			if !activeProject(stage) {
				return web.Fail(409, "项目已结束，无法追加")
			}
			var supported bool
			if err := tx.QueryRow(ctx, "select exists(select from budget_request_targets where project_id=$1 and team_id=$2 and scope=$3 and resource_budget is not distinct from nullif($4,'')::uuid)", project, team, scope, resource).Scan(&supported); err != nil {
				return err
			}
			if !supported {
				return web.Fail(409, "承担预算已变化，请重新核对")
			}
			sql := `with available as (select project_id from project_budgets where project_id=$2 for update skip locked) update project_budgets set limit_cny=limit_cny+$1::numeric,revision=revision+1 where project_id=(select project_id from available) and revision=$3 and limit_cny+$1::numeric<1000000000000 returning limit_cny::text,revision`
			target := project
			if scope == "team" {
				sql = `with available as (select team_id from budgets where team_id=$2 for update skip locked) update budgets set limit_cny=limit_cny+$1::numeric,revision=revision+1 where team_id=(select team_id from available) and revision=$3 and limit_cny+$1::numeric<1000000000000 returning limit_cny::text,revision`
				target = team
			}
			if scope == "resource" || scope == "resource_calls" {
				column, maximum := "limit_cny", "1000000000000"
				if scope == "resource_calls" {
					column, maximum = "call_limit", "2147483648"
				}
				sql = `with available as (select id from resource_budgets where id=$2 for update skip locked) update resource_budgets set ` + column + `=` + column + `+$1::numeric,revision=revision+1 where id=(select id from available) and revision=$3 and ` + column + `+$1::numeric<` + maximum + ` returning ` + column + `::text,revision`
				target = resource
			}
			err := tx.QueryRow(ctx, sql, amount, target, in.Expected).Scan(&limit, &revision)
			if err == pgx.ErrNoRows {
				return web.Fail(409, "预算正在处理其他操作、额度已变化或金额超出范围，请刷新后核对")
			}
			if err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `update budget_requests set state=$2,decided_by=$3,decided_at=now(),decision_reason=$4,applied_limit=$5,applied_revision=$6 where id=$1`, id, state, u.ID, in.Reason, limit, revision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "budget.request."+in.Action, id, map[string]any{"reason": in.Reason, "appliedLimit": limit, "budgetRevision": revision})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

const requestListSQL = `
select coalesce(jsonb_agg(jsonb_build_object(
 'id',br.id,'project',br.project_id,'projectTitle',case when pm.member_id is not null then p.title end,'team',t.name,'scope',br.scope,'amount',br.requested_increase::text,
 'state',br.state,'requester',rq.display_name,'owner',case when owner_member.enabled then owner_member.display_name end,
 'reason',br.reason,'work',br.remaining_work,'alternative',br.alternative,'created',br.created_at,
 'resource',br.resource_budget,'resourceName',br.resource_name,'projectCalls',br.project_calls,'projectUsed',br.project_used::text,'projectUnknown',br.project_unknown,'decision',br.decision_reason,
 'decidedBy',dm.display_name,'decidedAt',br.decided_at,
 'baseline',case when br.scope='project' or auth.finance then br.baseline_limit::text end,
 'applied',case when br.scope='project' or auth.finance then br.applied_limit::text end,
 'currentLimit',case when auth.finance then target.limit_value::text end,
 'afterApproval',case when auth.finance then (target.limit_value+br.requested_increase)::text end,
 'revision',case when auth.finance then target.revision end,
 'changed',target.revision is distinct from br.baseline_revision,
 'canDecide',auth.finance and br.state='pending','canApprove',auth.finance and br.state='pending' and target.limit_value is not null and p.stage not in ('adopted','delivered','cancelled','failed'),
 'run',br.run_id,'controlRevision',br.control_revision,
 'canResume',coalesce(br.state='approved' and pm.member_id is not null and blocked.control_revision=br.control_revision
   and (p.owner=$1 or (blocked.is_change and blocked.actor=$1 and pm.can_review)),false),
 'canWithdraw',br.requester=$1 and pm.member_id is not null and br.state='pending')
 order by (br.state='pending') desc,br.created_at desc,br.id),'[]')
from budget_requests br left join budget_blocked_runs blocked on blocked.id=br.run_id join projects p on p.id=br.project_id join teams t on t.id=br.team_id
join members rq on rq.id=br.requester left join members dm on dm.id=br.decided_by
left join members owner_member on owner_member.id=t.budget_owner
left join team_members tm on tm.team_id=t.id and tm.member_id=$1
left join project_members pm on pm.project_id=p.id and pm.member_id=$1
cross join lateral (select coalesce(tm.member_id=t.budget_owner,false) as finance) auth
left join budget_request_targets target on target.project_id=p.id and target.team_id=t.id and target.scope=br.scope and target.resource_budget is not distinct from br.resource_budget
`

const projectOrTeamRequests = `where (br.project_id=nullif($2,'')::uuid and pm.member_id is not null)
   or (br.team_id=nullif($3,'')::uuid and auth.finance)`

func (s Server) requests(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	project, team := r.PathValue("project"), r.PathValue("team")
	targets := json.RawMessage("[]")
	runs := json.RawMessage("[]")
	var items json.RawMessage
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if project != "" {
			var member string
			if err := tx.QueryRow(ctx, "select member_id from project_members where project_id=$1 and member_id=$2 for share", project, u.ID).Scan(&member); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `select coalesce(jsonb_agg(jsonb_build_object('id',id,'change',is_change,'created',created_at,'revision',control_revision) order by created_at desc),'[]') from budget_blocked_runs where project_id=$1`, project).Scan(&runs); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `select coalesce(jsonb_agg(jsonb_build_object('team',t.team_id,'name',coalesce(t.resource_name,teams.name),'resource',t.resource_budget,'scope',t.scope,'revision',t.revision,
    'owner',case when m.enabled then m.display_name end) order by t.scope,teams.name),'[]')
    from budget_request_targets t join teams on teams.id=t.team_id left join members m on m.id=teams.budget_owner
    where project_id=$1 and limit_value is not null`, project).Scan(&targets); err != nil {
				return err
			}
		} else if err := requireOwner(ctx, tx, team, u.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, requestListSQL+projectOrTeamRequests, u.ID, project, team).Scan(&items)
	})
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, map[string]json.RawMessage{"targets": targets, "items": items, "runs": runs})
}
