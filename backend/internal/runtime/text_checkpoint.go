package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"alliance/internal/database"
	"alliance/internal/harness"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/memory"
	"alliance/internal/resources"
	"alliance/internal/web"

	"github.com/jackc/pgx/v5"
)

// TextReplyRef can travel through Temporal history. The reply stays in the
// encrypted checkpoint, addressed by the original inference attempt.
type TextReplyRef struct {
	Step    Step
	Attempt int
}

func (ref TextReplyRef) scope() string {
	return fmt.Sprintf("text-reply/v1/%s/%d/%d", ref.Step.Run, ref.Step.Ordinal, ref.Attempt)
}

type checkpointedReply struct {
	Text         string
	FinishReason string
	Evidence     *lookupReplyEvidence `json:",omitempty"`
}

func (reply checkpointedReply) complete() bool {
	return strings.TrimSpace(reply.Text) != "" && (reply.FinishReason == "" || reply.FinishReason == "stop")
}

func (a Activities) recordTextReply(ctx context.Context, ref TextReplyRef, d contextData, reply resources.Reply, result Result) (Result, error) {
	step := ref.Step
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var status, invocation string
		var attempt int
		var replaced bool
		if err := tx.QueryRow(ctx, `select r.status,i.status,i.attempt_no,exists(
            select from active_call_redos c where(c.run_id,c.ordinal,c.attempt_no)=(i.run_id,i.ordinal,$3))
            from runs r join invocations i on i.run_id=r.id
            where (r.id,i.ordinal)=($1,$2) for update of r`,
			step.Run, step.Ordinal, ref.Attempt).Scan(&status, &invocation, &attempt, &replaced); err != nil {
			return err
		}
		result.Stop = runEnded(status)
		result.Wait = !result.Stop && status != "running"
		if replaced || attempt != ref.Attempt {
			result.Wait = !result.Stop
			return saveLateReceipt(ctx, tx, step, ref.Attempt, d.model, reply, nil)
		}
		if invocation == "completed" {
			if !result.Stop && !result.Wait {
				result.TextReply = &ref
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `update invocations set status='completed',tokens=$4,result=null,ended_at=clock_timestamp()
            where (run_id,ordinal,attempt_no)=($1,$2,$3)`, step.Run, step.Ordinal, ref.Attempt, callTokens(d.model, reply)); err != nil {
			return err
		}
		if result.Stop {
			return nil
		}
		usable, err := lockReplyInputs(ctx, tx, ref, d)
		if err != nil {
			return err
		}
		state := "discarded"
		var payload []byte
		if usable {
			state = "pending"
			data, err := json.Marshal(checkpointedReply{Text: reply.Text, FinishReason: reply.FinishReason, Evidence: d.frozenLookup})
			if err != nil {
				return err
			}
			payload, err = a.Catalog.Vault.Seal(string(data), ref.scope())
			if err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `insert into text_reply_checkpoints(run_id,ordinal,attempt_no,state,payload)
            values($1,$2,$3,$4,$5)`, step.Run, step.Ordinal, ref.Attempt, state, payload); err != nil {
			return err
		}
		if usable {
			if _, err := tx.Exec(ctx, "select record_service_event($1,'step','checking_model_reply',$2)", step.Run, step.Ordinal); err != nil {
				return err
			}
		}
		if status == "reconcile" {
			return resumeReturnedSubmission(ctx, tx, step.Run, d.project)
		}
		if !result.Wait {
			result.TextReply = &ref
		}
		return nil
	})
	return result, err
}

// Lock in the same order as source edits and working-set invalidation.
func lockReplyInputs(ctx context.Context, tx pgx.Tx, ref TextReplyRef, d contextData) (bool, error) {
	if _, err := tx.Exec(ctx, "select id from projects where id=$1 for update", d.project); err != nil {
		return false, err
	}
	var pinned bool
	if err := tx.QueryRow(ctx, `select (plan_version,build_agent,build_revision) is not distinct from ($4::int,$5::uuid,$6::int)
        from run_working_sets where (run_id,ordinal,attempt_no)=($1,$2,$3) for update`,
		ref.Step.Run, ref.Step.Ordinal, ref.Attempt, d.planVersion, d.agent, d.revision).Scan(&pinned); err != nil || !pinned {
		return false, err
	}
	return memory.Usable(ctx, tx, memory.Key{Run: ref.Step.Run, Step: ref.Step.Ordinal, Attempt: ref.Attempt})
}

// CommitTextReply adopts one recorded response. Activity retries use the
// checkpoint and never enter the inference or budget-reservation path.
func (a Activities) CommitTextReply(ctx context.Context, ref TextReplyRef) (Result, error) {
	return a.commitModelReply(ctx, ref, checkpointedTextReply)
}

