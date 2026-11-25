package llmgateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrBudgetExceeded  = errors.New("model monthly budget exceeded")
	ErrChargeConflict  = errors.New("model charge conflicts with existing operation")
	ErrBudgetForbidden = errors.New("model budget unavailable for this member and team")
)

type Budget struct {
	DB interface {
		Begin(context.Context) (pgx.Tx, error)
	}
}

type Reservation struct {
	OperationKey string
	TeamID       string
	MemberID     string
	PriceID      string
	Tier         Tier
	Estimate     Usage
}

type charge struct {
	team, member, price, state string
	tier                       Tier
	month                      time.Time
	held                       int64
}

// Reserve returns true only for the transaction that created the charge.
// A retried worker can check the ledger without submitting a second call.
func (b Budget) Reserve(ctx context.Context, in Reservation) (bool, error) {
	if !in.Tier.Valid() || len(in.OperationKey) == 0 || len(in.OperationKey) > 180 {
		return false, errors.New("invalid model charge")
	}
	var acquired bool
	err := pgx.BeginFunc(ctx, b.DB, func(tx pgx.Tx) error {
		price, err := readPrice(ctx, tx, in.PriceID)
		if err != nil {
			return err
		}
		hold, err := price.CostMicroCNY(in.Estimate)
		if err != nil {
			return err
		}
		c := charge{team: in.TeamID, member: in.MemberID, price: in.PriceID, tier: in.Tier, held: hold}
		var member string
		if err := tx.QueryRow(ctx, `select m.id from team_members tm join members m on m.id=tm.member_id
		 where tm.team_id=$1 and tm.member_id=$2 and m.enabled and m.kind='person' for share of tm,m`, c.team, c.member).Scan(&member); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrBudgetForbidden
			}
			return err
		}
		err = tx.QueryRow(ctx, `insert into model_gateway_charges(operation_key,team_id,member_id,price_id,tier,month_start,reserved_micro_cny)
		 values($1,$2,$3,$4,$5,date_trunc('month',now() at time zone 'UTC')::date,$6)
		 on conflict do nothing returning month_start`, in.OperationKey, c.team, c.member, c.price, c.tier, hold).Scan(&c.month)
		if errors.Is(err, pgx.ErrNoRows) {
			var same bool
			err = tx.QueryRow(ctx, `select team_id=$2 and member_id=$3 and price_id=$4 and tier=$5 and reserved_micro_cny=$6
			 from model_gateway_charges where operation_key=$1`, in.OperationKey, c.team, c.member, c.price, c.tier, hold).Scan(&same)
			if err == nil && !same {
				return ErrChargeConflict
			}
			return err
		}
		if err != nil {
			return err
		}
		if err := adjustBudgets(ctx, tx, c, hold, 0, true); err != nil {
			return err
		}
		acquired = true
		return nil
	})
	return acquired && err == nil, err
}

func (b Budget) Settle(ctx context.Context, key string, usage Usage) (int64, error) {
	return b.finish(ctx, key, &usage)
}

// Release applies when a request was never sent or was explicitly rejected
// without usage. An ambiguous provider outcome retains its reservation.
func (b Budget) Release(ctx context.Context, key string) error {
	_, err := b.finish(ctx, key, nil)
	return err
}

