package llmgateway

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (a Admin) listBudgets(w http.ResponseWriter, r *http.Request, _ identity.Member) error {
	rows, err := a.DB.Query(r.Context(), `select jsonb_build_object('scope',coalesce(p.scope,b.scope),
	 'scopeId',nullif(coalesce(p.scope_id,b.scope_id),'00000000-0000-0000-0000-000000000000'::uuid),
	 'revision',coalesce(p.revision,0),'limitMicroCny',p.limit_micro_cny::text,
	 'reservedMicroCny',coalesce(b.reserved_micro_cny,0)::text,'spentMicroCny',coalesce(b.spent_micro_cny,0)::text)
	 from model_budget_policies p full join (select * from model_monthly_budget_totals
	 where month_start=date_trunc('month',now() at time zone 'UTC')::date) b using(scope,scope_id)
	 order by coalesce(p.scope,b.scope),coalesce(p.scope_id,b.scope_id)`)
	if err != nil {
		return err
	}
	return sendJSONRows(w, rows)
}

func (a Admin) configureBudget(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var in struct {
		ScopeID          string          `json:"scopeId"`
		Limit            json.RawMessage `json:"limitMicroCny"`
		ExpectedRevision int             `json:"expectedRevision"`
	}
	if err := web.ReadLimited(w, r, &in, 4<<10); err != nil {
		return err
	}
	scope := r.PathValue("scope")
	id := uuid.Nil
	switch scope {
	case "enterprise":
		if in.ScopeID != "" {
			return web.Fail(422, "企业预算无需指定团队或员工")
		}
	case "team", "member":
		var err error
		id, err = uuid.Parse(in.ScopeID)
		if err != nil || id == uuid.Nil {
			return web.Fail(422, "请选择设置预算的团队或员工")
		}
	default:
		return web.Fail(422, "未知预算范围")
	}
	var limit *int64
	if len(in.Limit) == 0 || in.ExpectedRevision < 0 {
		return web.Fail(422, "请填写预算额度或明确移除限制")
	}
	if string(in.Limit) != "null" {
		var amount string
		if err := json.Unmarshal(in.Limit, &amount); err != nil {
			return web.Fail(422, "请输入非负整数微元额度")
		}
		raw, err := strconv.ParseUint(amount, 10, 63)
		if err != nil {
			return web.Fail(422, "请输入非负整数微元额度")
		}
		parsed := int64(raw)
		limit = &parsed
	}
	ctx := r.Context()
	revision := 0
	err := pgx.BeginFunc(ctx, a.DB, func(tx pgx.Tx) error {
		var month time.Time
		var held, spent int64
		if err := tx.QueryRow(ctx, `select date_trunc('month',now() at time zone 'UTC')::date`).Scan(&month); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into model_monthly_budget_totals(scope,scope_id,month_start) values($1,$2,$3) on conflict do nothing`, scope, id, month); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select reserved_micro_cny,spent_micro_cny from model_monthly_budget_totals
		 where (scope,scope_id,month_start)=($1,$2,$3) for update`, scope, id, month).Scan(&held, &spent); err != nil {
			return err
		}
		var current int
		err := tx.QueryRow(ctx, `select revision from model_budget_policies where (scope,scope_id)=($1,$2) for update`, scope, id).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current != in.ExpectedRevision {
			return web.Fail(409, "预算额度已更新，请刷新后再保存")
		}
		if limit == nil {
			if _, err := tx.Exec(ctx, `delete from model_budget_policies where (scope,scope_id)=($1,$2)`, scope, id); err != nil {
				return err
			}
		} else {
			if err := tx.QueryRow(ctx, `insert into model_budget_policies(scope,scope_id,limit_micro_cny) values($1,$2,$3)
			 on conflict(scope,scope_id) do update set limit_micro_cny=excluded.limit_micro_cny,revision=model_budget_policies.revision+1 returning revision`,
				scope, id, *limit).Scan(&revision); err != nil {
				return err
			}
			if held > math.MaxInt64-spent {
				return errors.New("invalid stored budget balance")
			}
			if err := recordBudgetThresholds(ctx, tx, scope, id.String(), month, *limit, held+spent); err != nil {
				return err
			}
		}
		return database.Audit(ctx, tx, actor.ID, "gateway.budget.configure", scope+"/"+id.String(),
			map[string]any{"limitMicroCny": in.Limit, "revision": revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, map[string]any{"revision": revision})
}
