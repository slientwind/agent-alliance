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
	"alliance/internal/memory"
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
	Terms         []string
	SearchBatches [][]string                   `json:",omitempty"`
	Calculation   *harness.CalculationDecision `json:",omitempty"`
	Citations     []indexCitation
	AlreadyKnown  []indexCitation `json:",omitempty"`
}

type ActionKind string

const (
	refineLookupAction ActionKind = "refine_lookup"
	calculateAction    ActionKind = "calculate_amounts"
)

type ActionRef struct {
	Step            Step
	Attempt         int
	Kind            ActionKind
	ControlRevision int
	Round           int `json:",omitempty"`
}

func (a ActionRef) scope() string {
	if a.Kind == "" {
		return (lookupAction{a.Step, a.Attempt}).scope()
	}
	return fmt.Sprintf("local-action/v1/%s/%d/%d/%s", a.Step.Run, a.Step.Ordinal, a.Attempt, a.Kind)
}

// Plan one local action from the completed model reply. The checkpoint preserves
// locations and sealed terms but keeps original quotes in their source versions.
func (a Activities) prepareLookupTurn(step Step, attempt int, d contextData, reply string) ([]byte, error) {
	return a.prepareRefinement(ActionRef{Step: step, Attempt: attempt}, d, reply)
}

func (a Activities) prepareRefinement(action ActionRef, d contextData, reply string) ([]byte, error) {
	decision, err := harness.DecodeLookup(reply, len(d.hits))
	if err != nil || len(d.hits) == 0 {
		return nil, nil
	}
	terms := normalizeLookupTerms(decision.Terms)
	if len(terms) == 0 {
		return nil, nil
	}
	return a.sealLookupAction(action, d, lookupTurnInput{Terms: terms})
}

func (a Activities) prepareBatchedRefinement(step Step, attempt int, d contextData, reply string) ([]byte, int, error) {
	action := ActionRef{Step: step, Attempt: attempt, Kind: refineLookupAction}
	if batches, err := harness.DecodeLookupBatches(reply); err == nil && len(d.hits) > 0 {
		body, err := a.sealLookupAction(action, d, lookupTurnInput{SearchBatches: batches})
		return body, len(batches), err
	}
	body, err := a.prepareRefinement(action, d, reply)
	return body, 1, err
}

func (a Activities) prepareLocalAction(step Step, attempt int, d contextData, reply string) (ActionKind, []byte, error) {
	switch d.lookupPhase {
	case "answer", "review_evidence":
		body, err := a.prepareRefinement(ActionRef{Step: step, Attempt: attempt, Kind: refineLookupAction}, d, reply)
		return refineLookupAction, body, err
	case "answer_calc":
		decision, err := harness.DecodeCalculation(reply, len(d.operands))
		if err != nil || len(d.hits) == 0 {
			return "", nil, nil
		}
		body, err := a.sealLookupAction(ActionRef{Step: step, Attempt: attempt, Kind: calculateAction}, d, lookupTurnInput{Calculation: &decision})
		return calculateAction, body, err
	default:
		return "", nil, nil
	}
}

func (a Activities) sealLookupAction(action ActionRef, d contextData, input lookupTurnInput) ([]byte, error) {
	input.Citations = lookupCitationRefs(d.hits, d.lookupFiles)
	input.AlreadyKnown = lookupCitationRefs(d.knownLookupEvidence, d.lookupFiles)
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return a.Catalog.Vault.Seal(string(encoded), action.scope())
}

func saveLookupTurn(ctx context.Context, tx pgx.Tx, step Step, d contextData, attempt, control int, status string, kind ActionKind, decision []byte, reply resources.Reply, observe bool, actionCount int, result *Result) error {
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
	var initial []byte
	if revoked {
		decision, disposition = nil, "invalidated"
	} else if kind == refineLookupAction {
		var err error
		initial, err = json.Marshal(lookupCitationRefs(d.hits, d.lookupFiles))
		if err != nil {
			return err
		}
	}
	var agent, revision, decidedControl any
	if kind != "" {
		agent, revision, decidedControl = d.agent, d.revision, d.controlRevision
	}
	if _, err := tx.Exec(ctx, `insert into lookup_turns(run_id,ordinal,attempt_no,plan_version,decision,outcome,
		  action_kind,agent_id,agent_revision,decided_control_revision,observe_before_commit,action_count,initial_evidence)
		values($1,$2,$3,$4,$5,$6,nullif($7,''),$8,$9,$10,$11,$12,$13::jsonb) on conflict do nothing`, step.Run, step.Ordinal, attempt,
		d.planVersion, decision, disposition, kind, agent, revision, decidedControl, observe, actionCount, initial); err != nil {
		return err
	}
	if actionCount > 1 && !revoked {
		if _, err := tx.Exec(ctx, "select record_service_event($1,'step','refining_evidence',$2)", step.Run, step.Ordinal); err != nil {
			return err
		}
	}
	if status == "running" {
		setLocalActionResult(step, attempt, control, kind, lookupActionRound(actionCount, 1), result)
		return nil
	}
	result.Wait = true
	if status == "reconcile" {
		return resumeReturnedSubmission(ctx, tx, step.Run, d.project)
	}
	return nil
}