func (b Budget) finish(ctx context.Context, key string, usage *Usage) (int64, error) {
	var cost int64
	err := pgx.BeginFunc(ctx, b.DB, func(tx pgx.Tx) error {
		var c charge
		var oldCost *int64
		var oldUsage Usage
		err := tx.QueryRow(ctx, `select team_id,member_id,price_id,tier,month_start,reserved_micro_cny,state,cost_micro_cny,
		 coalesce(prompt_tokens,0),coalesce(completion_tokens,0),coalesce(cached_tokens,0),coalesce(image_count,0)
		 from model_gateway_charges where operation_key=$1 for update`, key).
			Scan(&c.team, &c.member, &c.price, &c.tier, &c.month, &c.held, &c.state, &oldCost,
				&oldUsage.PromptTokens, &oldUsage.CompletionTokens, &oldUsage.CachedTokens, &oldUsage.Images)
		if err != nil {
			return err
		}
		if c.state != "reserved" {
			if c.state == "released" && usage == nil {
				return nil
			}
			if c.state == "settled" && usage != nil && oldUsage == *usage {
				cost = *oldCost
				return nil
			}
			return ErrChargeConflict
		}
		if usage != nil {
			price, err := readPrice(ctx, tx, c.price)
			if err != nil {
				return err
			}
			cost, err = price.CostMicroCNY(*usage)
			if err != nil {
				return err
			}
		}
		if err := adjustBudgets(ctx, tx, c, -c.held, cost, false); err != nil {
			return err
		}
		if usage == nil {
			_, err = tx.Exec(ctx, `update model_gateway_charges set state='released',settled_at=now() where operation_key=$1`, key)
		} else {
			_, err = tx.Exec(ctx, `update model_gateway_charges set state='settled',settled_at=now(),cost_micro_cny=$2,
			 prompt_tokens=$3,completion_tokens=$4,cached_tokens=$5,image_count=$6 where operation_key=$1`, key, cost,
				usage.PromptTokens, usage.CompletionTokens, usage.CachedTokens, usage.Images)
		}
		return err
	})
	return cost, err
}

func readPrice(ctx context.Context, tx pgx.Tx, id string) (Price, error) {
	var price Price
	err := tx.QueryRow(ctx, `select input_per_1m_micro_cny,output_per_1m_micro_cny,cached_per_1m_micro_cny,per_image_micro_cny
	 from model_pricing where id=$1 and effective_at<=now()`, id).
		Scan(&price.InputPerMillion, &price.OutputPerMillion, &price.CachedPerMillion, &price.PerImage)
	return price, err
}

func adjustBudgets(ctx context.Context, tx pgx.Tx, c charge, holdDelta, spendDelta int64, enforcing bool) error {
	for _, scope := range [][2]string{{"enterprise", uuid.Nil.String()}, {"team", c.team}, {"member", c.member}} {
		if _, err := tx.Exec(ctx, `insert into model_monthly_budget_totals(scope,scope_id,month_start) values($1,$2,$3)
		 on conflict do nothing`, scope[0], scope[1], c.month); err != nil {
			return err
		}
		var held, spent int64
		if err := tx.QueryRow(ctx, `select reserved_micro_cny,spent_micro_cny from model_monthly_budget_totals
		 where (scope,scope_id,month_start)=($1,$2,$3) for update`, scope[0], scope[1], c.month).Scan(&held, &spent); err != nil {
			return err
		}
		var limit *int64
		err := tx.QueryRow(ctx, `select limit_micro_cny from model_budget_policies
		 where (scope,scope_id)=($1,$2) for share`, scope[0], scope[1]).Scan(&limit)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if holdDelta > math.MaxInt64-held || spendDelta > math.MaxInt64-spent {
			return errors.New("monthly model budget counter overflow")
		}
		held += holdDelta
		spent += spendDelta
		if held < 0 || held > math.MaxInt64-spent {
			return errors.New("invalid monthly model budget balance")
		}
		used := held + spent
		if enforcing && holdDelta > 0 && limit != nil && used > *limit {
			return fmt.Errorf("%w: %s", ErrBudgetExceeded, scope[0])
		}
		if _, err := tx.Exec(ctx, `update model_monthly_budget_totals set reserved_micro_cny=$4,spent_micro_cny=$5
		 where (scope,scope_id,month_start)=($1,$2,$3)`, scope[0], scope[1], c.month, held, spent); err != nil {
			return err
		}
		if limit != nil && *limit > 0 && used >= *limit-*limit/5 {
			if _, err := tx.Exec(ctx, `insert into model_budget_alert_events(scope,scope_id,month_start,threshold,limit_micro_cny,used_micro_cny)
			 select $1,$2,$3,t,$4::bigint,$5::bigint from (values(80),(100)) as thresholds(t)
			 where t=80 or $5::bigint>=$4::bigint on conflict do nothing`, scope[0], scope[1], c.month, *limit, used); err != nil {
				return err
			}
		}
	}
	return nil
}
