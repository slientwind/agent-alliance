package onboarding

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/resources"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type Server struct {
	Catalog resources.Catalog
	Objects knowledge.Objects
}
type preset struct {
	Slot         string `json:"slot"`
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
	Sample       string `json:"sample"`
	Expected     string `json:"expected"`
}

var presets = []preset{
	{"writer", "简报整理助手", "根据获准资料生成中文工作简报。逐项列出事实、负责人和期限。保留来源中的编号，资料不足时明确写出待核实事项。", "把下面的培训安排整理成简报，保留编号。编号 PX-17，周三完成设备登记。", "PX-17"},
	{"checker", "简报核对助手", "比对简报与原资料，检查编号、负责人和期限。明确指出需要修正之处，输出可交付的完整修订稿，保留原始编号。", "核对并修正简报。原资料编号 PX-17，草稿误写为 PX-71。请返回正确编号和核对说明。", "PX-17"},
	{"document", "Word 成稿助手", "将核对后的完整内容生成可下载并可在外部编辑的 Word 文档。", "培训资料 PX-17：周三完成设备登记。", "PX-17"},
}

func (s Server) Routes(m *http.ServeMux, g identity.Server) {
	m.Handle("GET /api/admin/onboarding", g.Protect(true, s.initialSetupStatus))
	m.Handle("POST /api/admin/onboarding", g.Protect(true, s.initialSetup))
	m.Handle("GET /api/teams/{team}/setup", g.Protect(false, s.status))
	m.Handle("POST /api/teams/{team}/setup/connections", g.Protect(true, s.probe))
	m.Handle("PUT /api/teams/{team}/setup/planner", g.Protect(true, s.planner))
	m.Handle("PUT /api/teams/{team}/setup/{slot}", g.Protect(false, s.configure))
	m.Handle("POST /api/teams/{team}/setup/samples", g.Protect(false, s.sample))
}
func scope(ctx context.Context, tx pgx.Tx, team, actor string) error {
	var id string
	return tx.QueryRow(ctx, "select t.id from teams t join team_members tm on tm.team_id=t.id where t.id=$1 and tm.member_id=$2 for update of t for share of tm", team, actor).Scan(&id)
}

// Match the delivered plan to the original sample source, ordered revisions and dependencies.
const statusSQL = `with slots as(
 select s.slot,a.id,a.disabled,a.emergency,a.published_revision as pub,v.revision,v.name,v.resource_id,v.resource_revision,
 agent_verified(a.id,v.revision) as verified,agent_admissible(a.id,a.published_revision,$2) and exists(select from agent_revisions pub where(pub.agent_id,pub.revision)=(a.id,a.published_revision) and case when s.slot='document' then pub.tool->>'operation'='docx' else pub.resource_id is not null and agent_output_operation(pub)='text' and not agent_uses_picture(pub) end) as ready,
 exists(select from validations t where (t.agent_id,t.revision)=(a.id,v.revision) and (t.tokens is not null or exists(select from validation_files f where f.trial_id=t.id))) as connected
 from team_starters s join agents a on a.id=s.agent_id and not a.removed
 join lateral(select * from agent_revisions where agent_id=a.id order by revision desc limit 1)v on true
 where s.team_id=$1
), samples as (
 select x.project_id,p.stage,p.created_at,
 exists(select from project_members pm where pm.project_id=p.id and pm.member_id=$2) as visible,
 (select count(*)=3 and bool_and(coalesce(s.ready and n.agent_revision=s.pub and n.agent_id=x.agents[n.ordinal] and n.agent_revision=x.revisions[n.ordinal],false))
  from nodes n left join slots s on s.id=n.agent_id where n.project_id=p.id and n.plan_version=p.plan_version)
 and (select array_agg(array[e.ordinal,e.depends_on] order by ordinal,depends_on)=array[[2,1],[3,2]] from node_edges e where e.project_id=p.id and e.plan_version=p.plan_version)
 and exists(select from plan_sources initial join plan_sources actual using(project_id,source_id,source_version)
 where initial.project_id=p.id and initial.plan_version=1 and actual.plan_version=p.plan_version) as matches,
 exists(select from plans pl join planning_requests q on q.id=pl.suggestion_id join planning_defaults df on df.team_id=p.team_id
 where pl.project_id=p.id and pl.version=p.plan_version and q.status='ready' and (df.resource_id,df.resource_revision)=(q.resource_id,q.resource_revision)
 and q.result->'agents'=to_jsonb(x.agents) and q.result->'revisions'=to_jsonb(x.revisions)) as planned,
 exists(select from deliveries d join delivery_items di on di.delivery_id=d.id join artifacts a on a.id=di.artifact_id and a.ordinal=3
 join artifact_files f on(f.artifact_id,f.version)=(di.artifact_id,di.version) where d.project_id=p.id and lower(f.filename) like '%.docx') as delivered
 from starter_samples x join projects p on p.id=x.project_id where x.team_id=$1)
 select jsonb_build_object('slots',coalesce((select jsonb_object_agg(slot,jsonb_build_object('agent',id,'name',name,'revision',revision,'published',coalesce(pub,0),'verified',verified,'ready',ready,'connected',connected,'resource',resource_id,'resourceRevision',resource_revision,'disabled',disabled or emergency)) from slots),'{}'),
 'connections',array(select jsonb_build_object('resource',r.resource_id,'revision',r.resource_revision,'status',r.current_status,'detail',r.detail,'at',r.created_at) from (select p.*,row_number() over(partition by resource_id,resource_revision order by created_at desc,id) pos from model_probe_receipts p where team_id=$1 and candidate is null and exists(select from model_revisions v where(v.resource_id,v.revision)=(p.resource_id,p.resource_revision) and v.key_generation=coalesce(p.key_generation,1))) r where r.pos<=5 order by r.created_at desc,r.id),
 'planner',(select jsonb_build_object('resource',d.resource_id,'revision',d.resource_revision,'version',d.revision,'name',m.name,'enabled',m.enabled and not m.emergency and not v.key_blocked)
 from planning_defaults d join model_resources m on m.id=d.resource_id join model_revisions v on(v.resource_id,v.revision)=(d.resource_id,d.resource_revision) where d.team_id=$1),
 'planned',exists(select from samples s join planning_defaults d on d.team_id=$1 join model_resources m on m.id=d.resource_id join model_revisions v on(v.resource_id,v.revision)=(d.resource_id,d.resource_revision) where matches and planned and delivered and m.enabled and not m.emergency and not v.key_blocked),
 'passed',exists(select from samples where matches and delivered),
 'projects',array(select jsonb_build_object('id',project_id,'stage',stage,'createdAt',created_at,'matches',coalesce(matches,false)) from samples where visible order by created_at desc, project_id))
 from team_members where team_id=$1 and member_id=$2`