// Keep the evidence Activity distinct so an older worker cannot adopt a
// numbered lookup reply as an ordinary text candidate.
func (a Activities) CommitEvidenceReply(ctx context.Context, ref TextReplyRef) (Result, error) {
	return a.commitModelReply(ctx, ref, checkpointedLookupReply)
}

func (a Activities) CommitEvidenceQuestion(ctx context.Context, ref TextReplyRef) (Result, error) {
	return a.commitModelReply(ctx, ref, checkpointedLookupQuestion)
}

func (a Activities) commitModelReply(ctx context.Context, ref TextReplyRef, mode turnMode) (Result, error) {
	step := ref.Step
	var result Result
	err := database.WithTx(ctx, a.Catalog.DB, func(tx pgx.Tx) error {
		var d contextData
		var status string
		var attempt int
		var published bool
		if err := tx.QueryRow(ctx, `select r.project_id,r.actor,r.status,r.plan_version,r.max_calls,r.control_revision,
            n.agent_id,n.agent_revision,n.lookup_phase,i.attempt_no,i.result is not null,`+runSkillTaskSQL+`
            from runs r join plans p on (p.project_id,p.version)=(r.project_id,r.plan_version)
            join effective_run_nodes n on n.run_id=r.id join invocations i on (i.run_id,i.ordinal)=(n.run_id,n.ordinal)
            where (r.id,n.ordinal)=($1,$2) for update of r`, step.Run, step.Ordinal).
			Scan(&d.project, &d.actor, &status, &d.planVersion, &d.maximum, &d.controlRevision,
				&d.agent, &d.revision, &d.lookupPhase, &attempt, &published, &d.skillTask); err != nil {
			return err
		}
		result.More = step.Ordinal < d.maximum
		result.Stop = runEnded(status)
		result.Wait = !result.Stop && (status != "running" || attempt != ref.Attempt)
		if result.Stop || result.Wait {
			return nil
		}
		if (mode != checkpointedTextReply) != isEvidenceReply(d.lookupPhase) {
			return errors.New("recorded reply does not belong to this activity")
		}
		usable, err := lockReplyInputs(ctx, tx, ref, d)
		if err != nil {
			return err
		}
		var state string
		var payload []byte
		if err := tx.QueryRow(ctx, `select state,payload from text_reply_checkpoints
            where (run_id,ordinal,attempt_no)=($1,$2,$3)`, step.Run, step.Ordinal, ref.Attempt).Scan(&state, &payload); err != nil {
			return err
		}
		if state == "consumed" && (published || !isEvidenceReply(d.lookupPhase)) {
			return nil
		}
		if !usable || state == "discarded" {
			result.Wait = true
			if err := retireTextReply(ctx, tx, ref, "discarded"); err != nil {
				return err
			}
			if parked, err := parkRevokedWorkingRule(ctx, tx, step, d.project, ref.Attempt); parked || err != nil {
				return err
			}
			return setState(ctx, tx, step, d.project, "blocked", "working_memory_invalidated", memory.Unavailable)
		}
		var granted bool
		if err := tx.QueryRow(ctx, identity.RunGrantSQL, step.Run).Scan(&granted); err != nil {
			return err
		}
		if !granted {
			result.Wait = true
			if err := retireTextReply(ctx, tx, ref, "discarded"); err != nil {
				return err
			}
			return setState(ctx, tx, step, d.project, "blocked", "delegation_expired", "提交成果前应用或成员委托已失效")
		}
		if ok, err := checkAgentEvidence(ctx, tx, step, &result, d); !ok || err != nil {
			return err
		}
		if _, _, err := a.Catalog.ResolveRunTask(ctx, tx, d.project, d.actor, d.agent, d.revision, d.skillTask); err != nil {
			return blockUnavailableAgent(ctx, tx, step, d, &result, err)
		}
		if state == "consumed" {
			found, err := resumeLookupTurn(ctx, tx, step, d, ref.Attempt, &result)
			if err != nil || found {
				return err
			}
			return errors.New("lookup reply was consumed without a candidate or decision")
		}
		var files []knowledge.InputSource
		if isEvidenceReply(d.lookupPhase) {
			_, files, err = knowledge.RunInputs(ctx, tx, step.Run, step.Ordinal, false)
		} else {
			_, _, err = knowledge.Inputs(ctx, tx, step.Run)
		}
		if err != nil {
			var problem web.Problem
			if !errors.As(err, &problem) {
				return err
			}
			result.Wait = true
			return setState(ctx, tx, step, d.project, "blocked", "input_changed", problem.Message)
		}
		if state == "incomplete" {
			return blockIncompleteReply(ctx, tx, step, d.project, &result)
		}
		var reply checkpointedReply
		body, err := a.Catalog.Vault.Open(payload, ref.scope())
		if err != nil || json.Unmarshal([]byte(body), &reply) != nil {
			result.Wait = true
			if err := retireTextReply(ctx, tx, ref, "discarded"); err != nil {
				return err
			}
			return setState(ctx, tx, step, d.project, "blocked", "model_reply_unreadable", "原模型回包无法核对，请联系维护人")
		}
		if isEvidenceReply(d.lookupPhase) || reply.Evidence != nil {
			return a.adoptLookupReply(ctx, tx, ref, d, reply, files, status, mode, &result)
		}
		return a.adoptPlainReply(ctx, tx, ref, d, reply, &result)
	})
	return result, err
}

