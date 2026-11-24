package runtime

import (
	"context"
	"encoding/json"
	"errors"

	"alliance/internal/harness"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

const lookupQuestionWait = "还缺一段能回答本次问题的原文。请核对上一轮提出的缺项并补充原句。"

func stageLookupQuestion(ctx context.Context, tx pgx.Tx, ref TextReplyRef, d contextData, question string) (bool, error) {
	if !harness.IsReceiptQuestion(question) {
		return false, nil
	}
	var target int
	err := tx.QueryRow(ctx, `select n.ordinal
        from runs r join node_edges e on(e.project_id,e.plan_version,e.depends_on)=(r.project_id,r.plan_version,$2)
        join effective_run_nodes n on(n.run_id,n.ordinal)=(r.id,e.ordinal)
        join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
        where r.id=$1 and r.await_lookup_receipt and n.lookup_phase in ('review_evidence','synthesize')
          and (n.agent_id,n.agent_revision)=($3,$4) and n.ordinal<=r.max_calls
          and a.resource_id is not null and a.tool is null and a.card is null and agent_output_operation(a)='text'
        order by n.ordinal limit 1`, ref.Step.Run, ref.Step.Ordinal, d.agent, d.revision).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// A receipt belongs to its own step. Carry only positions from the
	// plan sources into the next turn and require a new, scoped receipt there.
	var prior []lookupHit
	for _, hit := range d.hits {
		if d.lookupFiles[hit.source].ReceiptStep == 0 {
			prior = append(prior, hit)
		}
	}
	citations, err := json.Marshal(lookupCitationRefs(prior, d.lookupFiles))
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `insert into lookup_question_handoffs
        (run_id,target,parent,parent_attempt,plan_version,question,citations) values($1,$2,$3,$4,$5,$6,$7)`,
		ref.Step.Run, target, ref.Step.Ordinal, ref.Attempt, d.planVersion, question, citations); err != nil {
		return false, err
	}
	if err := retireTextReply(ctx, tx, ref, "consumed"); err != nil {
		return false, err
	}
	// A completed inference with a handoff is an observation, not a deliverable.
	if _, err := tx.Exec(ctx, `update invocations set result=$4
        where (run_id,ordinal,attempt_no)=($1,$2,$3)`, ref.Step.Run, ref.Step.Ordinal, ref.Attempt, lookupQuestionWait); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint','lookup_question_committed')", ref.Step.Run, ref.Step.Ordinal); err != nil {
		return false, err
	}
	if err := projects.TouchRun(ctx, tx, ref.Step.Run, d.project, "running"); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, "select record_service_event($1,'step','lookup_question_committed',$2)", ref.Step.Run, ref.Step.Ordinal)
	return true, err
}

func lookupQuestionEvidence(ctx context.Context, tx pgx.Tx, step Step, files []knowledge.InputSource) ([]lookupHit, bool, error) {
	var refs []byte
	var usable bool
	err := tx.QueryRow(ctx, `select q.citations,q.usable from lookup_question_availability q
        where (q.run_id,q.target)=($1,$2)`, step.Run, step.Ordinal).Scan(&refs, &usable)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !usable {
		return nil, true, web.Fail(409, "上一轮提出补件所用的依据已变化，请重新核对任务")
	}
	var original []indexCitation
	if err := json.Unmarshal(refs, &original); err != nil {
		return nil, true, err
	}
	prior, ok := reopenLookupTurn(original, files)
	if !ok {
		return nil, true, web.Fail(409, "上一轮引用的原文已变化，请重新核对任务")
	}
	// Put the newly confirmed passage first even if all original slots
	// were filled. No passage from another step can substitute for it.
	for _, hit := range receiptHits(files) {
		if files[hit.source].ReceiptStep == step.Ordinal {
			return append([]lookupHit{hit}, prior[:min(len(prior), 5)]...), true, nil
		}
	}
	return nil, true, nil
}
