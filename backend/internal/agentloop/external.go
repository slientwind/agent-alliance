package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"alliance/internal/billing"
	"alliance/internal/database"
	"alliance/internal/egress"
	"alliance/internal/harness"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type externalRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

type externalTool struct {
	id, label, recipient string
}

type externalWork struct {
	model   resources.Model
	receipt resources.RemoteWait
	poll    int
}

const externalTaskSchema = `{"type":"object","properties":{"message":{"type":"string","maxLength":8192}},"required":["message"],"additionalProperties":false}`

func validateExternalSelection(in StartInput) error {
	if len(in.External) > 8 || in.Direct && (len(in.External) != 1 || len(in.Goal) > 8<<10 ||
		len(in.Sources) != 0 || in.SearchQuery != "") {
		return web.Fail(422, "请选择要使用的外部 Agent。直连时只发送本条文字消息")
	}
	seen := make(map[string]bool)
	for _, ref := range in.External {
		id, err := uuid.Parse(ref.ID)
		if err != nil || id.String() != ref.ID || ref.Revision < 1 || seen[ref.ID] {
			return web.Fail(422, "请核对所选外部 Agent 和版本")
		}
		seen[ref.ID] = true
	}
	return nil
}

func (s Server) bindExternalAgents(ctx context.Context, tx pgx.Tx, run, actor string, refs []externalRef, latestOnly bool) error {
	catalog := resources.Catalog{DB: s.DB, Vault: s.Vault}
	for _, ref := range refs {
		tag, err := tx.Exec(ctx, `insert into general_external_agents(run_id,agent_id,revision)
		 select $1,x.agent_id,x.revision from external_agent_admissions x join agents a on a.id=x.agent_id
		 where (x.agent_id,x.revision)=($2,$3) and external_admission_can_use(x.agent_id,x.revision,$4)
		 and (not $5 or x.revision=a.published_revision or x.revision=(select max(revision) from agent_revisions where agent_id=a.id))`,
			run, ref.ID, ref.Revision, actor, latestOnly)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "这个外部 Agent 的版本或使用权限已变化")
		}
		model, _, err := catalog.ResolveRun(ctx, tx, "", actor, ref.ID, ref.Revision)
		if err != nil || !model.External || !model.RemoteOperations.CanStart() {
			return web.Fail(409, "这个外部 Agent 当前无法使用")
		}
	}
	return nil
}

func externalToolName(id string) string { return "external_" + strings.ReplaceAll(id, "-", "") }

func (a Activities) externalTools(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, run string) ([]externalTool, error) {
	rows, err := q.Query(ctx, `select b.agent_id,r.name,r.rpc_endpoint from general_external_agents b
	 join agent_revisions r on (r.agent_id,r.revision)=(b.agent_id,b.revision)
	 where b.run_id=$1 and general_external_binding_current(b.run_id,b.agent_id) order by b.agent_id`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tools []externalTool
	for rows.Next() {
		var tool externalTool
		var endpoint string
		if err := rows.Scan(&tool.id, &tool.label, &endpoint); err != nil {
			return nil, err
		}
		address, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		tool.recipient = address.Host
		tools = append(tools, tool)
	}
	return tools, rows.Err()
}

func (a Activities) directExternalTurn(ctx context.Context, tx pgx.Tx, ref TurnRef, d runData) (TurnResult, error) {
	tools, err := a.externalTools(ctx, tx, ref.RunID)
	if err != nil {
		return TurnResult{}, err
	}
	if len(tools) != 1 || ref.Turn != 1 {
		return TurnResult{Done: true}, finishGeneralRun(ctx, tx, ref.RunID, d, "failed", "外部 Agent 当前无法使用。")
	}
	tool := tools[0]
	args, err := json.Marshal(map[string]string{"message": d.goal})
	if err != nil {
		return TurnResult{}, err
	}
	cipher, err := a.Vault.Seal(string(args), actionScope(ToolRef{ref.RunID, 1, 1})+"/arguments")
	if err != nil {
		return TurnResult{}, err
	}
	if _, err := tx.Exec(ctx, `insert into general_agent_turns(run_id,turn,state,response) values($1,1,'tools',''::bytea)`, ref.RunID); err != nil {
		return TurnResult{}, err
	}
	name := externalToolName(tool.id)
	if _, err := tx.Exec(ctx, `insert into general_agent_actions(run_id,turn,position,tool_name,call_id,args_cipher,fingerprint,
	 external_agent,external_label,external_recipient) values($1,1,1,$2,$2,$3,$4,$5,$6,$7)`,
		ref.RunID, name, cipher, fmt.Sprintf("%x", web.Hash(name+string(args))), tool.id, tool.label, tool.recipient); err != nil {
		return TurnResult{}, err
	}
	_, err = tx.Exec(ctx, "update runs set status='running' where id=$1", ref.RunID)
	return TurnResult{Tools: 1}, err
}

func (a Activities) prepareExternal(ctx context.Context, tx pgx.Tx, ref ToolRef, d runData, task *toolWork) error {
	var rev int
	err := tx.QueryRow(ctx, `select revision from general_external_agents where (run_id,agent_id)=($1,$2)
	 and general_external_binding_current(run_id,agent_id)`, ref.RunID, task.external).Scan(&rev)
	if err != nil {
		return err
	}
	catalog := resources.Catalog{DB: a.DB, Vault: a.Vault}
	model, _, err := catalog.ResolveRun(ctx, tx, "", d.actor, task.external, rev)
	if err != nil {
		return err
	}
	model.Operation = actionScope(ref) + "/external"
	model.ExternalTrace = resources.ExternalTrace{RunID: ref.RunID, ActorID: d.actor, Turn: (ref.Turn-1)*maxToolsPerTurn + ref.Position}
	model.AuthorizeExternal = func(ctx context.Context) error {
		var allowed bool
		err := a.DB.QueryRow(ctx, `select exists(select from general_agent_actions a join runs r on r.id=a.run_id
		 join members u on u.id=r.actor and u.enabled join projects p on p.id=r.project_id and p.plan_version=r.plan_version
		 join project_members pm on (pm.project_id,pm.member_id)=(r.project_id,r.actor)
		 join team_members tm on (tm.team_id,tm.member_id)=(r.charge_team,r.actor)
		 where (a.run_id,a.turn,a.position)=($1,$2,$3) and a.state='running' and r.status='running'
		  and general_external_binding_current(a.run_id,a.external_agent))`, ref.RunID, ref.Turn, ref.Position).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return web.Fail(409, "外部 Agent 使用权限已变化")
		}
		if !d.direct {
			return a.verifyApproval(ctx, ref, harness.L2, task.fingerprint)
		}
		return nil
	}
	task.remote.model = model
	return nil
}