func (a Activities) adoptLookupReply(ctx context.Context, tx pgx.Tx, ref TextReplyRef, d contextData, reply checkpointedReply, files []knowledge.InputSource, status string, mode turnMode, result *Result) error {
	if reply.Evidence == nil || !reply.Evidence.restore(&d, files) {
		result.Wait = true
		if err := retireTextReply(ctx, tx, ref, "discarded"); err != nil {
			return err
		}
		return setState(ctx, tx, ref.Step, d.project, "blocked", "lookup_reply_input_changed", lookupReplyChanged)
	}
	d.frozenLookup = reply.Evidence
	interpreted, err := a.interpretLookupReply(ref, d, reply, mode, true)
	if err != nil {
		return err
	}
	if interpreted.decision != nil {
		if err := retireTextReply(ctx, tx, ref, "consumed"); err != nil {
			return err
		}
		return saveLookupDecision(ctx, tx, ref.Step, d, ref.Attempt, d.controlRevision, status, interpreted.kind,
			interpreted.decision, true, interpreted.groups, result)
	}
	if mode == checkpointedLookupQuestion && interpreted.outcome == "lookup_ask" {
		ask, err := harness.DecodeLookup(reply.Text, len(d.hits))
		if err == nil {
			if staged, err := stageLookupQuestion(ctx, tx, ref, d, ask.Question); staged || err != nil {
				return err
			}
		}
	}
	if interpreted.outcome != "" {
		if _, err := tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint',$3)",
			ref.Step.Run, ref.Step.Ordinal, interpreted.outcome); err != nil {
			return err
		}
	}
	return publishTextReply(ctx, tx, ref, d, interpreted.text, result)
}

func (a Activities) adoptPlainReply(ctx context.Context, tx pgx.Tx, ref TextReplyRef, d contextData, reply checkpointedReply, result *Result) error {
	step := ref.Step
	if err := d.routing.load(ctx, tx, step.Run, step.Ordinal); err != nil {
		return err
	}
	if d.routing.target > 0 {
		if !reply.complete() {
			reply.Text = ""
		}
		choice, text := d.routing.accept(reply.Text, reply.FinishReason)
		if err := d.routing.record(ctx, tx, step, choice); err != nil {
			return err
		}
		reply.Text = text
	} else if !reply.complete() {
		if err := retireTextReply(ctx, tx, ref, "incomplete"); err != nil {
			return err
		}
		return blockIncompleteReply(ctx, tx, step, d.project, result)
	}
	return publishTextReply(ctx, tx, ref, d, reply.Text, result)
}

func publishTextReply(ctx context.Context, tx pgx.Tx, ref TextReplyRef, d contextData, text string, result *Result) error {
	if err := retireTextReply(ctx, tx, ref, "consumed"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update invocations set result=$4
        where (run_id,ordinal,attempt_no)=($1,$2,$3)`, ref.Step.Run, ref.Step.Ordinal, ref.Attempt, text); err != nil {
		return err
	}
	return commitOutput(ctx, tx, ref.Step, d, text, result)
}

func retireTextReply(ctx context.Context, tx pgx.Tx, ref TextReplyRef, state string) error {
	_, err := tx.Exec(ctx, `update text_reply_checkpoints set state=$4,payload=null
        where (run_id,ordinal,attempt_no)=($1,$2,$3) and state='pending'`,
		ref.Step.Run, ref.Step.Ordinal, ref.Attempt, state)
	return err
}

func blockIncompleteReply(ctx context.Context, tx pgx.Tx, step Step, project string, result *Result) error {
	result.Wait = true
	return setState(ctx, tx, step, project, "blocked", "model_reply_incomplete", "模型回复未完整结束，未生成候选。可取消本次运行后重新发起。")
}
