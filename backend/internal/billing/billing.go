package billing

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"regexp"
)

var money = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,6})?$`)

func ValidAmount(raw string) bool { return money.MatchString(raw) }

type Operation struct {
	SourceRevision                                 int
	SourceName                                     string
	Key, Kind, Actor, Team, Project, Run, Resource string
	Estimate                                       *string
	AllowUnknown                                   bool
}

func Reserve(ctx context.Context, tx pgx.Tx, op Operation) error {
	if op.Estimate == nil && !op.AllowUnknown {
		return web.Fail(409, "未知费用缺少预授权")
	}
	if _, err := tx.Exec(ctx, "insert into budgets(team_id) values($1) on conflict do nothing", op.Team); err != nil {
		return err
	}
	var limit *string
	var used string
	if err := tx.QueryRow(ctx, "select limit_cny::text, used_cny::text from budgets where team_id=$1 for update", op.Team).Scan(&limit, &used); err != nil {
		return err
	}
	var existing bool
	if err := tx.QueryRow(ctx, "select exists(select from spend where op_key=$1)", op.Key).Scan(&existing); err != nil {
		return err
	}
	if existing {
		return nil
	}
	if err := enforceLimit(ctx, tx, "团队", used, limit, op.Estimate); err != nil {
		return err
	}
	if op.Project != "" {
		if err := tx.QueryRow(ctx, "select limit_cny::text, used_cny::text from project_budgets where project_id=$1 for update", op.Project).Scan(&limit, &used); err != nil {
			return err
		}
		if err := enforceLimit(ctx, tx, "项目", used, limit, op.Estimate); err != nil {
			return err
		}
	}
	var calls *int
	var usedCalls int64
	if err := tx.QueryRow(ctx, "select limit_cny::text, used_cny::text, call_limit, used_calls from resource_budgets where id=$1 and team_id=$2 for update", op.Resource, op.Team).Scan(&limit, &used, &calls, &usedCalls); err != nil {
		return err
	}
	if err := enforceLimit(ctx, tx, "资源", used, limit, op.Estimate); err != nil {
		return err
	}
	if calls != nil && usedCalls >= int64(*calls) {
		return web.Fail(409, "资源调用次数已用完，请追加次数或调整方案")
	}
	_, err := tx.Exec(ctx, `insert into spend(op_key,kind,actor,team_id,project_id,run_id,reserved_cny,unknown_authorized,resource_budget,source_revision,source_name) values($1,$2,$3,$4,nullif($5,'')::uuid,nullif($6,'')::uuid,$7,$8,$9,$10,$11)`, op.Key, op.Kind, op.Actor, op.Team, op.Project, op.Run, op.Estimate, op.AllowUnknown, op.Resource, op.SourceRevision, op.SourceName)
	return err
}

func enforceLimit(ctx context.Context, tx pgx.Tx, scope, used string, limit, estimate *string) error {
	if limit == nil {
		return nil
	}
	var over bool
	err := tx.QueryRow(ctx, `select budget_amount_short($1::numeric,coalesce($2::numeric,0),$2::numeric is null,$3::numeric)`, used, estimate, limit).Scan(&over)
	if err != nil {
		return err
	}
	if over {
		return web.Fail(409, scope+"预算不足，需预算负责人追加或调整方案")
	}
	return nil
}

type Server struct{ DB *pgxpool.Pool }

func (s Server) Routes(m *http.ServeMux, g identity.Server) {
	s.requestRoutes(m, g)
	m.Handle("PUT /api/teams/{team}/resource-budgets/{resource}", g.Protect(false, s.configure))
	m.Handle("GET /api/admin/budget", g.Protect(false, s.view))
	m.Handle("PUT /api/admin/budget", g.Protect(false, s.configure))
	m.Handle("POST /api/admin/bills", g.Protect(false, s.reconcile))
	m.Handle("GET /api/teams/{team}/budget", g.Protect(false, s.view))
	m.Handle("PUT /api/teams/{team}/budget", g.Protect(false, s.configure))
	m.Handle("PUT /api/teams/{team}/budget-owner", g.Protect(true, s.assignOwner))
	m.Handle("PUT /api/projects/{id}/budget", g.Protect(false, s.configure))
}
func (s Server) view(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var raw []byte
	err := s.DB.QueryRow(r.Context(), `
    select jsonb_build_object(
      'revision',coalesce(b.revision,0),'limit',b.limit_cny::text,
      'warningPercent',coalesce(b.warning_percent,80),'currency','CNY',
      'warning',coalesce((s.recorded+s.reserved)>=b.limit_cny*b.warning_percent/100,false),
      'exhausted',coalesce((s.recorded+s.reserved)>=b.limit_cny,false),
      'settledAmount',s.recorded::text,'reserved',s.reserved::text,'unknown',s.unknown,
      'resources',coalesce((select jsonb_agg(jsonb_build_object('id',rb.id,'kind',rb.kind,'name',rb.name,'model',rb.model_id,'agent',rb.agent_id,
        'limit',rb.limit_cny::text,'revision',rb.revision,'callLimit',rb.call_limit,'warningPercent',rb.warning_percent,'used',rb.used::text,'recorded',rb.recorded::text,
        'reserved',rb.reserved::text,'calls',rb.calls,'unknown',rb.unknown,
        'warning',coalesce(rb.used>=rb.limit_cny*rb.warning_percent/100,false) or coalesce(rb.calls::numeric>=rb.call_limit::numeric*rb.warning_percent/100,false),
        'exhausted',coalesce(rb.used>=rb.limit_cny,false) or coalesce(rb.calls>=rb.call_limit,false)) order by rb.kind,rb.name,rb.id)
        from resource_budget_status rb where rb.team_id=t.id),'[]'),
      'projects',coalesce((
        select jsonb_agg(jsonb_build_object('id',p.id,'title',case when pm.member_id is not null then p.title end,
          'limit',pb.limit_cny::text,'revision',pb.revision,'warningPercent',pb.warning_percent,
          'used',pb.used::text,'warning',pb.warning,'exhausted',pb.exhausted) order by p.created_at desc)
        from projects p join project_budget_status pb on pb.project_id=p.id
        left join project_members pm on pm.project_id=p.id and pm.member_id=$2
        where p.team_id=t.id),'[]'),
      'items',coalesce((
        select jsonb_agg(jsonb_build_object('id',id,'kind',kind,'resource',resource_budget,'sourceName',source_name,'sourceRevision',source_revision,'estimate',reserved_cny::text,
          'amount',amount_cny::text,'settled',settled,'unknown',reserved_cny is null,'createdAt',created_at)
          order by created_at desc)
        from (select * from spend where team_id=t.id order by created_at desc limit 100) recent),'[]'))
    from teams t join team_members tm on tm.team_id=t.id and tm.member_id=t.budget_owner
    left join budgets b on b.team_id=t.id
    cross join lateral (select coalesce(sum(amount_cny),0) recorded,
      coalesce(sum(greatest(reserved_cny-amount_cny,0)) filter(where not settled),0) reserved,
      count(*) filter(where reserved_cny is null and not settled) unknown from spend where team_id=t.id) s
    where t.id=coalesce(nullif($1,'')::uuid,(select default_team from enterprise)) and tm.member_id=$2`, r.PathValue("team"), u.ID).Scan(&raw)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(raw)
	return err
}
func (s Server) configure(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Limit                    *string
		CallLimit                *int32
		WarningPercent, Expected int
		Reason                   string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if (in.CallLimit != nil && *in.CallLimit < 0) || (in.Limit != nil && !ValidAmount(*in.Limit)) || in.WarningPercent < 1 || in.WarningPercent > 100 || web.Blank(in.Reason) {
		return web.Fail(422, "请输入非负十进制金额、提醒比例和调整原因")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var team string
		project := r.PathValue("id")
		if project != "" {
			if err := tx.QueryRow(ctx, "select team_id from projects where id=$1", project).Scan(&team); err != nil {
				return err
			}
		} else if team = r.PathValue("team"); team == "" {
			if err := tx.QueryRow(ctx, "select default_team from enterprise").Scan(&team); err != nil {
				return err
			}
		}
		if err := requireOwner(ctx, tx, team, u.ID); err != nil {
			return err
		}
		if resource := r.PathValue("resource"); resource != "" {
			tag, err := tx.Exec(ctx, `update resource_budgets set limit_cny=$1,call_limit=$2,warning_percent=$3,revision=revision+1 where id=$4 and team_id=$5 and revision=$6`, in.Limit, in.CallLimit, in.WarningPercent, resource, team, in.Expected)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "资源预算或版本已变化")
			}
			return database.Audit(ctx, tx, u.ID, "budget.resource.configure", resource, map[string]any{"limit": in.Limit, "callLimit": in.CallLimit, "warningPercent": in.WarningPercent, "reason": in.Reason})
		}
		if project != "" {
			tag, err := tx.Exec(ctx, `update project_budgets set limit_cny=$1,warning_percent=$2,revision=revision+1 where project_id=$3 and revision=$4`, in.Limit, in.WarningPercent, project, in.Expected)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "项目预算配置已变化")
			}
			return database.Audit(ctx, tx, u.ID, "budget.project.configure", project, map[string]any{"limit": in.Limit, "warningPercent": in.WarningPercent, "reason": in.Reason})
		}
		if _, err := tx.Exec(ctx, "insert into budgets(team_id,revision) values($1,0) on conflict do nothing", team); err != nil {
			return err
		}
		var rev int
		err := tx.QueryRow(ctx, "select revision from budgets where team_id=$1 for update", team).Scan(&rev)
		if err != nil {
			return err
		}
		if rev != in.Expected {
			return web.Fail(409, "预算配置已变化")
		}
		if _, err = tx.Exec(ctx, `update budgets set limit_cny=$2,warning_percent=$3,revision=revision+1 where team_id=$1`, team, in.Limit, in.WarningPercent); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "budget.configure", team, map[string]any{"warningPercent": in.WarningPercent, "limit": in.Limit, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) reconcile(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Line, Spend, Amount string
		Final               bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Line) || !ValidAmount(in.Amount) {
		return web.Fail(422, "需要账单行唯一标识与非负增量金额")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var team string
		var project *string
		var settled bool
		if err := tx.QueryRow(ctx, "select team_id,settled,project_id from spend where id=$1 for update", in.Spend).Scan(&team, &settled, &project); err != nil {
			return err
		}
		if err := requireOwner(ctx, tx, team, u.ID); err != nil {
			return err
		}
		var rev int
		if err := tx.QueryRow(ctx, "select revision from budgets where team_id=$1 for update", team).Scan(&rev); err != nil {
			return err
		}
		if project != nil {
			if err := tx.QueryRow(ctx, "select revision from project_budgets where project_id=$1 for update", *project).Scan(&rev); err != nil {
				return err
			}
		}
		var match bool
		err := tx.QueryRow(ctx, "select spend_id=$1 and amount_cny=$2 and final=$3 from bill_rows where team_id=$4 and line_key=$5", in.Spend, in.Amount, in.Final, team, in.Line).Scan(&match)
		if err == nil {
			if !match {
				return web.Fail(409, "账单行已用于其他结算")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if settled {
			return web.Fail(409, "调用已最终结算")
		}
		if _, err = tx.Exec(ctx, "insert into bill_rows(team_id,line_key,spend_id,amount_cny,final,actor) values($1,$2,$3,$4,$5,$6)", team, in.Line, in.Spend, in.Amount, in.Final, u.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update spend set amount_cny=amount_cny+$1,settled=$2 where id=$3", in.Amount, in.Final, in.Spend); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "budget.bill", in.Spend, map[string]any{"line": in.Line, "amount": in.Amount, "final": in.Final})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
