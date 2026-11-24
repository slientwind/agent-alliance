package projects

import (
	"alliance/internal/builder"
	"alliance/internal/web"
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type templateBuild struct {
	agent    string
	revision int
	digest   string
}

// The manifest commits to the reusable graph and its explicit dependencies.
// Where a publication receipt exists, use its build even if the resource has
// drifted since the originating run. That drift then fails publication checks.
func compileWorkflow(ctx context.Context, tx pgx.Tx, t templateReceipt) error {
	rows, err := tx.Query(ctx, `with nodes as (
   select n.doc from workflow_template_versions v cross join lateral jsonb_array_elements(v.nodes) n(doc)
   where (v.template_id,v.version)=($1,$2)
 ), dependencies as (
   select doc->>'agent' as agent,(doc->>'revision')::int as revision from nodes
   union
   select doc->'toolChoice'->>'agent',(doc->'toolChoice'->>'revision')::int from nodes
   where doc->'toolChoice' <> 'null'::jsonb
 ) select d.agent,d.revision,coalesce((select p.build_digest from agent_publications p
   where (p.agent_id,p.revision)=(d.agent::uuid,d.revision) order by generation desc limit 1),'')
   from dependencies d order by d.agent,d.revision`, t.ID, t.Version)
	if err != nil {
		return err
	}
	builds, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (templateBuild, error) {
		var b templateBuild
		err := row.Scan(&b.agent, &b.revision, &b.digest)
		return b, err
	})
	if err != nil {
		return err
	}
	for _, b := range builds {
		if b.digest == "" {
			build, err := builder.Capture(ctx, tx, b.agent, b.revision)
			if err != nil {
				return err
			}
			b.digest = build.Digest
		}
		if _, err := tx.Exec(ctx, `insert into workflow_template_builds(template_id,version,agent_id,agent_revision,build_digest)
             values($1,$2,$3,$4,$5)`, t.ID, t.Version, b.agent, b.revision, b.digest); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `update workflow_template_versions v set manifest_digest=agent_build_digest(jsonb_build_object(
     'format',1,'name',v.name,'instructions',v.instructions,'inputs',v.inputs,'outcomes',v.outcomes,
     'requiresSources',v.requires_sources,'deliveryContract',v.delivery_contract,'nodes',v.nodes,'builds',array(
       select jsonb_build_array(b.agent_id,b.agent_revision,b.build_digest) from workflow_template_builds b
       where (b.template_id,b.version)=(v.template_id,v.version) order by b.agent_id,b.agent_revision)))
    where (v.template_id,v.version)=($1,$2)`, t.ID, t.Version)
	return err
}

func checkWorkflowPublication(ctx context.Context, tx pgx.Tx, template string, version int, actor string) error {
	var ordinal int
	err := tx.QueryRow(ctx, `select n.ordinal from workflow_template_versions v
        cross join lateral jsonb_array_elements(v.nodes) with ordinality n(doc,ordinal)
        cross join lateral (values(n.doc),(n.doc->'toolChoice')) ref(doc)
        join agents a on a.id=(ref.doc->>'agent')::uuid
        where (v.template_id,v.version)=($1,$2) and v.manifest_digest is not null and
        (a.published_revision is distinct from (ref.doc->>'revision')::int
         or not agent_admissible(a.id,(ref.doc->>'revision')::int,$3)
         or not workflow_build_usable(v.template_id,v.version,a.id,(ref.doc->>'revision')::int))
        order by n.ordinal limit 1`, template, version, actor).Scan(&ordinal)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return web.Fail(409, fmt.Sprintf("模板第 %d 步的 Agent 或备选工具修订已变化或不可用，请核对后更新模板", ordinal))
}

func bindWorkflowBuilds(ctx context.Context, tx pgx.Tx, project string, version, templateVersion int) error {
	if _, err := tx.Exec(ctx, `insert into plan_workflow_templates(project_id,plan_version,template_id,template_version)
      select b.project_id,$2,v.template_id,v.version from project_workflow_template_binding b
      join workflow_template_versions v on (v.template_id,v.version)=(b.template_id,coalesce(nullif($3,0),b.version))
      where b.project_id=$1 and v.manifest_digest is not null`, project, version, templateVersion); err != nil {
		return err
	}
	return checkWorkflowBuilds(ctx, tx, project, version)
}

func checkWorkflowBuilds(ctx context.Context, tx pgx.Tx, project string, version int) error {
	var ordinal int
	err := tx.QueryRow(ctx, `select ordinal from (
       select n.ordinal,n.agent_id,n.agent_revision from nodes n where (n.project_id,n.plan_version)=($1,$2)
       union all
       select c.ordinal,c.alternative,c.alternative_revision from plan_tool_choices c where (c.project_id,c.plan_version)=($1,$2)
      ) input where not plan_workflow_build_usable($1,$2,input.agent_id,input.agent_revision)
      order by ordinal limit 1`, project, version).Scan(&ordinal)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return web.Fail(409, fmt.Sprintf("第 %d 步的构建清单与模板记录不一致，请选择当前可用的修订", ordinal))
}