func (a Activities) startExternal(ctx context.Context, tx pgx.Tx, ref ToolRef, d runData, task *toolWork) error {
	if err := a.prepareExternal(ctx, tx, ref, d, task); err != nil {
		return err
	}
	var args struct{ Message string }
	if err := decodeToolArgs(task.args, &args); err != nil || strings.TrimSpace(args.Message) == "" || len(args.Message) > 8<<10 || strings.ContainsRune(args.Message, 0) {
		return harness.ErrToolArguments
	}
	task.args = args.Message
	m := task.remote.model
	return billing.Reserve(ctx, tx, billing.Operation{Key: m.Operation, Kind: "execution", Actor: d.actor, Team: m.Team,
		Resource: m.BudgetID, SourceRevision: m.SourceRevision, SourceName: m.Name, Run: ref.RunID, Project: d.project,
		Estimate: m.Estimate, AllowUnknown: true})
}

func (a Activities) pollExternal(ctx context.Context, tx pgx.Tx, ref ToolRef, d runData, task *toolWork, result *ToolResult) (bool, error) {
	var cipher []byte
	var free, expired bool
	err := tx.QueryRow(ctx, `select remote_receipt,coalesce(poll_until<=now(),true),expires_at<=now()
	 from general_external_jobs where (run_id,turn,position)=($1,$2,$3) for update`, ref.RunID, ref.Turn, ref.Position).
		Scan(&cipher, &free, &expired)
	if errors.Is(err, pgx.ErrNoRows) || expired {
		return false, a.rejectExternal(ctx, tx, ref, d, result, "未收到外部 Agent 的最终答复。请核对原请求后再试。")
	}
	if err != nil {
		return false, err
	}
	if err := a.prepareExternal(ctx, tx, ref, d, task); err != nil {
		return false, a.handleExternalPreparation(ctx, tx, ref, d, result, err)
	}
	raw, err := a.Vault.Open(cipher, actionScope(ref)+"/remote-receipt")
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), &task.remote.receipt); err != nil {
		return false, err
	}
	if !free {
		result.Wait, result.PollSeconds = true, task.remote.model.Policy.IntervalSeconds
		return false, nil
	}
	err = tx.QueryRow(ctx, `update general_external_jobs set poll_count=poll_count+1,poll_until=now()+interval '90 seconds'
	 where (run_id,turn,position)=($1,$2,$3) returning poll_count`, ref.RunID, ref.Turn, ref.Position).Scan(&task.remote.poll)
	return err == nil, err
}

func (a Activities) handleExternalPreparation(ctx context.Context, tx pgx.Tx, ref ToolRef, d runData, result *ToolResult, err error) error {
	var problem web.Problem
	if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, harness.ErrToolArguments) || errors.As(err, &problem) {
		return a.rejectExternal(ctx, tx, ref, d, result, "外部 Agent 无法使用，请检查所选版本、发送内容和用量额度。")
	}
	return err
}

