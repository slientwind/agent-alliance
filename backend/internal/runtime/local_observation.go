package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"alliance/internal/knowledge"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

type localObservation struct {
	Body       string
	Outcome    string
	Refinement []indexCitation `json:",omitempty"`
}

func observeLocalAction(kind ActionKind, input lookupTurnInput, first []lookupHit, files []knowledge.InputSource) (localObservation, error) {
	var observation localObservation
	switch kind {
	case "", refineLookupAction:
		var fresh []lookupHit
		observation.Body, observation.Outcome, fresh = refineLookupObserved(input.Terms, first, files)
		if len(fresh) > 0 {
			hits := append(fresh, first...)
			observation.Refinement = lookupCitationRefs(hits[:min(len(hits), 6)], files)
		}
	case calculateAction:
		operands := moneyOperands(first, files)
		if input.Calculation == nil || !input.Calculation.Valid(len(operands)) {
			return observation, web.Fail(409, "核算动作引用的金额已变化，请核对后重新发起。")
		}
		observation.Body, observation.Outcome = applyCalculation(*input.Calculation, first, files, operands)
	default:
		return observation, web.Fail(409, "无法核对已登记的操作，请联系维护人。")
	}
	return observation, nil
}

// The caller holds the run and pending decision locks. A retry returns the
// original observation handle, and publication consumes it in the same
// transaction as the candidate and dependent-ready result.
func (a Activities) resolveLocalObservation(ctx context.Context, tx pgx.Tx, action ActionRef, commit bool, input lookupTurnInput, first []lookupHit, files []knowledge.InputSource, result *Result) (localObservation, error) {
	var observation localObservation
	scope := action.scope() + "/observation"
	var sealed []byte
	var outcome string
	err := tx.QueryRow(ctx, `select payload,outcome from local_action_observations
        where (run_id,ordinal,attempt_no)=($1,$2,$3)`, action.Step.Run, action.Step.Ordinal, action.Attempt).Scan(&sealed, &outcome)
	if errors.Is(err, pgx.ErrNoRows) {
		if commit {
			result.RevisitTurn = true
			return observation, nil
		}
		observation, err := observeLocalAction(action.Kind, input, first, files)
		if err != nil {
			return observation, err
		}
		data, err := json.Marshal(observation)
		if err != nil {
			return observation, err
		}
		sealed, err := a.Catalog.Vault.Seal(string(data), scope)
		if err != nil {
			return observation, err
		}
		if _, err := tx.Exec(ctx, `insert into local_action_observations(run_id,ordinal,attempt_no,control_revision,payload,outcome)
                values($1,$2,$3,$4,$5,$6)`, action.Step.Run, action.Step.Ordinal, action.Attempt, action.ControlRevision, sealed, observation.Outcome); err != nil {
			return observation, err
		}
		if _, err := tx.Exec(ctx, "select record_service_event($1,'step','checking_observation',$2)", action.Step.Run, action.Step.Ordinal); err != nil {
			return observation, err
		}
		result.Observation = &action
		return observation, nil
	}
	if err != nil {
		return observation, err
	}
	if len(sealed) == 0 {
		return observation, web.Fail(409, "已记录的观察无法继续使用，请核对当前任务。")
	}
	if !commit {
		result.Observation = &action
		return observation, nil
	}
	data, err := a.Catalog.Vault.Open(sealed, scope)
	if err != nil || json.Unmarshal([]byte(data), &observation) != nil || observation.Body == "" || observation.Outcome != outcome {
		return observation, web.Fail(409, "已记录的观察无法验证，请核对后重新执行。")
	}
	if len(observation.Refinement) > 0 {
		if _, ok := reopenLookupTurn(observation.Refinement, files); !ok {
			return observation, web.Fail(409, "补查观察的资料位置或版本已变化，请核对原文。")
		}
	}
	_, err = tx.Exec(ctx, `update local_action_observations set consumed_at=now(),payload=NULL
        where (run_id,ordinal,attempt_no)=($1,$2,$3)`, action.Step.Run, action.Step.Ordinal, action.Attempt)
	return observation, err
}
