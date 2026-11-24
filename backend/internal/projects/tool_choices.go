package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type ToolChoicePin struct {
	Step        int    `json:"step"`
	Selector    int    `json:"selector"`
	Alternative string `json:"alternative"`
	Revision    int    `json:"revision"`
}

func saveToolChoices(ctx context.Context, tx pgx.Tx, project string, version int, actor string, nodes []executionNode, pins []ToolChoicePin) error {
	if len(pins) == 0 {
		return nil
	}
	businessSteps := make([]int, 0, len(nodes))
	for i, n := range nodes {
		if n.phase != "answer" && n.phase != "answer_calc" && n.phase != "synthesize" {
			businessSteps = append(businessSteps, i+1)
		}
	}
	selectors := map[int]bool{}
	targets := map[int]bool{}
	for _, pin := range pins {
		if pin.Selector < 1 || pin.Step > len(businessSteps) || pin.Step <= pin.Selector || pin.Revision < 1 || pin.Alternative == "" || selectors[pin.Selector] || targets[pin.Step] {
			return web.Fail(422, "请选择两个不同的已发布汇总能力和前置的文本 Agent")
		}
		selectors[pin.Selector] = true
		targets[pin.Step] = true
		step, selector := businessSteps[pin.Step-1], businessSteps[pin.Selector-1]
		n := nodes[step-1]
		if n.phase != "" || nodes[selector-1].phase != "" || len(n.dependencies) != 1 || n.dependencies[0] != selector || len(nodes[selector-1].dependencies) > 0 {
			return web.Fail(422, "汇总步骤需直接引用一个可独立开始的文本 Agent 作为选择步骤")
		}
		tag, err := tx.Exec(ctx, `insert into plan_tool_choices
 select p.id,$2,primary_node.ordinal,chooser.ordinal,alt.agent_id,alt.revision
 from projects p
 join nodes primary_node on (primary_node.project_id,primary_node.plan_version,primary_node.ordinal)=(p.id,$2,$3)
 join nodes chooser on (chooser.project_id,chooser.plan_version,chooser.ordinal)=(p.id,$2,$4)
 join agent_revisions picked on (picked.agent_id,picked.revision)=(primary_node.agent_id,primary_node.agent_revision)
 join agent_revisions alt on (alt.agent_id,alt.revision)=($5,$6) and alt.agent_id<>picked.agent_id
 join agent_revisions selector on (selector.agent_id,selector.revision)=(chooser.agent_id,chooser.agent_revision)
 join agents one on one.id=picked.agent_id and one.team_id=p.team_id
 join agents two on two.id=alt.agent_id and two.team_id=p.team_id
 and (two.published_revision=alt.revision or exists(select from plan_tool_choices previous
   where (previous.project_id,previous.plan_version,previous.alternative,previous.alternative_revision)=(p.id,p.plan_version,alt.agent_id,alt.revision)))
 join agents model_agent on model_agent.id=selector.agent_id and model_agent.team_id=p.team_id
 where p.id=$1 and picked.tool->>'operation'='aggregate' and alt.tool->>'operation'='aggregate'
 and selector.resource_id is not null and agent_output_operation(selector)='text' and not agent_uses_picture(selector)
 and agent_admissible(alt.agent_id,alt.revision,$7)
 and exists(select from plan_sources ps join source_versions v on (v.source_id,v.version)=(ps.source_id,ps.source_version)
   where (ps.project_id,ps.plan_version)=(p.id,$2) and lower(v.filename) like '%.csv')
 and not exists(select from plan_sources ps join source_versions v on (v.source_id,v.version)=(ps.source_id,ps.source_version)
   where (ps.project_id,ps.plan_version)=(p.id,$2) and coalesce(lower(v.filename),'') not like '%.csv')`, project, version, step, selector, pin.Alternative, pin.Revision, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(422, "自动选择需要本团队已验证的文本 Agent、两个不同的已发布汇总能力及本方案的 CSV 资料")
		}
	}
	return nil
}