func (a Activities) rejectExternal(ctx context.Context, tx pgx.Tx, ref ToolRef, d runData, result *ToolResult, note string) error {
	if err := a.recordObservation(ctx, tx, ref, "rejected", note, "", ""); err != nil {
		return err
	}
	if d.direct {
		result.Done = true
		return finishGeneralRun(ctx, tx, ref.RunID, d, "failed", note)
	}
	return nil
}

func (a Activities) executeExternal(ctx context.Context, ref ToolRef, task toolWork) (ToolResult, error) {
	var reply resources.Reply
	var callErr error
	operation := "submit"
	m := task.remote.model
	if task.remote.poll > 0 {
		operation = "poll"
		reply, callErr = m.CheckRemote(ctx, task.remote.receipt, false)
	} else {
		reply, callErr = m.Generate(ctx, []resources.Message{{Role: "user", Content: task.args}})
	}
	var result ToolResult
	ctx = context.WithoutCancel(ctx)
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		d, err := loadRun(ctx, tx, ref.RunID)
		if err != nil {
			return err
		}
		if err := resources.RecordExternalReceipt(ctx, tx, resources.ExternalReceiptRef{
			Request: fmt.Sprintf("%s/%s/%d", m.Operation, operation, task.remote.poll), Run: ref.RunID,
			Turn: m.ExternalTrace.Turn, Attempt: 1, Operation: operation}, reply.ExternalMetadata); err != nil {
			return err
		}
		if d.terminal() {
			result.Done = true
			return nil
		}
		var state string
		if err := tx.QueryRow(ctx, `select state from general_agent_actions
		 where (run_id,turn,position)=($1,$2,$3) for update`, ref.RunID, ref.Turn, ref.Position).Scan(&state); err != nil || state != "running" {
			return err
		}
		var current bool
		if err := tx.QueryRow(ctx, "select general_external_binding_current($1,$2)", ref.RunID, task.external).Scan(&current); err != nil {
			return err
		}
		if !current || !d.authorized || !(egress.Policy{DB: tx}).Allowed(ctx, m.Endpoint, false) {
			return a.rejectExternal(ctx, tx, ref, d, &result, "外部 Agent 权限在执行期间已变化。")
		}
		if task.remote.poll > 0 {
			tag, err := tx.Exec(ctx, `update general_external_jobs set poll_until=null where (run_id,turn,position)=($1,$2,$3) and poll_count=$4`,
				ref.RunID, ref.Turn, ref.Position, task.remote.poll)
			if err != nil || tag.RowsAffected() == 0 {
				result.Wait, result.PollSeconds = true, m.Policy.IntervalSeconds
				return err
			}
		}
		var receipt *resources.RemoteWait
		if errors.As(callErr, &receipt) && receipt.Task != "" && m.RemoteOperations.CanFollow(false) &&
			(receipt.State == "submitted" || receipt.State == "working") {
			body, err := json.Marshal(receipt)
			if err != nil {
				return err
			}
			cipher, err := a.Vault.Seal(string(body), actionScope(ref)+"/remote-receipt")
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `insert into general_external_jobs(run_id,turn,position,remote_receipt,expires_at)
			 values($1,$2,$3,$4,now()+make_interval(secs=>$5)) on conflict(run_id,turn,position)
			 do update set remote_receipt=excluded.remote_receipt`, ref.RunID, ref.Turn, ref.Position, cipher, m.Policy.WindowSeconds)
			result.Wait, result.PollSeconds = true, m.Policy.IntervalSeconds
			return err
		}
		if callErr != nil || strings.TrimSpace(reply.Text) == "" {
			if task.remote.poll > 0 && receipt == nil && !resources.IsCredentialRejection(callErr) {
				result.Wait, result.PollSeconds = true, m.Policy.IntervalSeconds
				return nil
			}
			return a.rejectExternal(ctx, tx, ref, d, &result, "外部 Agent 未返回可确认的答复。")
		}
		var artifact string
		if d.direct {
			artifact, err = a.saveFinalReply(ctx, tx, ref.RunID, d.project, "外部 Agent 答复", reply)
			if err != nil {
				return err
			}
		}
		if err := a.recordObservation(ctx, tx, ref, "completed", boundedText(reply.Text, 8192), artifact, ""); err != nil {
			return err
		}
		if d.direct {
			if d.state == "paused" {
				result.Wait = true
				return nil
			}
			result.Done = true
			return finishGeneralRun(ctx, tx, ref.RunID, d, "completed", "外部 Agent 已回复。")
		}
		return nil
	})
	return result, err
}
