package agentloop

import (
	"alliance/internal/database"
	"alliance/internal/llmgateway"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const delegateTool = "delegate_task"
const maxDelegateNodes = 8

type delegateSpec struct {
	Agent    string         `json:"id"`
	Revision int            `json:"revision"`
	Children []delegateSpec `json:"children,omitempty"`
	Fallback *externalRef   `json:"fallback,omitempty"`
}

type flatDelegate struct {
	path, parent string
	primary      externalRef
	fallback     *externalRef
}

func flattenDelegates(nodes []delegateSpec) ([]flatDelegate, error) {
	var result []flatDelegate
	var visit func([]delegateSpec, string, []string, int) error
	visit = func(nodes []delegateSpec, parent string, ancestors []string, depth int) error {
		if len(nodes) == 0 {
			return nil
		}
		if depth > 3 || len(nodes) > maxDelegateNodes || len(result)+len(nodes) > maxDelegateNodes {
			return web.Fail(422, "委派最多包含三层和八个位置")
		}
		siblings := map[string]bool{}
		for i, node := range nodes {
			primary := externalRef{ID: node.Agent, Revision: node.Revision}
			ids := []externalRef{primary}
			if node.Fallback != nil {
				ids = append(ids, *node.Fallback)
			}
			for _, ref := range ids {
				id, err := uuid.Parse(ref.ID)
				loop := false
				for _, ancestor := range ancestors {
					loop = loop || ancestor == ref.ID
				}
				if err != nil || id == uuid.Nil || id.String() != ref.ID || ref.Revision < 1 || loop || siblings[ref.ID] {
					return web.Fail(422, "请为委派选择有效版本，且同一路径不能重复使用同一个 Agent")
				}
				siblings[ref.ID] = true
			}
			path := strconv.Itoa(i + 1)
			if parent != "" {
				path = parent + "." + path
			}
			result = append(result, flatDelegate{path, parent, primary, node.Fallback})
			next := append([]string(nil), ancestors...)
			for _, ref := range ids {
				next = append(next, ref.ID)
			}
			if err := visit(node.Children, path, next, depth+1); err != nil {
				return err
			}
		}
		if len(result) > maxDelegateNodes {
			return web.Fail(422, "委派最多包含八个位置")
		}
		return nil
	}
	err := visit(nodes, "", nil, 1)
	return result, err
}

func bindDelegates(ctx context.Context, tx pgx.Tx, run, project, actor string, entries []flatDelegate) error {
	for _, node := range entries {
		choices := []externalRef{node.primary}
		if node.fallback != nil {
			choices = append(choices, *node.fallback)
		}
		for i, choice := range choices {
			tag, err := tx.Exec(ctx, `insert into general_delegate_bindings(run_id,path,parent,alternate,agent_id,revision,grant_id)
			 select $1,$2,$3,$4,a.id,v.revision,case when project_agent_access(null,a.id,v.revision,$8) then null else g.id end
			 from agents a join agent_revisions v on (v.agent_id,v.revision)=(a.id,a.published_revision)
			 left join effective_project_agent_grants g on (g.project_id,g.agent_id,g.revision)=($7,a.id,v.revision)
			 where (a.id,v.revision)=($5,$6) and v.tool is null and v.card is null
			  and not exists(select from agent_skill_bindings s where (s.agent_id,s.revision)=(a.id,v.revision))
			  and project_agent_admissible($7,a.id,v.revision,$8)`, run, node.path, node.parent, i == 1, choice.ID, choice.Revision, project, actor)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return web.Fail(409, "请选择当前获准、已发布且可独立执行的文本 Agent")
			}
		}
	}
	return nil
}

type delegateChoice struct{ Path, Name string }

func delegateChoices(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, run, parent string) ([]delegateChoice, error) {
	rows, err := q.Query(ctx, `select distinct on(b.path) b.path,r.name from general_delegate_bindings b
	 join agent_revisions r on (r.agent_id,r.revision)=(b.agent_id,b.revision)
	 where b.run_id=$1 and b.parent=$2 and general_delegate_usable(b.run_id,b.path,b.alternate)
	 order by b.path,b.alternate`, run, parent)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[delegateChoice])
}