func (s Server) status(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var result json.RawMessage
	if err := s.Catalog.DB.QueryRow(r.Context(), statusSQL, r.PathValue("team"), u.ID).Scan(&result); err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"templates": presets, "state": result})
}
func (s Server) planner(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Resource           string
		Revision, Expected int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx, team := r.Context(), r.PathValue("team")
	err := database.WithTx(ctx, s.Catalog.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := scope(ctx, tx, team, u.ID); err != nil {
			return err
		}
		var version int
		if err := tx.QueryRow(ctx, "select coalesce((select revision from planning_defaults where team_id=$1),0)", team).Scan(&version); err != nil {
			return err
		}
		if version != in.Expected {
			return web.Fail(409, "默认模型已变化，请重新核对")
		}
		tag, err := tx.Exec(ctx, `insert into planning_defaults(team_id,resource_id,resource_revision,revision,changed_by) select $1,m.id,v.revision,$4,$5 from model_resources m join model_revisions v on v.resource_id=m.id where m.team_id=$1 and m.id=$2 and v.revision=$3 and v.operation='text' and m.enabled and not m.emergency and not v.key_blocked
 on conflict(team_id) do update set resource_id=excluded.resource_id,resource_revision=excluded.resource_revision,revision=excluded.revision,changed_by=excluded.changed_by`, team, in.Resource, in.Revision, version+1, u.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(422, "请选择本团队已启用的模型修订")
		}
		return database.Audit(ctx, tx, u.ID, "setup.planner", team, map[string]any{"version": version + 1, "resource": in.Resource, "revision": in.Revision})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) configure(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Resource string
		Revision int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	i := slices.IndexFunc(presets, func(p preset) bool { return p.Slot == r.PathValue("slot") })
	if i < 0 {
		return web.Fail(404, "模板不存在")
	}
	t := presets[i]
	team := r.PathValue("team")
	ctx := r.Context()
	var id string
	err := database.WithTx(ctx, s.Catalog.DB, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, team, u.ID); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `select a.id,coalesce(v.resource_id::text,'')=$3 and coalesce(v.resource_revision,0)=$4 from team_starters s join agents a on a.id=s.agent_id and not a.removed
 join agent_revisions v on v.agent_id=a.id and v.revision=1 where s.team_id=$1 and s.slot=$2`, team, t.Slot, in.Resource, in.Revision).Scan(&id, &same)
		if err == nil {
			if !same {
				return web.Fail(409, "模板已配置，修改现有助手需另存修订")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if t.Slot == "document" {
			if in.Resource != "" || in.Revision != 0 {
				return web.Fail(422, "Word 连接器无需模型")
			}
			id, _, err = s.Catalog.SaveToolDraftTx(ctx, tx, u.ID, resources.ToolDraft{Name: t.Name, Description: t.Instructions, Team: team, Tool: resources.Tool{Operation: "docx", Config: map[string]string{"title": "工作简报"}}})
		} else {
			var allowed bool
			if err := tx.QueryRow(ctx, "select exists(select from model_resources m join model_revisions v on v.resource_id=m.id where m.id=nullif($1,'')::uuid and team_id=$2 and enabled and not emergency and v.revision=$3 and v.operation='text')", in.Resource, team, in.Revision).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return web.Fail(422, "请选择本团队已启用的模型资源")
			}
			id, _, err = s.Catalog.SaveDraftTx(ctx, tx, u.ID, resources.DraftConfig{Name: t.Name, Instructions: t.Instructions, Resource: in.Resource, ResourceRevision: in.Revision})
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `insert into agent_checks(agent_id,revision,ordinal,name,sample,expected,match_kind) values($1,1,1,'保留资料编号',$2,$3,'contains')`, id, t.Sample, t.Expected); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update agent_revisions set checks_version=1 where agent_id=$1 and revision=1", id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into team_starters(team_id,slot,agent_id,template_version) values($1,$2,$3,1)
 on conflict(team_id,slot) do update set agent_id=excluded.agent_id,template_version=excluded.template_version`, team, t.Slot, id)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"agent": id})
}

const sourceText = "资料编号 PX-17。运营组负责设备登记，截止时间为本周三。培训组负责整理使用说明，截止时间为本周五。"

func (s Server) sample(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Agents    []string
		Revisions []int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if len(in.Agents) != len(presets) || len(in.Revisions) != len(presets) {
		return web.Fail(422, "请核对三个已发布版本")
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 180 {
		return web.Fail(422, "缺少有效的样例请求编号")
	}
	team := r.PathValue("team")
	ctx := r.Context()
	var project string
	err := database.WithTx(ctx, s.Catalog.DB, func(tx pgx.Tx) error {
		if err := scope(ctx, tx, team, u.ID); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, "select project_id,team_id=$3 and revisions=$4 and agents=$5 from starter_samples where actor=$1 and request_key=$2", u.ID, key, team, in.Revisions, in.Agents).Scan(&project, &same)
		if err == nil {
			if !same {
				return web.Fail(409, "请求编号已用于其他配置")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		plan := projects.PlanInput{Expected: 1, Brief: "根据培训安排整理简报，核对编号、负责人和期限，生成可编辑的 Word 成稿。", AgentRevisions: in.Revisions}
		for i, p := range presets {
			var id string
			err := tx.QueryRow(ctx, `select a.id from team_starters s join agents a on a.id=s.agent_id where s.team_id=$1 and s.slot=$2 and a.published_revision=$3 and a.id=$5 and agent_admissible(a.id,$3,$4) and exists(select from agent_revisions v where(v.agent_id,v.revision)=(a.id,$3) and (case when s.slot='document' then v.tool->>'operation'='docx' else v.resource_id is not null and agent_output_operation(v)='text' and not agent_uses_picture(v) end)) for share of a`, team, p.Slot, in.Revisions[i], u.ID, in.Agents[i]).Scan(&id)
			if err == pgx.ErrNoRows {
				return web.Fail(409, "助手版本、验证或授权已变化，请刷新首次配置")
			}
			if err != nil {
				return err
			}
			plan.Agents = append(plan.Agents, id)
		}
		if err = tx.QueryRow(ctx, "insert into projects(owner,team_id,final_reviewer,title) values($1,$2,$1,'团队配置验证 · 培训简报') returning id", u.ID, team).Scan(&project); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into project_memberships(project_id,member_id,can_review) values($1,$2,true)", project, u.ID); err != nil {
			return err
		}
		if err = projects.Note(ctx, tx, project, "user", plan.Brief, u.ID); err != nil {
			return err
		}
		src, err := s.Objects.NewTextSource(ctx, tx, project, u.ID, "培训安排.txt", sourceText)
		if err != nil {
			return err
		}
		plan.Sources = []projects.SourcePin{{ID: src, Version: 1}}
		if err = projects.SavePlan(ctx, tx, project, u.ID, plan); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into starter_samples(project_id,team_id,actor,request_key,revisions,agents) values($1,$2,$3,$4,$5,$6)", project, team, u.ID, key, in.Revisions, in.Agents)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"id": project})
}
