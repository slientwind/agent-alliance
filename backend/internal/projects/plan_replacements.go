package projects

import (
	"context"
	"fmt"

	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type StepReplacement struct {
	From int `json:"from"`
	Step int `json:"step"`
}

// Match business steps across the fixed plan and its new version. Lookup
// answers are internal nodes of the corresponding search step.
func checkStepReplacements(ctx context.Context, tx pgx.Tx, project string, from, to int, changes []StepReplacement) error {
	if len(changes) == 0 {
		return nil
	}
	if len(changes) > 12 {
		return web.Fail(422, "方案最多可替换 12 个步骤")
	}
	targets, origins := map[int]bool{}, map[int]bool{}
	for _, change := range changes {
		if change.From < 1 || change.Step < 1 || origins[change.From] || targets[change.Step] {
			return web.Fail(422, "请指定有效且互不重复的替换步骤")
		}
		origins[change.From], targets[change.Step] = true, true
		var compatible bool
		err := tx.QueryRow(ctx, `with business_steps as (
 select n.plan_version,n.agent_id,n.agent_revision,
        row_number() over(partition by n.plan_version order by n.ordinal) as step
 from nodes n where n.project_id=$1 and n.plan_version in ($2,$3)
   and n.lookup_phase not in ('answer','answer_calc','synthesize')
 )
 select exists(
 select from business_steps original join business_steps replacement on replacement.plan_version=$3 and replacement.step=$5
 join agent_revisions prev on (prev.agent_id,prev.revision)=(original.agent_id,original.agent_revision)
 join agent_revisions next on (next.agent_id,next.revision)=(replacement.agent_id,replacement.agent_revision)
 join agents published on published.id=next.agent_id
 where original.plan_version=$2 and original.step=$4
   and (published.published_revision=next.revision or exists(
     select from effective_project_agent_grants grant_for_plan
     where (grant_for_plan.project_id,grant_for_plan.agent_id,grant_for_plan.revision)=($1,next.agent_id,next.revision)))
   and agent_output_operation(prev)=agent_output_operation(next)
   and agent_uses_picture(prev)=agent_uses_picture(next)
   and agent_edits_picture(prev)=agent_edits_picture(next)
   and (agent_output_operation(prev)<>'pptx' or
     coalesce(prev.tool->>'contract','')=coalesce(next.tool->>'contract',''))
 )`, project, from, to, change.From, change.Step).Scan(&compatible)
		if err != nil {
			return err
		}
		if !compatible {
			return web.Fail(422, fmt.Sprintf("步骤 %d 的替换 Agent 已不可用或与原步骤的成果与输入约定不兼容", change.Step))
		}
	}
	return nil
}