func delegateDefinition(choices []delegateChoice) resources.ToolDefinition {
	type targetDescription struct {
		Target string `json:"target"`
		Agent  string `json:"agent"`
	}
	targets := make([]string, 0, len(choices))
	labels := make([]targetDescription, 0, len(choices))
	for _, choice := range choices {
		targets = append(targets, choice.Path)
		labels = append(labels, targetDescription{choice.Path, boundedText(web.TraceText(choice.Name), 160)})
	}
	description, _ := json.Marshal(labels)
	schema, _ := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{
			"target":  map[string]any{"type": "string", "enum": targets},
			"message": map[string]any{"type": "string", "maxLength": 4000},
		}, "required": []string{"target", "message"}, "additionalProperties": false,
	})
	return resources.ToolDefinition{Type: "function", Function: resources.ToolSchema{
		Name: delegateTool, Description: "向其中一个已授权的文本 Agent 委派一项业务子任务。仅发送完成子任务所需的文字。" + string(description),
		Parameters: schema}}
}

type delegateInput struct{ Target, Message string }
type delegateHop struct {
	path      string
	alternate bool
}

func parseDelegateInput(raw string) (delegateInput, bool) {
	var args delegateInput
	err := decodeToolArgs(raw, &args)
	args.Message = strings.TrimSpace(args.Message)
	return args, err == nil && args.Target != "" && args.Message != "" && len(args.Message) <= 8<<10 &&
		!strings.ContainsRune(args.Message, 0) && web.TraceText(args.Message) == args.Message
}

var errDelegationUnavailable = errors.New("delegated result unavailable")

func (a Activities) executeDelegation(ctx context.Context, ref ToolRef, d runData, raw string) (ToolResult, error) {
	args, valid := parseDelegateInput(raw)
	var branch []delegateHop
	var answer string
	var runErr error
	if valid {
		answer, branch, runErr = a.callDelegate(ctx, ref, d, "", args, nil)
	} else {
		runErr = errDelegationUnavailable
	}
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		head, err := loadRun(ctx, tx, ref.RunID)
		if err != nil || head.terminal() {
			return err
		}
		if current, err := delegationActionRunning(ctx, tx, ref); err != nil || !current {
			return err
		}
		if err := lockDelegateGrants(ctx, tx, ref.RunID, branch); err != nil {
			return err
		}
		current, err := delegatePathUsable(ctx, tx, ref.RunID, branch)
		if err != nil {
			return err
		}
		if !head.authorized || !current || runErr != nil || answer == "" {
			return a.recordObservation(ctx, tx, ref, "rejected", "委派任务未返回可用结果。", "", "",
				failureObservation(delegateTool, "delegation_unavailable", "请继续处理已授权的部分，或说明缺项。"))
		}
		for _, hop := range branch {
			tag, err := tx.Exec(ctx, `update general_delegate_attempts set adopted=true
			 where (run_id,turn,position,path,alternate)=($1,$2,$3,$4,$5) and state='received'`,
				ref.RunID, ref.Turn, ref.Position, hop.path, hop.alternate)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return errDelegationUnavailable
			}
		}
		return a.recordObservation(ctx, tx, ref, "completed", answer, "", "")
	})
	return ToolResult{}, err
}

