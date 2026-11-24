package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

// A snapshot contains workflow structure and authored reusable instructions.
// Candidate identifiers and names are projected only through current access.
const workflowTemplateNodes = `coalesce((select jsonb_agg(jsonb_build_object(
 'agent',coalesce(a.id::text,''),'name',coalesce(a.name,''),'revision',(n.doc->>'revision')::int,
 'ready',coalesce(a.ready,false),'buildMatches',a.build_matches,'operation',n.doc->>'operation','vision',coalesce((n.doc->>'vision')::boolean,false),'editsImage',coalesce((n.doc->>'editsImage')::boolean,false),'lookupPhase',n.doc->>'lookupPhase','dependsOn',n.doc->'dependsOn',
 'toolChoice',case when n.doc->'toolChoice' is distinct from 'null'::jsonb then jsonb_build_object(
 'selector',(n.doc->'toolChoice'->>'selector')::int,'agent',coalesce(b.id::text,''),'name',coalesce(b.name,''),
 'revision',(n.doc->'toolChoice'->>'revision')::int,'ready',coalesce(b.ready,false),'buildMatches',b.build_matches) end
 ) order by n.ordinal) from jsonb_array_elements(v.nodes) with ordinality n(doc,ordinal)
 left join lateral(select a.id,ar.name,build.matches as build_matches,
   coalesce(a.published_revision=ar.revision and agent_admissible(a.id,ar.revision,$1) and build.matches,false) ready
 from agents a join agent_revisions ar on ar.agent_id=a.id and ar.revision=(n.doc->>'revision')::int
 cross join lateral(select workflow_build_usable(v.template_id,v.version,a.id,ar.revision) matches) build
 where a.id=(n.doc->>'agent')::uuid and not a.removed and project_agent_access(null,a.id,ar.revision,$1)) a on true
 left join lateral(select a.id,ar.name,build.matches as build_matches,
   coalesce(a.published_revision=ar.revision and agent_admissible(a.id,ar.revision,$1) and build.matches,false) ready
 from agents a join agent_revisions ar on ar.agent_id=a.id and ar.revision=(n.doc->'toolChoice'->>'revision')::int
 cross join lateral(select workflow_build_usable(v.template_id,v.version,a.id,ar.revision) matches) build
 where a.id=(n.doc->'toolChoice'->>'agent')::uuid and not a.removed and project_agent_access(null,a.id,ar.revision,$1)) b on true),'[]')`

const projectWorkflowTemplateSQL = `(select jsonb_build_object('id',v.template_id,'version',v.version,'name',v.name,
 'instructions',v.instructions,'inputs',v.inputs,'outcomes',v.outcomes,'requiresSources',v.requires_sources,'deliveryContract',v.delivery_contract,'manifestDigest',v.manifest_digest,'nodes',` + workflowTemplateNodes + `,
 'updateVersion',nullif(greatest(offered_workflow_template_version(u.template_id,$1),v.version),v.version))
 from project_workflow_template_binding u join workflow_template_versions v on(v.template_id,v.version)=(u.template_id,u.version) where u.project_id=p.id)`

const templateCatalogScope = `from workflow_templates t
 join teams team on team.id=t.team_id join team_members tm on(tm.team_id,tm.member_id)=(t.team_id,$1)
 join members viewer on viewer.id=tm.member_id and viewer.enabled
 join workflow_template_versions v on v.template_id=t.id
 where t.archived_at is null and (v.version=t.published_version
 or (v.version=t.current_version and (t.owner=$1 or (viewer.role='admin' and v.version=t.requested_version))))`

const templateSummary = `'id',t.id,'team',t.team_id,'teamName',team.name,'name',v.name,'version',v.version,
 'currentVersion',t.current_version,'publishedVersion',t.published_version,'requestedVersion',t.requested_version,
 'owner',t.owner=$1,'canPublish',viewer.role='admin','canUse',(t.owner=$1 or v.version=t.published_version),
 'decision',case when t.owner=$1 and v.version=t.current_version then t.decision_reason else '' end`

