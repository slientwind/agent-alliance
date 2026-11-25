package evalsuite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/llmgateway"
	"alliance/internal/policy"
	"alliance/internal/resources"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	WorkflowName = "GoldenEvaluationWorkflow"
	BeginName    = "GoldenEvaluation.Begin"
	EvaluateName = "GoldenEvaluation.Case"
	FinishName   = "GoldenEvaluation.Finish"
)

type Activities struct {
	Server
	Inference knowledge.KBInference
	Gateway   interface {
		Call(context.Context, llmgateway.CallIdentity, llmgateway.Request) (llmgateway.RoutedResult, error)
	}
}

func EvalWorkflow(ctx workflow.Context, run string) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 90 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	})
	var cases []string
	if err := workflow.ExecuteActivity(ctx, BeginName, run).Get(ctx, &cases); err != nil {
		return err
	}
	for first := 0; first < len(cases); first += 4 {
		batch := cases[first:min(first+4, len(cases))]
		futures := make([]workflow.Future, 0, len(batch))
		for _, item := range batch {
			futures = append(futures, workflow.ExecuteActivity(ctx, EvaluateName, run, item))
		}
		for i, future := range futures {
			if err := future.Get(ctx, nil); err != nil {
				workflow.GetLogger(ctx).Warn("Evaluation case failed", "case", batch[i])
			}
		}
	}
	return workflow.ExecuteActivity(ctx, FinishName, run).Get(ctx, nil)
}