func (a Activities) callDelegate(ctx context.Context, ref ToolRef, d runData, parent string, input delegateInput, ancestors []delegateHop) (string, []delegateHop, error) {
	for _, alternate := range []bool{false, true} {
		hop := delegateHop{path: input.Target, alternate: alternate}
		lineage := append(append([]delegateHop(nil), ancestors...), hop)
		var instructions string
		err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
			head, err := loadRun(ctx, tx, ref.RunID)
			if err != nil {
				return err
			}
			if head.state != "running" || !head.authorized {
				return errDelegationUnavailable
			}
			if current, err := delegationActionRunning(ctx, tx, ref); err != nil || !current {
				if err != nil {
					return err
				}
				return errDelegationUnavailable
			}
			current, err := delegatePathUsable(ctx, tx, ref.RunID, lineage)
			if err != nil {
				return err
			}
			if !current {
				return errDelegationUnavailable
			}
			return admitDelegateAttempt(ctx, tx, ref, parent, hop, &instructions)
		})
		if errors.Is(err, errDelegationUnavailable) || errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		children, err := delegateChoices(ctx, a.DB, ref.RunID, hop.path)
		if err != nil {
			return "", nil, err
		}
		request := llmgateway.Request{
			Tier: d.tier, MaxTokens: 1800, Messages: []resources.Message{
				{Role: "system", Content: "完成这一项获准的业务子任务。工作范围限于收到的文字。可调用给出的委派工具，或直接返回可供上级继续使用的答复。\n" + boundedText(web.TraceText(instructions), 8<<10)},
				{Role: "user", Content: input.Message},
			}}
		if len(children) > 0 {
			request.Tools = []resources.ToolDefinition{delegateDefinition(children)}
		}
		key := fmt.Sprintf("%s/delegate/%s/%t", actionScope(ref), hop.path, hop.alternate)
		model, callErr := a.Gateway.Call(ctx, llmgateway.CallIdentity{
			OperationKey: key, RunID: ref.RunID, TeamID: d.team, MemberID: d.actor, Turn: ref.Turn,
			Authorize: func(ctx context.Context) error {
				return database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
					head, err := loadRun(ctx, tx, ref.RunID)
					if err != nil {
						return err
					}
					allowed, err := delegatePathUsable(ctx, tx, ref.RunID, lineage)
					if err != nil {
						return err
					}
					if !head.authorized || head.state != "running" || !allowed {
						return errDelegationUnavailable
					}
					return nil
				})
			}}, request)
		state := "received"
		if callErr != nil {
			state = "failed"
		}
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 4*time.Second)
		current, err := a.settleDelegateAttempt(settle, ref, lineage, state)
		cancel()
		if err != nil {
			return "", nil, err
		}
		if errors.Is(callErr, llmgateway.ErrBudgetExceeded) {
			return "", nil, errDelegationUnavailable
		}
		if !current || callErr != nil {
			continue
		}
		if len(model.Reply.ToolCalls) == 0 {
			if answer := strings.TrimSpace(model.Reply.Text); answer != "" {
				return boundedText(answer, 8<<10), lineage, nil
			}
			continue
		}
		if len(model.Reply.ToolCalls) != 1 || model.Reply.ToolCalls[0].Function.Name != delegateTool || len(children) == 0 {
			continue
		}
		next, valid := parseDelegateInput(model.Reply.ToolCalls[0].Function.Arguments)
		if !valid {
			continue
		}
		answer, branch, err := a.callDelegate(ctx, ref, d, hop.path, next, lineage)
		if err == nil {
			return answer, branch, nil
		}
		if !errors.Is(err, errDelegationUnavailable) {
			return "", nil, err
		}
	}
	return "", nil, errDelegationUnavailable
}

func admitDelegateAttempt(ctx context.Context, tx pgx.Tx, ref ToolRef, parent string, hop delegateHop, instructions *string) error {
	var grant *string
	err := tx.QueryRow(ctx, `select v.instructions,b.grant_id from general_delegate_bindings b
	 join agent_revisions v on (v.agent_id,v.revision)=(b.agent_id,b.revision)
	 where (b.run_id,b.path,b.parent,b.alternate)=($1,$2,$3,$4)`, ref.RunID, hop.path, parent, hop.alternate).
		Scan(instructions, &grant)
	if err != nil {
		return err
	}
	if grant != nil {
		var cap, used int
		if err := tx.QueryRow(ctx, `select g.call_limit from project_agent_grants g where g.id=$1 and g.revoked_at is null for update`, grant).Scan(&cap); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select project_agent_grant_usage($1)", grant).Scan(&used); err != nil {
			return err
		}
		if used >= cap {
			return errDelegationUnavailable
		}
	}
	tag, err := tx.Exec(ctx, `insert into general_delegate_attempts(run_id,turn,position,path,alternate)
	 select $1,$2,$3,$4,$5 where (select count(*) from general_delegate_attempts where run_id=$1)<$6
	 on conflict do nothing`, ref.RunID, ref.Turn, ref.Position, hop.path, hop.alternate, maxDelegateNodes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errDelegationUnavailable
	}
	return nil
}