func (s Server) confirmToolChoice(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Step, Choice, Expected int }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if (in.Choice != 1 && in.Choice != 2) || in.Step < 1 || in.Step > 12 || key == "" || len(key) > 180 {
		return web.Fail(422, "请选择原方案中的汇总能力并提供幂等键")
	}
	ctx, run := r.Context(), r.PathValue("id")
	var controlRevision int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var project, state, agent string
		var current, revision, modelChoice int
		var prior *int
		err := tx.QueryRow(ctx, `select r.project_id,r.status,r.control_revision,routed.choice,routed.manual_choice,
      case when $4=2 then c.alternative else n.agent_id end,case when $4=2 then c.alternative_revision else n.agent_revision end
   from runs r join projects p on p.id=r.project_id join project_members pm on (pm.project_id,pm.member_id)=(p.id,$2)
   join members m on m.id=pm.member_id and m.enabled
   join run_tool_choices routed on routed.run_id=r.id and routed.ordinal=$3
   join plan_tool_choices c on(c.project_id,c.plan_version,c.ordinal)=(r.project_id,r.plan_version,routed.ordinal)
   join nodes n on(n.project_id,n.plan_version,n.ordinal)=(c.project_id,c.plan_version,c.ordinal)
   where r.id=$1 and m.kind='person' and (p.owner=$2 or (r.app_id is not null and r.delegated_by is null and p.final_reviewer=$2 and pm.can_review)) for update of r,routed for share of pm,m`, run, u.ID, in.Step, in.Choice).Scan(&project, &state, &current, &modelChoice, &prior, &agent, &revision)
		if err != nil {
			return err
		}
		if modelChoice != 0 {
			return web.Fail(409, "Agent 已选定工具，请按当前运行处理")
		}
		reason := fmt.Sprintf("手动选择汇总步骤 %d 的候选 %d", in.Step, in.Choice)
		if prior != nil {
			if *prior != in.Choice {
				return web.Fail(409, "汇总工具已经选定")
			}
			if err := tx.QueryRow(ctx, `select revision from controls where run_id=$1 and actor=$2 and request_key=$3 and signature=$4`,
				run, u.ID, key, web.Hash(fmt.Sprintf("%s/resume/%d/%s", run, in.Expected, reason))).Scan(&controlRevision); err == pgx.ErrNoRows {
				return web.Fail(409, "汇总工具已经选定")
			} else {
				return err
			}
		}
		if state != "blocked" || current != in.Expected {
			return web.Fail(409, "运行状态已变化，请刷新后核对")
		}
		var admitted bool
		if err := tx.QueryRow(ctx, "select agent_admissible($1,$2,$3)", agent, revision, u.ID).Scan(&admitted); err != nil {
			return err
		}
		if !admitted {
			return web.Fail(409, "所选候选已不可用，请恢复权限或复验")
		}
		if _, err := tx.Exec(ctx, `update run_tool_choices set manual_choice=$3, manual_by=$4 where run_id=$1 and ordinal=$2`, run, in.Step, in.Choice, u.ID); err != nil {
			return err
		}
		controlRevision, err = controlTx(ctx, tx, run, u.ID, key, "resume", in.Expected, reason, true)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'checkpoint','tool_choice_confirmed')`, run, in.Step); err != nil {
			return err
		}
		return Note(ctx, tx, project, "assistant", reason+"，继续原运行。", nil)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]int{"controlRevision": controlRevision})
}

// A one-click adoption preserves the consent displayed on the proposal. Open
// the plan editor to submit a separate, explicitly edited configuration.
func checkSuggestedToolChoice(ctx context.Context, tx pgx.Tx, in PlanInput, raw []byte) error {
	var proposed struct {
		ToolChoice     *ToolChoicePin
		Agents         []string
		Revisions      []int
		ExecutionBrief string
	}
	if err := json.Unmarshal(raw, &proposed); err != nil {
		return err
	}
	if proposed.ToolChoice == nil && len(in.ToolChoices) == 0 {
		return nil
	}
	conflict := web.Fail(409, "汇总候选、选择步骤或执行要求已变化，请核对建议或在方案编辑器中重新保存")
	pin := proposed.ToolChoice
	if pin == nil || len(in.ToolChoices) != 1 || in.ToolChoices[0] != *pin || in.Brief != proposed.ExecutionBrief ||
		pin.Selector != 1 || pin.Step != 2 || len(proposed.Agents) < 2 || len(proposed.Revisions) < 2 ||
		len(in.Agents) < 2 || len(in.AgentRevisions) < 2 {
		return conflict
	}
	for i := range 2 {
		if in.Agents[i] != proposed.Agents[i] || in.AgentRevisions[i] != proposed.Revisions[i] {
			return conflict
		}
	}
	var current int
	err := tx.QueryRow(ctx, `select count(*) from (
        select id from agents where not disabled and (id,published_revision) in (($1,$2),($3,$4),($5,$6))
        order by id for share
    ) as listed`, in.Agents[0], in.AgentRevisions[0], in.Agents[1], in.AgentRevisions[1], pin.Alternative, pin.Revision).Scan(&current)
	if err != nil {
		return err
	}
	if current != 3 {
		return conflict
	}
	return nil
}
