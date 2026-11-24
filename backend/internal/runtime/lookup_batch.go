package runtime

import (
	"context"
	"encoding/json"

	"alliance/internal/knowledge"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

const lookupBatchEvidenceLimit = 6

type lookupBatchProgress struct {
	count    int
	evidence []indexCitation
}

func observeLookupBatch(terms []string, prior []lookupHit, files []knowledge.InputSource) (localObservation, error) {
	_, outcome, fresh := refineLookupObserved(terms, prior, files)
	if outcome != "lookup_refined" && outcome != "lookup_no_progress" {
		return localObservation{}, web.Fail(409, "本组关键词或原文位置已失效，请核对后重新开始补查。")
	}
	return localObservation{Outcome: outcome, Refinement: lookupCitationRefs(fresh, files)}, nil
}

// A batch advances the cursor of the current inference's decision. Only the
// final observation publishes a candidate or enables the planned continuation.
func (p lookupBatchProgress) consume(ctx context.Context, tx pgx.Tx, action ActionRef, observation localObservation, first []lookupHit, files []knowledge.InputSource, result *Result) (localObservation, error) {
	evidence := append(p.evidence, observation.Refinement...)
	evidence = evidence[:min(len(evidence), lookupBatchEvidenceLimit)]
	if action.Round < p.count && len(evidence) < lookupBatchEvidenceLimit {
		data, err := json.Marshal(evidence)
		if err != nil {
			return observation, err
		}
		if _, err := tx.Exec(ctx, `update lookup_turns set action_cursor=action_cursor+1,refinement_evidence=$4::jsonb
            where (run_id,ordinal,attempt_no)=($1,$2,$3)`, action.Step.Run, action.Step.Ordinal, action.Attempt, data); err != nil {
			return observation, err
		}
		if _, err := tx.Exec(ctx, "select record_service_event($1,'step','refining_evidence',$2)", action.Step.Run, action.Step.Ordinal); err != nil {
			return observation, err
		}
		action.Round++
		result.Action = &action
		return observation, nil
	}
	if len(evidence) == 0 {
		return localObservation{
			Body:    "分组补查未取得新依据。请核对已有原文，并补充缺少的资料。\n" + lookupContext(first, files),
			Outcome: "lookup_no_progress",
		}, nil
	}
	found, ok := reopenLookupTurn(evidence, files)
	if !ok {
		return observation, web.Fail(409, "已补查的原文位置无法核对。")
	}
	hits := append(found, first...)
	hits = hits[:min(len(hits), lookupBatchEvidenceLimit)]
	return localObservation{
		Body:       "已完成分组补查，以下原文供核对。\n" + lookupContext(hits, files),
		Outcome:    "lookup_refined",
		Refinement: lookupCitationRefs(hits, files),
	}, nil
}