func resumeLookupTurn(ctx context.Context, tx pgx.Tx, step Step, d contextData, attempt int, result *Result) (bool, error) {
	var pending, observed bool
	var rounds, cursor int
	var kind ActionKind
	err := tx.QueryRow(ctx, `select t.decision is not null,coalesce(t.action_kind,''),o.payload is not null,t.action_count,t.action_cursor
		from lookup_turns t left join local_action_observations o on
		(o.run_id,o.ordinal,o.attempt_no,o.action_round)=(t.run_id,t.ordinal,t.attempt_no,t.action_cursor)
		where t.run_id=$1 and t.ordinal=$2 and t.attempt_no=$3`, step.Run, step.Ordinal, attempt).Scan(&pending, &kind, &observed, &rounds, &cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if pending {
		round := lookupActionRound(rounds, cursor)
		if observed {
			result.Observation = &ActionRef{Step: step, Attempt: attempt, Kind: kind, ControlRevision: d.controlRevision, Round: round}
		} else {
			setLocalActionResult(step, attempt, d.controlRevision, kind, round, result)
		}
		return true, nil
	}
	result.Wait = true
	return true, setState(ctx, tx, step, d.project, "blocked", "lookup_turn_invalidated", "续查依据已失效，请核对后重新发起。")
}

func lookupActionRound(count, cursor int) int {
	if count == 1 {
		return 0
	}
	return cursor
}

func setLocalActionResult(step Step, attempt, control int, kind ActionKind, round int, result *Result) {
	if kind == "" {
		result.LookupAttempt = attempt
		return
	}
	result.Action = &ActionRef{Step: step, Attempt: attempt, Kind: kind, ControlRevision: control, Round: round}
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
	return a.executeLocalAction(ctx, ActionRef{Step: action.Step, Attempt: action.Attempt}, false)
}

// ExecuteAction consumes only the decision recorded for this invocation.
// A new control revision forces the Workflow to ask for the current action.
func (a Activities) ExecuteAction(ctx context.Context, action ActionRef) (Result, error) {
	if action.Kind == "" {
		return Result{}, fmt.Errorf("missing local action kind")
	}
	return a.executeLocalAction(ctx, action, false)
}

func (a Activities) CommitLocalObservation(ctx context.Context, action ActionRef) (Result, error) {
	return a.executeLocalAction(ctx, action, true)
}

func (a Activities) executeLocalAction(ctx context.Context, action ActionRef, commit bool) (Result, error) {
	step := action.Step
	var result Result
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var d contextData
		var status, invocationStatus string
		var savedPlan, attempt, control, savedRevision, decidedControl int
		var actionCount, actionCursor int
		var savedAgent string
		var kind ActionKind
		var sealed, accumulated []byte
		var recorded, observe bool
		if err := tx.QueryRow(ctx, `select project_id,actor,status,plan_version,max_calls,control_revision from runs
            where id=$1 for update`, step.Run).Scan(&d.project, &d.actor, &status, &d.planVersion, &d.maximum, &control); err != nil {
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
          i.result is not null,t.plan_version,t.decision,coalesce(t.action_kind,''),coalesce(t.agent_id::text,''),
		  coalesce(t.agent_revision,0),coalesce(t.decided_control_revision,0),t.observe_before_commit,
		  t.action_count,t.action_cursor,t.refinement_evidence
          from effective_run_nodes n join invocations i on(i.run_id,i.ordinal)=(n.run_id,n.ordinal)
          join lookup_turns t on(t.run_id,t.ordinal)=(n.run_id,n.ordinal) and t.attempt_no=$3
          where n.run_id=$1 and n.ordinal=$2`, step.Run, step.Ordinal, action.Attempt).Scan(
			&d.agent, &d.revision, &d.lookupPhase, &attempt, &invocationStatus, &recorded, &savedPlan, &sealed,
			&kind, &savedAgent, &savedRevision, &decidedControl, &observe, &actionCount, &actionCursor, &accumulated)
		if err != nil {
			return err
		}
		if attempt != action.Attempt || invocationStatus != "completed" || kind != action.Kind {
			result.Wait = true
			return nil
		}
		if recorded {
			return nil
		}
		if commit && !observe {
			return errors.New("local action does not use the observation protocol")
		}
		if kind != "" && (control != action.ControlRevision || action.Round != lookupActionRound(actionCount, actionCursor)) {
			result.RevisitTurn = true
			return nil
		}
		invalid := func(reason string) error {
			result.Wait = true
			if _, err := tx.Exec(ctx, `update lookup_turns set decision=null,outcome='invalidated',refinement_evidence=null,initial_evidence=null
                where run_id=$1 and ordinal=$2 and attempt_no=$3`, step.Run, step.Ordinal, action.Attempt); err != nil {
				return err
			}
			return setState(ctx, tx, step, d.project, "blocked", "lookup_turn_invalidated", reason)
		}
		validPhase := (kind == calculateAction && d.lookupPhase == "answer_calc") ||
			(kind != calculateAction && (d.lookupPhase == "answer" || d.lookupPhase == "review_evidence"))
		if len(sealed) == 0 || savedPlan != d.planVersion || !validPhase ||
			(kind != "" && (savedAgent != d.agent || savedRevision != d.revision || decidedControl > control)) {
			return invalid("续查依据或方案已变化，请核对后重新发起。")
		}
		usable, err := memory.Usable(ctx, tx, memory.Key{Run: step.Run, Step: step.Ordinal, Attempt: action.Attempt})
		if err != nil {
			return err
		}
		if !usable {
			if parked, err := parkRevokedWorkingRule(ctx, tx, step, d.project, action.Attempt); parked || err != nil {
				result.Wait = true
				return err
			}
			return invalid(memory.Unavailable)
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
		prior := first
		if len(input.AlreadyKnown) > 0 {
			known, ok := reopenLookupTurn(input.AlreadyKnown, files)
			if !ok {
				return invalid("已核对的原文位置或版本已变化，请核对后继续。")
			}
			prior = append(append([]lookupHit{}, first...), known...)
		}
		progress := lookupBatchProgress{count: actionCount}
		if action.Round > 0 {
			if len(input.SearchBatches) != actionCount || len(input.SearchBatches[action.Round-1]) == 0 {
				return invalid("补查计划的分组已变化，请重新核对。")
			}
			input.Terms = input.SearchBatches[action.Round-1]
			if len(accumulated) != 0 {
				if err := json.Unmarshal(accumulated, &progress.evidence); err != nil {
					return invalid("已补查的依据无法读取，请重新核对。")
				}
				if len(progress.evidence) > 0 {
					found, ok := reopenLookupTurn(progress.evidence, files)
					if !ok {
						return invalid("此前补查的原文已变化，请重新核对。")
					}
					prior = append(append([]lookupHit{}, prior...), found...)
				}
			}
		}
		var observation localObservation
		if observe {
			// Serialize the publication check with invalidation of this decision.
			var stillPending bool
			if err := tx.QueryRow(ctx, `select decision is not null and run_working_set_usable(run_id,ordinal,attempt_no) from lookup_turns
                where (run_id,ordinal,attempt_no)=($1,$2,$3) for update`, step.Run, step.Ordinal, attempt).Scan(&stillPending); err != nil {
				return err
			}
			if !stillPending {
				if parked, err := parkRevokedWorkingRule(ctx, tx, step, d.project, attempt); parked || err != nil {
					result.Wait = true
					return err
				}
				return invalid(memory.Unavailable)
			}
			observation, err = a.resolveLocalObservation(ctx, tx, action, commit, input, prior, files, &result)
		} else {
			observation, err = observeLocalAction(kind, input, first, files)
		}
		if errors.As(err, &problem) {
			return invalid(problem.Message)
		}
		if err != nil || result.Observation != nil || result.RevisitTurn {
			return err
		}
		if action.Round > 0 {
			observation, err = progress.consume(ctx, tx, action, observation, first, files, &result)
			if errors.As(err, &problem) {
				return invalid(problem.Message)
			}
			if err != nil || result.Action != nil {
				return err
			}
		}
		var evidence []byte
		if len(observation.Refinement) > 0 {
			evidence, err = json.Marshal(observation.Refinement)
			if err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, "update invocations set result=$3 where run_id=$1 and ordinal=$2", step.Run, step.Ordinal, observation.Body); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `update lookup_turns set decision=null,outcome=$4,refinement_evidence=$5::jsonb
			where run_id=$1 and ordinal=$2 and attempt_no=$3`, step.Run, step.Ordinal, action.Attempt, observation.Outcome, evidence); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint',$3)", step.Run, step.Ordinal, observation.Outcome); err != nil {
			return err
		}
		if action.Round > 0 {
			if _, err := tx.Exec(ctx, "select record_service_event($1,'step','lookup_batches_finished',$2)", step.Run, step.Ordinal); err != nil {
				return err
			}
		}
		return commitOutput(ctx, tx, step, d, observation.Body, &result)
	})
	return result, err
}