func (a Activities) settleDelegateAttempt(ctx context.Context, ref ToolRef, lineage []delegateHop, state string) (bool, error) {
	hop := lineage[len(lineage)-1]
	var usable bool
	err := database.WithTx(ctx, a.DB, func(tx pgx.Tx) error {
		head, err := loadRun(ctx, tx, ref.RunID)
		if err != nil {
			return err
		}
		current, err := delegatePathUsable(ctx, tx, ref.RunID, lineage)
		if err != nil {
			return err
		}
		active, err := delegationActionRunning(ctx, tx, ref)
		if err != nil {
			return err
		}
		usable = current && active && head.authorized && head.state == "running"
		if !usable {
			state = "discarded"
		}
		_, err = tx.Exec(ctx, `update general_delegate_attempts set state=$6,checked_at=clock_timestamp()
		 where (run_id,turn,position,path,alternate)=($1,$2,$3,$4,$5) and state='submitted'`,
			ref.RunID, ref.Turn, ref.Position, hop.path, hop.alternate, state)
		return err
	})
	return usable, err
}

func delegationActionRunning(ctx context.Context, tx pgx.Tx, ref ToolRef) (bool, error) {
	var active bool
	err := tx.QueryRow(ctx, `select state='running' from general_agent_actions where (run_id,turn,position)=($1,$2,$3) for update`,
		ref.RunID, ref.Turn, ref.Position).Scan(&active)
	return active, err
}

func delegatePathArgs(path []delegateHop) ([]string, []bool) {
	names, backups := make([]string, len(path)), make([]bool, len(path))
	for i, hop := range path {
		names[i], backups[i] = hop.path, hop.alternate
	}
	return names, backups
}

func lockDelegateGrants(ctx context.Context, tx pgx.Tx, run string, path []delegateHop) error {
	names, backups := delegatePathArgs(path)
	_, err := tx.Exec(ctx, `select g.id from general_delegate_bindings b
	 join unnest($2::text[],$3::boolean[]) hop(path,alternate) using(path,alternate)
	 join project_agent_grants g on g.id=b.grant_id where b.run_id=$1 order by g.id for share of g`, run, names, backups)
	return err
}

func delegatePathUsable(ctx context.Context, tx pgx.Tx, run string, path []delegateHop) (bool, error) {
	names, backups := delegatePathArgs(path)
	var usable bool
	err := tx.QueryRow(ctx, `select not exists(select from unnest($2::text[],$3::boolean[]) hop(path,alternate)
	 where not general_delegate_usable($1,hop.path,hop.alternate))`, run, names, backups).Scan(&usable)
	return usable, err
}

func loadDelegateSpec(ctx context.Context, tx pgx.Tx, run string) ([]delegateSpec, error) {
	rows, err := tx.Query(ctx, `select p.path,p.parent,p.agent_id,p.revision,f.agent_id,f.revision
	 from general_delegate_bindings p left join general_delegate_bindings f on f.run_id=p.run_id and f.path=p.path and f.alternate
	 where p.run_id=$1 and not p.alternate order by p.path`, run)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byParent := map[string][]flatDelegate{}
	for rows.Next() {
		var node flatDelegate
		var fallbackID *string
		var fallbackRevision *int
		if err := rows.Scan(&node.path, &node.parent, &node.primary.ID, &node.primary.Revision, &fallbackID, &fallbackRevision); err != nil {
			return nil, err
		}
		if fallbackID != nil && fallbackRevision != nil {
			node.fallback = &externalRef{ID: *fallbackID, Revision: *fallbackRevision}
		}
		byParent[node.parent] = append(byParent[node.parent], node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var build func(string) []delegateSpec
	build = func(parent string) []delegateSpec {
		var nodes []delegateSpec
		for _, child := range byParent[parent] {
			nodes = append(nodes, delegateSpec{Agent: child.primary.ID, Revision: child.primary.Revision, Fallback: child.fallback, Children: build(child.path)})
		}
		return nodes
	}
	return build(""), nil
}
