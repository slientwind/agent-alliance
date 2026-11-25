package resources

import (
	"alliance/internal/billing"
	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"bytes"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

func (c Catalog) saveTrial(ctx context.Context, id string, m Model, expected string, reply Reply, callErr error, lease, operation string) (string, string, error) {
	status, body := "failed", reply.Text
	receipt := reply.Receipt
	if callErr != nil {
		status = "unknown"
		body = "调用结果待核对"
		var invalid *ToolInputError
		if errors.As(callErr, &invalid) {
			status, body = "failed", invalid.Reason
		} else if errors.As(callErr, &receipt) {
			switch receipt.State {
			case "canceled", "failed", "rejected", "completed":
				status = "failed"
			default:
				status = "remote"
			}
			body = "远端状态 · " + receipt.State
		} else if lease != "" {
			status = "remote"
			body = "查询未取得有效回执，仍可核对原任务"
			if operation == "supplement" {
				body = "补件响应待核对，请查询原任务"
			}
		}
	} else if strings.Contains(body, expected) {
		status = "passed"
	}
	if errors.Is(callErr, ErrImageFormat) || errors.Is(callErr, ErrSlideContract) || errors.Is(callErr, ErrOfficeFile) {
		status, body = "failed", callErr.Error()
	}
	if IsCredentialRejection(callErr) {
		status = "failed"
		body = "供应商拒绝此凭据"
	}
	var task, correlation, state, tokens any
	var question, questionMessage string
	if receipt != nil {
		task = receipt.Task
		correlation = receipt.Context
		state = receipt.State
		question = receipt.Question
		questionMessage = receipt.QuestionMessage
	}
	if callErr == nil && !m.External && m.Tool == nil && !ProducesPNG(m.OutputOperation) {
		tokens = reply.Tokens
	}
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var agent string
		if err := tx.QueryRow(ctx, `select a.id from validations v join agents a on a.id=v.agent_id
            where v.id=$1 for no key update of a`, id).Scan(&agent); err != nil {
			return err
		}
		discardFile := false
		if callErr == nil && len(m.SkillRefs) != 0 {
			var current bool
			if err := tx.QueryRow(ctx, `select coalesce(bool_and(usable),false) from (
                select s.revoked_at is null and exists(select from team_members mem join members u on u.id=mem.member_id and u.enabled
                  where (mem.team_id,mem.member_id)=(p.team_id,v.actor)) as usable
                from validation_skill_inputs used join validations v on v.id=used.trial_id
                join skill_revisions s on (s.skill_id,s.revision)=(used.skill_id,used.skill_revision)
                join skill_packs p on p.id=s.skill_id
                where v.id=$1 for share of s
            ) pinned`, id).Scan(&current); err != nil {
				return err
			}
			if !current {
				status, body, discardFile = "failed", "试运行采用的 Skill 已停用或超出授权范围，请核对修订后复验", true
			}
		}
		if status == "passed" {
			matches, err := builder.TrialMatches(ctx, tx, id)
			if err != nil {
				return err
			}
			if !matches {
				status, body, discardFile = "failed", "试运行期间 Agent 定义已变化，请核对修订后复验", true
			}
		}
		if err := ObserveKeyRejection(ctx, tx, m, callErr); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `update validations v set status=case when $1='passed' and $2<>v.expected and exists(select from agent_checks c where c.id=v.check_id and c.match_kind='exact') then 'failed' else $1 end,
            evidence=$2,tokens=$3,remote_task=coalesce($4,v.remote_task),remote_context=coalesce($5,v.remote_context),
            remote_input_revision=v.remote_input_revision+q.fresh::int,
            remote_input_prompt=case when q.fresh then $9 else v.remote_input_prompt end,
            remote_input_message=case when q.fresh or ($6='input-required' and $10<>'') then $10 else v.remote_input_message end,
            remote_status=coalesce($6,v.remote_status),ended_at=case when $1='remote' then null else now() end,follow_lease=null,follow_until=null
            from validations prior
            cross join lateral (select a2a_new_question(prior.remote_status,prior.remote_input_prompt,prior.remote_input_message,$6,$9,$10) fresh) q
            where v.id=prior.id and v.id=$7 and v.follow_lease is not distinct from nullif($8,'')::uuid returning v.status`,
			status, body, tokens, task, correlation, state, id, lease, question, questionMessage).Scan(&status)
		if err == pgx.ErrNoRows {
			err = web.Fail(409, "此回执已由另一次核对接管")
		}
		if err == nil && m.External {
			outcome := remoteTrialOutcome(reply, callErr)
			_, err = tx.Exec(ctx, `insert into remote_trial_observations(trial_id,operation,outcome,remote_state)
                values($1,$2,$3,$4)`, id, operation, outcome, state)
			if err == nil {
				kind := operation
				switch operation {
				case "send":
					kind = "submit"
				case "query":
					kind = "poll"
				}
				err = RecordExternalReceipt(ctx, tx, ExternalReceiptRef{Request: "trial/external/" + id + "/" + lease,
					Trial: id, Operation: kind}, reply.ExternalMetadata)
			}
		}
		if err != nil || callErr != nil || reply.File == nil || discardFile {
			return err
		}
		hash, _, err := knowledge.ConfiguredObjects().Put(ctx, tx, bytes.NewReader(reply.File.Data), 6<<20)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into validation_files values($1,$2,$3,$4)", id, hash, reply.File.Name, reply.File.Mime)
		return err
	})

	return status, body, err
}

