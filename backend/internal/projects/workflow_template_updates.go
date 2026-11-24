package projects

import (
	"encoding/json"
	"net/http"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) previewWorkflowTemplateUpdate(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object(
 'projectRevision',p.revision,'planVersion',p.plan_version,'fromVersion',b.version,'published',v.version=t.published_version,
 'canAdopt',p.owner=$1 and p.stage in ('brief','plan') and not exists(select from runs where project_id=p.id),
 'changedSteps',array(select pos+1 from generate_series(0,greatest(jsonb_array_length(prior.nodes),jsonb_array_length(v.nodes))-1) pos
   where prior.nodes->pos is distinct from v.nodes->pos or exists(
     select from (values(v.nodes->pos),(v.nodes->pos->'toolChoice')) ref(doc)
     join workflow_template_builds fresh on (fresh.template_id,fresh.version,fresh.agent_id,fresh.agent_revision)=
       (v.template_id,v.version,(ref.doc->>'agent')::uuid,(ref.doc->>'revision')::int)
     left join workflow_template_builds old on (old.template_id,old.version,old.agent_id,old.agent_revision)=
       (prior.template_id,prior.version,fresh.agent_id,fresh.agent_revision)
     where fresh.build_digest is distinct from old.build_digest
   ) order by pos),
 'target',jsonb_build_object('id',v.template_id,'version',v.version,'name',v.name,'instructions',v.instructions,
   'inputs',v.inputs,'outcomes',v.outcomes,'requiresSources',v.requires_sources,'manifestDigest',v.manifest_digest,'nodes',`+workflowTemplateNodes+`))
 from projects p join project_members pm on pm.project_id=p.id and pm.member_id=$1
 join project_workflow_template_binding b on b.project_id=p.id
 join workflow_templates t on t.id=b.template_id
 join workflow_template_versions prior on (prior.template_id,prior.version)=(b.template_id,b.version)
 join workflow_template_versions v on v.template_id=b.template_id and v.version>b.version
   and v.version=offered_workflow_template_version(b.template_id,$1)
 where p.id=$2`, u.ID, r.PathValue("id")).Scan(&result)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, result)
}

type templateUpdateReceipt struct {
	Version         int `json:"version"`
	PlanVersion     int `json:"planVersion"`
	ProjectRevision int `json:"projectRevision"`
}

func (s Server) adoptWorkflowTemplateUpdate(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		FromVersion, ToVersion int
		Plan                   PlanInput
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 160 || in.FromVersion < 1 || in.ToVersion <= in.FromVersion || in.Plan.Expected < 1 {
		return web.Fail(422, "请核对前后模板版本及本次待确认方案")
	}
	ctx, project := r.Context(), r.PathValue("id")
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	sig := web.Hash(project + "/" + string(body))
	var result templateUpdateReceipt
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, project, u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "由项目发起人确认采用新版")
		}
		var template string
		var boundVersion int
		if err := tx.QueryRow(ctx, `select template_id,version from project_workflow_template_binding where project_id=$1`, p.ID).Scan(&template, &boundVersion); err != nil {
			return err
		}
		t, err := lockTemplate(ctx, tx, template, u.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", "template-adopt/"+u.ID+"/"+key); err != nil {
			return err
		}
		var matches bool
		err = tx.QueryRow(ctx, `select to_version,plan_version,project_revision,request_hash=$3
 from workflow_template_adoptions where actor=$1 and request_key=$2`, u.ID, key, sig).Scan(&result.Version, &result.PlanVersion, &result.ProjectRevision, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "请求标识已用于不同的模板变更")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		offered := t.published
		if t.owner == u.ID {
			offered = t.current
		}
		if t.archived || offered != in.ToVersion || boundVersion != in.FromVersion || p.Revision != in.Plan.Expected {
			return web.Fail(409, "项目或模板版本已变化，请重新比较后保存")
		}
		var hasRun bool
		if err := tx.QueryRow(ctx, `select exists(select from runs where project_id=$1)`, p.ID).Scan(&hasRun); err != nil {
			return err
		}
		if hasRun || (p.Stage != "brief" && p.Stage != "plan") {
			return web.Fail(409, "此任务已经受理执行，请从新版模板开始新对话")
		}
		var requiresSources bool
		if err := tx.QueryRow(ctx, `select requires_sources from workflow_template_versions where(template_id,version)=($1,$2)`, template, in.ToVersion).Scan(&requiresSources); err != nil {
			return err
		}
		if requiresSources && len(in.Plan.Sources) == 0 {
			return web.Fail(422, "新版模板要求选择本项目的可读资料")
		}
		if err := savePlan(ctx, tx, p.ID, u.ID, in.Plan, in.ToVersion); err != nil {
			return err
		}
		result = templateUpdateReceipt{Version: in.ToVersion, PlanVersion: p.PlanVersion + 1, ProjectRevision: p.Revision + 1}
		if _, err := tx.Exec(ctx, `insert into workflow_template_adoptions(project_id,template_id,from_version,to_version,previous_plan_version,plan_version,project_revision,actor,request_key,request_hash)
 values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.ID, template, boundVersion, result.Version, p.PlanVersion, result.PlanVersion, result.ProjectRevision, u.ID, key, sig); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "workflow_template.adopt", p.ID, map[string]any{
			"template": template, "fromVersion": boundVersion, "toVersion": result.Version, "oldPlan": p.PlanVersion, "plan": result.PlanVersion,
		})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}