func (s Server) workflowTemplateList(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var result json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select coalesce(jsonb_agg(item order by updated_at desc,id,version desc),'[]') from (
 select jsonb_build_object(`+templateSummary+`) item,t.updated_at,t.id,v.version `+templateCatalogScope+`
 order by t.updated_at desc,t.id,v.version desc limit 100) listed`, u.ID).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) workflowTemplateDetail(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ver, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil || ver <= 0 {
		return web.Fail(422, "请选择模板版本")
	}
	var result json.RawMessage
	err = s.DB.QueryRow(r.Context(), `select jsonb_build_object(`+templateSummary+`,
 'instructions',v.instructions,'inputs',v.inputs,'outcomes',v.outcomes,'requiresSources',v.requires_sources,'deliveryContract',v.delivery_contract,'manifestDigest',v.manifest_digest,'nodes',`+workflowTemplateNodes+`) `+
		templateCatalogScope+` and t.id=$2 and v.version=$3`, u.ID, r.PathValue("template"), ver).Scan(&result)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

type templateVersionInput struct {
	Name, Instructions, Inputs, Outcomes, Replace string
	Expected, ExpectedVersion                     int
	DeliveryContract                              *DeliveryContract `json:",omitempty"`
}

type templateReceipt struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

func validTemplateText(value string, limit int) bool {
	return !web.Blank(value) && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, 0)
}

const snapshotWorkflow = `select coalesce(jsonb_agg(jsonb_build_object('agent',n.agent_id,'revision',n.agent_revision,
 'operation',agent_output_operation(a),'vision',agent_uses_picture(a) and not agent_edits_picture(a),'editsImage',agent_edits_picture(a),'lookupPhase',n.lookup_phase,
 'dependsOn',coalesce((select jsonb_agg(e.depends_on order by e.depends_on) from node_edges e
 where(e.project_id,e.plan_version,e.ordinal)=(n.project_id,n.plan_version,n.ordinal)),'[]'),
 'toolChoice',(select jsonb_build_object('selector',c.selector,'agent',c.alternative,'revision',c.alternative_revision)
 from plan_tool_choices c where(c.project_id,c.plan_version,c.ordinal)=(n.project_id,n.plan_version,n.ordinal))) order by n.ordinal),'[]')
 from nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 where (n.project_id,n.plan_version)=($1,$2)`

func (s Server) saveWorkflowTemplate(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in templateVersionInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	for _, value := range []*string{&in.Name, &in.Instructions, &in.Inputs, &in.Outcomes} {
		*value = strings.TrimSpace(*value)
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 160 || !validTemplateText(in.Name, 80) || !validTemplateText(in.Instructions, 4000) ||
		!validTemplateText(in.Inputs, 2000) || !validTemplateText(in.Outcomes, 2000) {
		return web.Fail(422, "请填写模板名称、可复用要求、输入和交付约定，并提供请求标识")
	}
	ctx, project := r.Context(), r.PathValue("id")
	encoded, _ := json.Marshal(in)
	sig := web.Hash(project + "/" + string(encoded))
	var result templateReceipt
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, project, u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "由原项目发起人保存可复用的协作模板")
		}
		var team string
		if err := tx.QueryRow(ctx, `select t.team_id from team_members t join projects p on p.team_id=t.team_id
 join members m on m.id=t.member_id and m.enabled where p.id=$1 and t.member_id=$2 for share of t,m`, project, u.ID).Scan(&team); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", "template-save/"+u.ID+"/"+key); err != nil {
			return err
		}
		var matches bool
		err = tx.QueryRow(ctx, `select template_id,version,request_hash=$3 from workflow_template_versions where author=$1 and request_key=$2`, u.ID, key, sig).Scan(&result.ID, &result.Version, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "请求标识已用于不同模板内容")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if p.Stage != "delivered" || p.Revision != in.Expected || p.PlanVersion == 0 {
			return web.Fail(409, "请核对当前已交付项目后保存模板")
		}
		var nodes json.RawMessage
		if err := tx.QueryRow(ctx, snapshotWorkflow, project, p.PlanVersion).Scan(&nodes); err != nil {
			return err
		}
		if string(nodes) == "[]" {
			return web.Fail(409, "此交付没有可复用的协作步骤")
		}
		contract, err := authoredWorkflowDelivery(ctx, tx, p, nodes, in.DeliveryContract)
		if err != nil {
			return err
		}
		if in.Replace == "" {
			err = tx.QueryRow(ctx, `insert into workflow_templates(team_id,owner) values($1,$2) returning id,current_version`, team, u.ID).Scan(&result.ID, &result.Version)
		} else {
			err = tx.QueryRow(ctx, `update workflow_templates set current_version=current_version+1,requested_version=0,decision_reason='',updated_at=now()
 where id=$1 and team_id=$2 and owner=$3 and current_version=$4 and archived_at is null returning id,current_version`,
				in.Replace, team, u.ID, in.ExpectedVersion).Scan(&result.ID, &result.Version)
			if err == pgx.ErrNoRows {
				return web.Fail(409, "模板版本或维护权限已变化，请重新打开模板")
			}
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into workflow_template_versions(template_id,version,name,instructions,inputs,outcomes,nodes,origin_project,origin_plan,author,request_key,request_hash,requires_sources,delivery_contract)
 values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,exists(select from plan_sources where(project_id,plan_version)=($8,$9)),$13)`, result.ID, result.Version, in.Name, in.Instructions, in.Inputs, in.Outcomes, nodes, project, p.PlanVersion, u.ID, key, sig, contract); err != nil {
			return err
		}
		if err := compileWorkflow(ctx, tx, result); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "workflow_template.save", result.ID, map[string]any{"version": result.Version, "project": project, "plan": p.PlanVersion})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, result)
}