func remoteTrialOutcome(reply Reply, callErr error) string {
	if callErr == nil && !web.Blank(reply.Text) {
		return "text"
	}
	var receipt *RemoteWait
	if errors.As(callErr, &receipt) && receipt.State != "unknown" {
		return "state"
	}
	return "unconfirmed"
}

func (c Catalog) trialReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Input    string
		InputRevision    int
		AuthorizeOneCall bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	supplement := in.Action == "supplement"
	if supplement {
		input, err := RemoteInput(in.Input)
		if err != nil {
			return err
		}
		in.Input = input
		if in.InputRevision < 1 || !in.AuthorizeOneCall {
			return web.Fail(422, "请核对当前补件项并授权一次未知费用的调用")
		}
	} else if in.Action != "check" && in.Action != "cancel" || in.Input != "" || in.InputRevision != 0 || in.AuthorizeOneCall {
		return web.Fail(422, "请选择查询、取消或补充原试运行")
	}
	ctx := r.Context()
	agent, id := r.PathValue("id"), r.PathValue("trial")
	var receipt RemoteWait
	var rev int
	var lease, expected, priorStatus, priorBody string
	var model Model
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		var busy, cancelled bool
		var status, output, project string
		var inputRevision int
		err := tx.QueryRow(ctx, `select v.revision,v.expected,coalesce(v.remote_task,''),coalesce(v.remote_context,''),coalesce(v.remote_status,''),
            coalesce(v.follow_until>now(),false),v.cancel_requested,v.status,v.evidence,v.remote_input_revision,coalesce(s.project_id::text,'')
            from validations v left join spend s on s.op_key='validation/'||v.id::text
            where v.id=$1 and v.agent_id=$2 and v.actor=$3 for update of v`, id, agent, u.ID).
			Scan(&rev, &expected, &receipt.Task, &receipt.Context, &receipt.State, &busy, &cancelled, &status, &output, &inputRevision, &project)
		if err != nil {
			return err
		}
		if supplement {
			var same bool
			err := tx.QueryRow(ctx, `select signature=$3 from remote_trial_inputs where (trial_id,input_revision)=($1,$2)`,
				id, in.InputRevision, web.Hash(in.Input)).Scan(&same)
			if err == nil {
				if !same {
					return web.Fail(409, "本轮已有补件，请核对原试运行")
				}
				priorStatus, priorBody = status, output
				return nil
			}
			if err != pgx.ErrNoRows {
				return err
			}
			if inputRevision != in.InputRevision || receipt.State != "input-required" || receipt.Context == "" || cancelled {
				return web.Fail(409, "原试运行补件项已经变化")
			}
		}
		if status != "remote" || receipt.Task == "" {
			return pgx.ErrNoRows
		}
		if busy || (cancelled && in.Action == "cancel") {
			return web.Fail(409, "已有查询或取消请求，请查看原回执")
		}
		matches, err := builder.TrialMatches(ctx, tx, id)
		if err != nil {
			return err
		}
		if !matches {
			return web.Fail(409, "原试运行的远端配置已变化，请由维护人核对原回执")
		}
		model, _, err = c.Resolve(ctx, tx, u.ID, agent, rev)
		if err != nil {
			return err
		}
		if supplement {
			if project != "" {
				var allowed bool
				if err := tx.QueryRow(ctx, `select exists(select from projects p join project_members m on m.project_id=p.id
                    where p.id=$1 and m.member_id=$2 and (p.owner=$2 or m.can_review)
                    and p.stage not in ('delivered','adopted','cancelled','failed'))`, project, u.ID).Scan(&allowed); err != nil {
					return err
				}
				if !allowed {
					return web.Fail(403, "项目试运行授权已结束")
				}
			}
			lease = uuid.NewString()
			if err := billing.Reserve(ctx, tx, billing.Operation{Key: "validation-input/" + lease, Kind: "remote-input", Actor: u.ID,
				Project: project, Team: model.Team, Resource: model.BudgetID, SourceRevision: model.SourceRevision, SourceName: model.Name, AllowUnknown: true}); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "insert into remote_trial_inputs(id,trial_id,input_revision,signature) values($1,$2,$3,$4)",
				lease, id, in.InputRevision, web.Hash(in.Input)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "update validations set follow_lease=$2,follow_until=now()+interval '100 seconds' where id=$1", id, lease)
			return err
		}
		return tx.QueryRow(ctx, `update validations set follow_lease=gen_random_uuid(),follow_until=now()+interval '35 seconds',cancel_requested=cancel_requested or $2 where id=$1 returning follow_lease`, id, in.Action == "cancel").Scan(&lease)
	})
	if err != nil {
		return err
	}
	if priorStatus != "" {
		return web.JSON(w, 200, map[string]string{"id": id, "status": priorStatus, "output": priorBody})
	}
	operation := "query"
	model.ExternalTrace = ExternalTrace{RunID: id, ActorID: u.ID, Turn: 1}
	var reply Reply
	var callErr error
	if supplement {
		operation = "supplement"
		reply, callErr = model.SupplementRemote(ctx, receipt, lease, in.Input)
	} else {
		if in.Action == "cancel" {
			operation = "cancel"
		}
		reply, callErr = model.CheckRemote(ctx, receipt, in.Action == "cancel")
	}
	status, body, err := c.saveTrial(context.WithoutCancel(ctx), id, model, expected, reply, callErr, lease, operation)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"id": id, "status": status, "output": body})
}