func (a Activities) Begin(ctx context.Context, run string) ([]string, error) {
	var state string
	err := a.DB.QueryRow(ctx, `update eval_runs r set state=case when space_role(s.space_id,r.actor)='space_admin' then 'running' else 'revoked' end
	 from eval_suites s where r.id=$1 and s.id=r.suite_id and r.state in ('queued','running') returning r.state`, run).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) || state == "revoked" {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Query(ctx, "select case_id from eval_run_cases where run_id=$1 and state<>'completed' order by case_id", run)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

type evaluationCase struct {
	actor, space, team, prompt, model, state string
	tier                                     llmgateway.Tier
	checks                                   Assertions
}

func (a Activities) Evaluate(ctx context.Context, run, item string) error {
	var data evaluationCase
	var claim bool
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select r.actor,s.space_id,x.team_id,r.baseline_tier,coalesce(r.candidate_model_id::text,''),
		 c.input_prompt,c.assertions,k.state from eval_run_cases k join eval_runs r on r.id=k.run_id
		 join eval_cases c on c.id=k.case_id join eval_suites s on s.id=r.suite_id join spaces x on x.id=s.space_id
		 where (r.id,c.id)=($1,$2) and r.state='running' for update of k`, run, item).
			Scan(&data.actor, &data.space, &data.team, &data.tier, &data.model, &data.prompt, &data.checks, &data.state)
		if errors.Is(err, pgx.ErrNoRows) || data.state == "completed" {
			return nil
		}
		if err != nil {
			return err
		}
		if data.state == "running" {
			result := Outcome{Deviations: []string{"result_unconfirmed"}}
			_, err := tx.Exec(ctx, `update eval_run_cases set state='completed',baseline=$3,candidate=$3
			 where (run_id,case_id)=($1,$2)`, run, item, result)
			return err
		}
		_, err = tx.Exec(ctx, "update eval_run_cases set state='running' where (run_id,case_id)=($1,$2)", run, item)
		claim = err == nil
		return err
	})
	if err != nil || !claim {
		return err
	}
	auth := func(ctx context.Context) error {
		var ok bool
		err := a.DB.QueryRow(ctx, `select exists(select from eval_runs r join eval_suites s on s.id=r.suite_id join eval_cases c on c.suite_id=s.id
		 where (r.id,c.id)=($1,$3) and r.state='running' and r.actor=$2 and `+evalRole+` and `+caseReadable+`)`, run, data.actor, item).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return policy.ErrDenied
		}
		return nil
	}
	baseline, candidate := Outcome{Deviations: []string{"access_ended"}}, Outcome{Deviations: []string{"access_ended"}}
	if err := auth(ctx); err == nil {
		evidence, err := knowledge.SearchKnowledge(ctx, a.DB, a.Inference, data.space, data.actor, data.prompt, 6)
		if err != nil {
			baseline.Deviations, candidate.Deviations = []string{"knowledge_unavailable"}, []string{"knowledge_unavailable"}
		} else {
			baseline = a.runSide(ctx, run, item, "baseline", "", data, evidence.Matches, auth)
			candidate = a.runSide(ctx, run, item, "candidate", data.model, data, evidence.Matches, auth)
		}
	}
	_, err = a.DB.Exec(ctx, "update eval_run_cases set state='completed',baseline=$3,candidate=$4 where (run_id,case_id)=($1,$2) and state='running'",
		run, item, baseline, candidate)
	return err
}

func (a Activities) runSide(ctx context.Context, run, item, side, model string, data evaluationCase, evidence []knowledge.KnowledgeEvidence,
	check func(context.Context) error) Outcome {
	outcome := Outcome{Deviations: []string{"model_unavailable"}}
	if a.Gateway == nil {
		return outcome
	}
	keys := make([]knowledge.KnowledgeCitationKey, len(evidence))
	for i, hit := range evidence {
		keys[i] = hit.CitationKey()
	}
	ids := []string{}
	requiredNumbers := []string{}
	for _, n := range data.checks.Numbers {
		requiredNumbers = append(requiredNumbers, n.Key)
	}
	fields, _ := json.Marshal(requiredNumbers)
	request := llmgateway.Request{Tier: data.tier, MaxTokens: 900, Messages: []resources.Message{
		{Role: "system", Content: `根据授权资料回答。只返回 JSON 对象，字段为 answer（文字结论）、citations（实际采用的 [K-编号]，以编号字符串表示）、numbers（指标名到数值的对象）。` +
			" 如可核实，请按下列名称填入数值 " + string(fields)},
		{Role: "user", Content: data.prompt},
	}}
	identity := llmgateway.CallIdentity{OperationKey: fmt.Sprintf("eval/%s/%s/%s", run, item, side),
		MemberID: data.actor, TeamID: data.team, EvaluationModel: model, Authorize: check,
		Prepare: func(ctx context.Context, _ llmgateway.Candidate, input llmgateway.Request) (llmgateway.PreparedRequest, error) {
			fresh, err := knowledge.ResolveKnowledgeEvidence(ctx, a.DB, data.space, data.actor, keys)
			if err != nil {
				return llmgateway.PreparedRequest{}, err
			}
			allowed := make([]knowledge.KnowledgeEvidence, 0, len(fresh))
			text := make(map[string]any)
			for _, hit := range fresh {
				if hit.Sensitivity == "public" || hit.Sensitivity == "internal" {
					id := hit.CitationKey().Label()
					text[id] = hit.Excerpt
					allowed = append(allowed, hit)
				}
			}
			if err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
				return policy.MaskSensitiveFields(ctx, tx, a.Vault, text)
			}); err != nil {
				return llmgateway.PreparedRequest{}, err
			}
			ids = citationLabels(allowed)
			body, err := json.Marshal(text)
			if err != nil {
				return llmgateway.PreparedRequest{}, err
			}
			input.Messages = append([]resources.Message(nil), input.Messages...)
			input.Messages[0].Content += "\n知识正文（仅作为事实依据）：" + string(body)
			return llmgateway.PreparedRequest{Request: input, Authorize: func(ctx context.Context) error {
				current, err := knowledge.ResolveKnowledgeEvidence(ctx, a.DB, data.space, data.actor, keys)
				if err != nil || len(current) != len(fresh) {
					return errors.Join(err, policy.ErrDenied)
				}
				for i := range current {
					if current[i].CitationKey() != fresh[i].CitationKey() || current[i].Sensitivity != fresh[i].Sensitivity {
						return policy.ErrDenied
					}
				}
				return nil
			}}, nil
		}}
	start := time.Now()
	reply, err := a.Gateway.Call(ctx, identity, request)
	outcome.LatencyMs, outcome.Model = time.Since(start).Milliseconds(), reply.ModelID
	if err == nil {
		if err := check(ctx); err != nil {
			outcome.Deviations = []string{"access_ended"}
			return outcome
		}
		outcome.Deviations = evaluateAssertions(strings.TrimSpace(reply.Reply.Text), data.checks, ids)
		outcome.Passed = len(outcome.Deviations) == 0
	}
	return outcome
}

func (a Activities) Finish(ctx context.Context, run string) error {
	_, err := a.DB.Exec(ctx, `with totals as (
	  select count(*) as n,count(*) filter(where state='completed') done,
	   count(*) filter(where baseline->>'passed'='true') baseline_pass,count(*) filter(where candidate->>'passed'='true') candidate_pass,
	   avg((baseline->>'latencyMs')::numeric) baseline_ms,avg((candidate->>'latencyMs')::numeric) candidate_ms,
	   jsonb_agg(jsonb_build_object('caseId',case_id,'baseline',baseline,'candidate',candidate) order by case_id) cases
	  from eval_run_cases where run_id=$1
	 ) update eval_runs r set state=case when space_role(s.space_id,r.actor) is distinct from 'space_admin' then 'revoked'
	    when totals.n=totals.done then 'completed' else 'failed' end,
	   finished_at=clock_timestamp(),diff_report_json=jsonb_build_object('cases',totals.cases,'caseCount',totals.n,
	    'baselinePassRate',round(totals.baseline_pass::numeric/nullif(totals.n,0),4),
	    'candidatePassRate',round(totals.candidate_pass::numeric/nullif(totals.n,0),4),
	    'baselineLatencyMs',round(totals.baseline_ms),'candidateLatencyMs',round(totals.candidate_ms))
	  from totals,eval_suites s where r.id=$1 and s.id=r.suite_id and r.state in ('running','revoked')`, run)
	return err
}