type templateAccess struct {
	team, owner, role             string
	current, published, requested int
	archived                      bool
}

func lockTemplate(ctx context.Context, tx pgx.Tx, id, actor string) (templateAccess, error) {
	var t templateAccess
	err := tx.QueryRow(ctx, `select t.team_id,t.owner,m.role,t.current_version,t.published_version,t.requested_version,t.archived_at is not null
 from workflow_templates t join team_members tm on(tm.team_id,tm.member_id)=(t.team_id,$2)
 join members m on m.id=tm.member_id and m.enabled where t.id=$1 for update of t for share of tm,m`, id, actor).
		Scan(&t.team, &t.owner, &t.role, &t.current, &t.published, &t.requested, &t.archived)
	return t, err
}

func (s Server) workflowTemplatePublication(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason string
		Version        int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	ctx, id := r.Context(), r.PathValue("template")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := lockTemplate(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		if t.owner != u.ID && t.published == 0 && (t.role != "admin" || t.requested == 0) {
			return pgx.ErrNoRows
		}
		archivePublished := in.Action == "archive" && t.published > 0 && t.published == in.Version
		if t.archived || (t.current != in.Version && !archivePublished) {
			return web.Fail(409, "模板版本或状态已更新")
		}
		var update string
		switch in.Action {
		case "request":
			if t.owner != u.ID {
				return web.Fail(403, "需要模板维护人提交申请")
			}
			if t.published == in.Version || t.requested == in.Version {
				return nil
			}
			if err := checkWorkflowPublication(ctx, tx, id, in.Version, u.ID); err != nil {
				return err
			}
			update = "requested_version=current_version,decision_reason=''"
		case "publish", "return":
			if t.role != "admin" {
				return web.Fail(403, "需要团队内管理员处理模板发布")
			}
			if t.published == in.Version && in.Action == "publish" {
				return nil
			}
			if t.requested != in.Version {
				return web.Fail(409, "该修订没有待处理的发布申请")
			}
			if in.Action == "publish" {
				if err := checkWorkflowPublication(ctx, tx, id, in.Version, u.ID); err != nil {
					return err
				}
				update = "published_version=current_version,requested_version=0,decision_reason=''"
			} else {
				if !validTemplateText(in.Reason, 1000) {
					return web.Fail(422, "请填写退回原因")
				}
				update = "requested_version=0,decision_reason=$2"
			}
		case "archive":
			if t.role != "admin" && (t.owner != u.ID || t.published != 0) {
				return web.Fail(403, "共享模板由团队内管理员归档")
			}
			update = "archived_at=now(),requested_version=0"
		default:
			return web.Fail(422, "未知模板操作")
		}
		args := []any{id}
		if in.Action == "return" {
			args = append(args, in.Reason)
		}
		if _, err := tx.Exec(ctx, `update workflow_templates set updated_at=now(),`+update+` where id=$1`, args...); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "workflow_template."+in.Action, id, map[string]any{"version": in.Version, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

func (s Server) useWorkflowTemplate(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Goal    string
		Version int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Goal = strings.TrimSpace(in.Goal)
	key := r.Header.Get("Idempotency-Key")
	if !validTemplateText(in.Goal, 16000) || in.Version < 1 || key == "" || len(key) > 160 {
		return web.Fail(422, "请选择模板版本并描述本次任务目标")
	}
	ctx, id := r.Context(), r.PathValue("template")
	sig := web.Hash(id + "/" + strconv.Itoa(in.Version) + "/" + in.Goal)
	var project string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		t, err := lockTemplate(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,0))", "template-use/"+u.ID+"/"+key); err != nil {
			return err
		}
		var matches bool
		err = tx.QueryRow(ctx, `select project_id,request_hash=$3 from workflow_template_uses where actor=$1 and request_key=$2`, u.ID, key, sig).Scan(&project, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "请求标识已用于其他任务")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if t.owner != u.ID && t.published == 0 {
			return pgx.ErrNoRows
		}
		if t.archived || (t.published != in.Version && (t.owner != u.ID || t.current != in.Version)) {
			return web.Fail(409, "该模板修订当前不可复用，请重新查看目录")
		}
		project, err = createProjectDraft(ctx, tx, u.ID, t.team, in.Goal)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into workflow_template_uses values($1,$2,$3,$4,$5,$6)`, project, id, in.Version, u.ID, key, sig); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "workflow_template.use", project, map[string]any{"template": id, "version": in.Version})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": project})
}
