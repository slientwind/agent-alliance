package llmgateway

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrCircuitOpen = errors.New("model circuit is open")
	ErrRateLimited = errors.New("model rate limit reached")
)

const (
	maxFailedCalls  = 3
	circuitCooldown = 30 * time.Second
	probeLease      = 2 * time.Minute
)

type Resilience struct {
	DB interface {
		Begin(context.Context) (pgx.Tx, error)
	}
}

type Permit struct {
	ModelID string
	Window  time.Time
	Tokens  int64
	Probe   *time.Time
}

type Attempt struct {
	OperationKey string
	RunID        string
	AgentID      string
	Number       int
	Fallback     bool
	Started      time.Time
	Finished     time.Time
	Status       string
	ProviderCode int
	Usage        *Usage

	TransientFailure bool
	Unsent           bool
}

func (r Resilience) Acquire(ctx context.Context, candidate Candidate, estimatedTokens int64, now time.Time) (Permit, error) {
	if estimatedTokens < 0 {
		return Permit{}, ErrRateLimited
	}
	permit := Permit{ModelID: candidate.ModelID, Window: now.UTC().Truncate(time.Minute), Tokens: estimatedTokens}
	err := pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into model_gateway_state(model_id,window_start) values($1,$2) on conflict do nothing`,
			permit.ModelID, permit.Window); err != nil {
			return err
		}
		var window time.Time
		var count int
		var tokens int64
		var until, probe *time.Time
		if err := tx.QueryRow(ctx, `select window_start,requests,tokens,open_until,probe_until
		 from model_gateway_state where model_id=$1 for update`, permit.ModelID).Scan(&window, &count, &tokens, &until, &probe); err != nil {
			return err
		}
		if until != nil {
			if now.Before(*until) || (probe != nil && now.Before(*probe)) {
				return ErrCircuitOpen
			}
			lease := now.Add(probeLease).Truncate(time.Microsecond)
			permit.Probe = &lease
		}
		if window.Before(permit.Window) {
			count, tokens = 0, 0
		} else {
			permit.Window = window
		}
		if count == math.MaxInt32 || estimatedTokens > math.MaxInt64-tokens ||
			(candidate.RequestsPerMin != nil && count >= *candidate.RequestsPerMin) ||
			(candidate.TokensPerMin != nil && tokens+estimatedTokens > int64(*candidate.TokensPerMin)) {
			return ErrRateLimited
		}
		_, err := tx.Exec(ctx, `update model_gateway_state set window_start=$2,requests=$3,tokens=$4,probe_until=$5 where model_id=$1`,
			permit.ModelID, permit.Window, count+1, tokens+estimatedTokens, permit.Probe)
		return err
	})
	return permit, err
}

func (r Resilience) Complete(ctx context.Context, permit Permit, attempt Attempt) error {
	return pgx.BeginFunc(ctx, r.DB, func(tx pgx.Tx) error {
		var window time.Time
		var failures, requests int
		var tokens int64
		var until, probe *time.Time
		if err := tx.QueryRow(ctx, `select window_start,requests,tokens,failures,open_until,probe_until from model_gateway_state
		 where model_id=$1 for update`, permit.ModelID).Scan(&window, &requests, &tokens, &failures, &until, &probe); err != nil {
			return err
		}
		if attempt.OperationKey != "" {
			tag, err := tx.Exec(ctx, `insert into model_gateway_attempts(operation_key,model_id,attempt,fallback,status,provider_status,started_at,finished_at,run_id,agent_id)
			 values($1,$2,$3,$4,$5,nullif($6,0),$7,$8,nullif($9,'')::uuid,nullif($10,'')::uuid) on conflict do nothing`,
				attempt.OperationKey, permit.ModelID, attempt.Number, attempt.Fallback, attempt.Status, attempt.ProviderCode,
				attempt.Started, attempt.Finished, attempt.RunID, attempt.AgentID)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
		}
		if window.Equal(permit.Window) {
			switch {
			case attempt.Unsent:
				requests = max(0, requests-1)
				tokens = max(0, tokens-permit.Tokens)
			case attempt.Usage != nil:
				u := *attempt.Usage
				if u.PromptTokens >= 0 && u.CompletionTokens >= 0 && u.CompletionTokens <= math.MaxInt64-u.PromptTokens {
					tokens = max(0, tokens-permit.Tokens)
					tokens += min(math.MaxInt64-tokens, u.PromptTokens+u.CompletionTokens)
				}
			}
		}
		matchedProbe := permit.Probe != nil && probe != nil && permit.Probe.Equal(*probe)
		if matchedProbe {
			probe = nil
		}
		if !attempt.Unsent && (until == nil || matchedProbe) {
			switch {
			case attempt.TransientFailure:
				failures = min(maxFailedCalls, failures+1)
				if failures == maxFailedCalls {
					next := attempt.Finished.Add(circuitCooldown)
					until = &next
				}
			case attempt.Status == "succeeded":
				failures, until = 0, nil
			}
		}
		_, err := tx.Exec(ctx, `update model_gateway_state set requests=$2,tokens=$3,failures=$4,open_until=$5,probe_until=$6
		 where model_id=$1`, permit.ModelID, requests, tokens, failures, until, probe)
		return err
	})
}
