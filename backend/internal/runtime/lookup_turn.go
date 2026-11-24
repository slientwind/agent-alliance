package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/resources"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

type lookupAction struct {
	Step    Step
	Attempt int
}

func (a lookupAction) scope() string {
	return fmt.Sprintf("lookup-turn/v1/%s/%d/%d", a.Step.Run, a.Step.Ordinal, a.Attempt)
}

type lookupTurnInput struct {
	Terms     []string
	Citations []indexCitation
}

// Plan one local action from the completed model reply. The checkpoint preserves
// locations and sealed terms but keeps original quotes in their source versions.
func (a Activities) prepareLookupTurn(step Step, attempt int, d contextData, reply string) ([]byte, error) {
	decision, err := harness.DecodeLookup(reply, len(d.hits))
	if err != nil || len(d.hits) == 0 {
		return nil, nil
	}
	terms := normalizeLookupTerms(decision.Terms)
	if len(terms) == 0 {
		return nil, nil
	}
	input := lookupTurnInput{Terms: terms}
	for _, hit := range d.hits {
		input.Citations = append(input.Citations, indexCitation{
			Source: d.lookupFiles[hit.source].ID, Version: hit.version,
			Start: hit.start, End: hit.end, Hash: receiptDigest([]byte(hit.text)),
		})
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return a.Catalog.Vault.Seal(string(encoded), (lookupAction{step, attempt}).scope())
}

func saveLookupTurn(ctx context.Context, tx pgx.Tx, step Step, d contextData, attempt int, status string, decision []byte, reply resources.Reply, result *Result) error {
	if _, err := tx.Exec(ctx, `update invocations set status='completed',tokens=$3,result=null,ended_at=now()
        where run_id=$1 and ordinal=$2`, step.Run, step.Ordinal, callTokens(d.model, reply)); err != nil {
		return err
	}
	if runEnded(status) {
		result.Stop = true
		return nil
	}
	// Take the source-edit lock before storing the terms. If a source was
	// revoked while the model call was in flight, retain only the paid receipt.
	if _, err := tx.Exec(ctx, "select id from projects where id=$1 for update", d.project); err != nil {
		return err
	}
	var revoked bool
	if err := tx.QueryRow(ctx, `select exists(select from plan_sources p join sources s on s.id=p.source_id
        where p.project_id=$1 and p.plan_version=$2 and s.revoked_at is not null)`, d.project, d.planVersion).Scan(&revoked); err != nil {
		return err
	}
	var disposition any
	if revoked {
		decision, disposition = nil, "invalidated"
	}
	if _, err := tx.Exec(ctx, `insert into lookup_turns(run_id,ordinal,attempt_no,plan_version,decision,outcome)
        values($1,$2,$3,$4,$5,$6) on conflict do nothing`, step.Run, step.Ordinal, attempt, d.planVersion, decision, disposition); err != nil {
		return err
	}
	if status == "running" {
		result.LookupAttempt = attempt
		return nil
	}
	result.Wait = true
	if status == "reconcile" {
		return resumeReturnedSubmission(ctx, tx, step.Run, d.project)
	}
	return nil
}

func resumeLookupTurn(ctx context.Context, tx pgx.Tx, step Step, d contextData, attempt int, result *Result) (bool, error) {
	var pending bool
	err := tx.QueryRow(ctx, `select decision is not null from lookup_turns
        where run_id=$1 and ordinal=$2 and attempt_no=$3`, step.Run, step.Ordinal, attempt).Scan(&pending)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if pending {
		result.LookupAttempt = attempt
		return true, nil
	}
	result.Wait = true
	return true, setState(ctx, tx, step, d.project, "blocked", "lookup_turn_invalidated", "续查依据已失效，请核对后重新发起。")
}

func reopenLookupTurn(citations []indexCitation, files []knowledge.InputSource) ([]lookupHit, bool) {
	if len(citations) == 0 || len(citations) > 6 {
		return nil, false
	}
	hits := make([]lookupHit, 0, len(citations))
	for _, citation := range citations {
		found := false
		for source, file := range files {
			if file.ID != citation.Source || file.Version != citation.Version {
				continue
			}
			text := []rune(file.Text)
			start, end := citation.Start, citation.End
			if start < 0 || end <= start || end > len(text) || end-start > 320 ||
				file.ReceiptStep != 0 && (start < file.ExcerptStart || end > file.ExcerptEnd) {
				return nil, false
			}
			quote := string(text[start:end])
			if receiptDigest([]byte(quote)) != citation.Hash {
				return nil, false
			}
			hits = append(hits, lookupHit{source: source, version: file.Version, start: start, end: end, text: quote})
			found = true
			break
		}
		if !found {
			return nil, false
		}
	}
	return hits, true
}

// ExecuteLookupAction consumes a single pending local effect and commits its
// observation with the candidate. Retrying after that commit is a read.
func (a Activities) ExecuteLookupAction(ctx context.Context, action lookupAction) (Result, error) {
	step := action.Step
	var result Result
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var d contextData
		var status, invocationStatus string
		var savedPlan, attempt int
		var sealed []byte
		var recorded bool
		if err := tx.QueryRow(ctx, `select project_id,actor,status,plan_version,max_calls from runs
            where id=$1 for update`, step.Run).Scan(&d.project, &d.actor, &status, &d.planVersion, &d.maximum); err != nil {
			return err
		}
		result.More = step.Ordinal < d.maximum
		if status != "running" {
			result.Stop = runEnded(status)
			result.Wait = !result.Stop
			return nil
		}
		// Read the receipt after taking the lock so a concurrent consumer's
		// commit is visible even when the run itself stays in "running".
		err := tx.QueryRow(ctx, `select n.agent_id,n.agent_revision,n.lookup_phase,i.attempt_no,i.status,
          i.result is not null,t.plan_version,t.decision
          from effective_run_nodes n join invocations i on(i.run_id,i.ordinal)=(n.run_id,n.ordinal)
          join lookup_turns t on(t.run_id,t.ordinal)=(n.run_id,n.ordinal) and t.attempt_no=$3
          where n.run_id=$1 and n.ordinal=$2`, step.Run, step.Ordinal, action.Attempt).Scan(
			&d.agent, &d.revision, &d.lookupPhase, &attempt, &invocationStatus, &recorded, &savedPlan, &sealed)
		if err != nil {
			return err
		}
		if attempt != action.Attempt || invocationStatus != "completed" {
			result.Wait = true
			return nil
		}
		if recorded {
			return nil
		}
		invalid := func(reason string) error {
			result.Wait = true
			if _, err := tx.Exec(ctx, `update lookup_turns set decision=null,outcome='invalidated'
                where run_id=$1 and ordinal=$2 and attempt_no=$3`, step.Run, step.Ordinal, action.Attempt); err != nil {
				return err
			}
			return setState(ctx, tx, step, d.project, "blocked", "lookup_turn_invalidated", reason)
		}
		if len(sealed) == 0 || savedPlan != d.planVersion || d.lookupPhase != "answer" {
			return invalid("续查依据或方案已变化，请核对后重新发起。")
		}
		// Source edits and revocations take the same project lock.
		var project string
		err = tx.QueryRow(ctx, `select p.id from projects p join project_members pm on pm.project_id=p.id
          join members u on u.id=pm.member_id and u.enabled where p.id=$1 and u.id=$2 for update of p`, d.project, d.actor).Scan(&project)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid("项目访问已失效，已停止续查。")
		}
		if err != nil {
			return err
		}
		var granted bool
		if err = tx.QueryRow(ctx, identity.RunGrantSQL, step.Run).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			return invalid("应用或成员委托已失效，已停止续查。")
		}
		if ok, err := checkAgentEvidence(ctx, tx, step, &result, d); !ok || err != nil {
			return err
		}
		var problem web.Problem
		if _, _, err = a.Catalog.ResolveRun(ctx, tx, d.project, d.actor, d.agent, d.revision); errors.Is(err, pgx.ErrNoRows) {
			return invalid("成员或资源授权已失效，已停止续查。")
		}
		if errors.As(err, &problem) {
			return invalid(problem.Message)
		}
		if err != nil {
			return err
		}
		_, files, err := knowledge.RunInputs(ctx, tx, step.Run, step.Ordinal, false)
		if errors.As(err, &problem) {
			return invalid(problem.Message)
		}
		if err != nil {
			return err
		}
		var input lookupTurnInput
		raw, err := a.Catalog.Vault.Open(sealed, action.scope())
		if err != nil || json.Unmarshal([]byte(raw), &input) != nil {
			return invalid("续查记录无法读取，请核对后重新发起。")
		}
		first, ok := reopenLookupTurn(input.Citations, files)
		if !ok {
			return invalid("首轮引文的资料位置或版本已变化，请核对后重新发起。")
		}
		text, outcome := refineLookup(input.Terms, first, files)
		if _, err = tx.Exec(ctx, "update invocations set result=$3 where run_id=$1 and ordinal=$2", step.Run, step.Ordinal, text); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update lookup_turns set decision=null,outcome=$4
            where run_id=$1 and ordinal=$2 and attempt_no=$3`, step.Run, step.Ordinal, action.Attempt, outcome); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint',$3)", step.Run, step.Ordinal, outcome); err != nil {
			return err
		}
		return commitOutput(ctx, tx, step, d, text, &result)
	})
	return result, err
}
