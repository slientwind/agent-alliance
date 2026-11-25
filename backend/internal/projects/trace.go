package projects

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type traceRights struct {
	Structure bool `json:"structure"`
	Content   bool `json:"content"`
	Export    bool `json:"export"`
}

func (s Server) traceGrant(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Member, Scope, Reason string
		Expected, Hours       int
		Allowed               *bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if (in.Scope != "structure" && in.Scope != "content" && in.Scope != "export") || in.Allowed == nil || web.Blank(in.Reason) || (*in.Allowed && (in.Hours < 1 || in.Hours > 720)) {
		return web.Fail(422, "请指定授权范围、原因和1至720小时的有效期")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "需要项目发起人授权")
		}
		if p.Revision != in.Expected {
			return web.Fail(409, "项目权限已变化，请刷新")
		}
		var run string
		err = tx.QueryRow(ctx, `select r.id from runs r join project_members pm on pm.project_id=r.project_id join members m on m.id=pm.member_id where r.id=$1 and r.project_id=$2 and pm.member_id=$3 and (not $4 or (m.enabled and (r.trace_expires_at is null or r.trace_expires_at>clock_timestamp())))`, r.PathValue("run"), p.ID, in.Member, *in.Allowed).Scan(&run)
		if err != nil {
			return err
		}
		if *in.Allowed {
			_, err = tx.Exec(ctx, `insert into trace_grants values($1,$2,$3,clock_timestamp()+$4*interval '1 hour') on conflict(run_id,member_id,scope) do update set expires_at=excluded.expires_at`, run, in.Member, in.Scope, in.Hours)
		} else {
			_, err = tx.Exec(ctx, "delete from trace_grants where run_id=$1 and member_id=$2 and scope=$3", run, in.Member, in.Scope)
		}
		if err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "trace.grant", run, map[string]any{"member": in.Member, "scope": in.Scope, "hours": in.Hours, "allowed": *in.Allowed, "reason": strings.TrimSpace(in.Reason)})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}

const traceInputsAllowed = `not exists(select from change_batches cb join feedback f on f.id=cb.feedback_id where cb.run_id=r.id and not feedback_notes_readable(f,$3))
 and not exists(select from plan_sources ps join sources s on s.id=ps.source_id where(ps.project_id,ps.plan_version)=(r.project_id,r.plan_version) and (s.revoked_at is not null or s.project_id<>r.project_id))
 and not exists(select from plan_bound_preferences pp join preferences pref on pref.id=pp.preference_id where(pp.project_id,pp.plan_version)=(r.project_id,r.plan_version) and (pref.member_id<>$3 or not pref.enabled or pref.deleted_at is not null or pref.revision<>pp.revision or pref.agent_scope is distinct from pp.agent_scope or pref.expires_at<=clock_timestamp()))
 and not exists(select from plan_rules pr join rule_versions v on(v.rule_id,v.revision)=(pr.rule_id,pr.rule_revision)
 where(pr.project_id,pr.plan_version)=(r.project_id,r.plan_version) and cardinality(v.agent_scope)=0 and not rule_usable(pr.rule_id,pr.rule_revision,$3))`

const traceAccessSQL = `select (p.owner=$3 or 'structure'=any(g.scopes)), 'content'=any(g.scopes) and (` + traceInputsAllowed + `), 'export'=any(g.scopes),
 (r.trace_expires_at is not null and r.trace_expires_at<=clock_timestamp())
 from runs r join projects p on p.id=r.project_id join project_members pm on pm.project_id=p.id
 join members m on m.id=pm.member_id and m.enabled
 cross join lateral(select array(select scope from trace_grants where run_id=r.id and member_id=m.id and expires_at>clock_timestamp()) scopes)g
 where p.id=$1 and r.id=$2 and m.id=$3 for share of p,pm,m`

const traceSpansSQL = `select jsonb_build_object('run',r.id,'traceId',replace(r.id::text,'-',''),'rootSpanId',substr(encode(digest(r.id::text||'/0','sha256'),'hex'),1,16),'status',r.status,'planVersion',r.plan_version,'start',r.created_at,'end',r.finished_at,
 'kind',case when cb.run_id is null then 'execution' else 'revision' end,
 'spans',(select jsonb_agg(jsonb_build_object('step',n.ordinal,'spanId',encode(call_span_id(r.id,n.ordinal,coalesce(i.attempt_no,1)),'hex'),'agent',n.agent_id,'revision',n.agent_revision,
 'choice',n.choice,'selector',n.selector,'type',case when ar.card is not null then 'external' when ar.tool is not null then 'tool' else 'model' end,
 'resource',ar.resource_id,'resourceRevision',ar.resource_revision,
 'attempts',array(select jsonb_build_object('number',x.attempt_no,'spanId',encode(call_span_id(x.run_id,x.ordinal,x.attempt_no),'hex'),'status',x.status,'start',x.started_at,'end',x.ended_at) from call_attempts x where(x.run_id,x.ordinal)=(r.id,n.ordinal) order by x.attempt_no),'parents',array(select depends_on from node_edges e where(e.project_id,e.plan_version,e.ordinal)=(n.project_id,n.plan_version,n.ordinal) and cb.run_id is null order by depends_on),
 'tokens',case when exists(select from project_members pm join projects p on p.id=pm.project_id where pm.project_id=r.project_id and pm.member_id=$2 and(pm.can_view_costs or p.owner=$2)) then i.tokens end,'remote',` + remoteSummarySQL + `,'status',case when n.lookup_phase in ('review_evidence','synthesize') and evidence_follow_up_skipped(r.id,n.ordinal) then 'skipped' else coalesce(i.status,case when n.lookup_phase in ('answer','answer_calc') and exists(select from artifacts done where (done.run_id,done.ordinal)=(r.id,n.ordinal)) then 'skipped' else 'pending' end) end,'start',i.started_at,'end',i.ended_at)
 order by n.ordinal) from effective_run_nodes n join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) left join invocations i on i.run_id=r.id and i.ordinal=n.ordinal
 where n.run_id=r.id and (cb.run_id is null or n.ordinal=a.ordinal)),
	'gatewaySpans',array(select jsonb_strip_nulls(jsonb_build_object('spanId',substr(g.operation_key,1,16),'turn',g.turn,'type','model',
	 'model',m.upstream_model,'tier',c.tier,'attempt',g.attempt,'fallback',g.fallback,'status',g.status,'start',g.started_at,'end',g.finished_at)
	 || case when exists(select from project_members pm join projects p on p.id=pm.project_id
	 where pm.project_id=r.project_id and pm.member_id=$2 and (pm.can_view_costs or p.owner=$2)) then
	 jsonb_build_object('promptTokens',c.prompt_tokens,'completionTokens',c.completion_tokens,'cachedTokens',c.cached_tokens,'costMicroCny',c.cost_micro_cny::text)
	 else '{}'::jsonb end) from model_gateway_attempts g join model_gateway_charges c using(operation_key)
	 join model_catalog m on m.id=g.model_id where g.run_id=r.id order by g.started_at,g.operation_key),
 'events',array(select jsonb_build_object('sequence',e.seq,'time',e.occurred_at,'step',e.ordinal,'kind',e.kind,'code',e.code) from trace_events e where e.run_id=r.id order by seq))
 from runs r left join change_batches cb on cb.run_id=r.id left join artifacts a on a.id=cb.artifact_id where r.id=$1`

// Business excerpts refer to the pinned plan and committed artifact. Never collect HTTP headers,
// system prompts or provider request bodies in the operational event stream.
const traceContentSQL = `select pl.brief,v.body,i.remote_task,i.remote_context,
 (` + traceInputsAllowed + `) and run_working_set_usable(r.id,n.ordinal,coalesce(i.attempt_no,1))
 and not exists(select from invalid_working_step_rules(r.id,n.ordinal,i.attempt_no,$3) where scoped)
 from runs r join plans pl on(pl.project_id,pl.version)=(r.project_id,r.plan_version)
 join nodes n on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) and n.ordinal=$2
 left join change_batches cb on cb.run_id=r.id
 left join invocations i on i.run_id=r.id and i.ordinal=n.ordinal
 left join artifacts a on (cb.run_id is null and a.run_id=r.id and a.ordinal=n.ordinal) or (cb.run_id is not null and a.id=cb.artifact_id and a.ordinal=n.ordinal)
 left join artifact_versions v on v.artifact_id=a.id and v.version=case when cb.run_id is null then 1 else cb.applied_version end
 where r.id=$1 and(cb.run_id is null or a.id is not null)`

var privateField = regexp.MustCompile(`(?i)(authorization|bearer[[:space:]]|api[-_ ]?key|access[-_ ]?token|password|set-cookie|cookie[[:space:]]*:|-----BEGIN [^-]*PRIVATE KEY|sk-[A-Za-z0-9_-]{10}|密码|密钥|口令)`)

func traceExcerpt(value *string) *string {
	if value == nil {
		return nil
	}
	if privateField.MatchString(*value) {
		redacted := "〔敏感字段已遮盖〕"
		return &redacted
	}
	chars := []rune(*value)
	if len(chars) > 2000 {
		preview := string(chars[:2000]) + "〔节选〕"
		return &preview
	}
	return value
}

func (s Server) monitorTrace(w http.ResponseWriter, r *http.Request, actor identity.Member) error {
	var project string
	if err := s.DB.QueryRow(r.Context(), `select r.project_id from runs r join project_members m on m.project_id=r.project_id
	 where r.id=$1 and m.member_id=$2`, r.PathValue("run"), actor.ID).Scan(&project); err != nil {
		return err
	}
	r.SetPathValue("id", project)
	return s.trace(w, r, actor)
}

func (s Server) trace(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	run := r.PathValue("run")
	project := r.PathValue("id")
	mode := r.URL.Query().Get("mode")
	download := mode == "export"
	if mode != "" && !download && mode != "content" {
		return web.Fail(422, "未知诊断操作")
	}
	step := 0
	if raw := r.URL.Query().Get("step"); raw != "" {
		var err error
		step, err = strconv.Atoi(raw)
		if err != nil || step < 1 || step > 12 {
			return web.Fail(422, "步骤无效")
		}
	}
	if (mode == "content" && step == 0) || (mode == "" && step != 0) {
		return web.Fail(422, "请选择需要查看的步骤")
	}
	var rights traceRights
	var spans json.RawMessage
	var content map[string]any
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		authorize := func() error {
			var expired bool
			err := tx.QueryRow(ctx, traceAccessSQL, project, run, u.ID).Scan(&rights.Structure, &rights.Content, &rights.Export, &expired)
			if err != nil {
				return err
			}
			if !rights.Structure || (download && !rights.Export) || (step > 0 && !rights.Content) {
				return web.Fail(403, "此操作需要相应的运行诊断授权")
			}
			if expired {
				return web.Fail(410, "此运行的详细诊断保留期已结束")
			}
			return nil
		}
		if err := authorize(); err != nil {
			return err
		}
		if mode != "content" {
			if err := tx.QueryRow(ctx, traceSpansSQL, run, u.ID).Scan(&spans); err != nil {
				return err
			}
		}
		if step > 0 {
			var brief, body, remoteTask, remoteContext *string
			var allowed bool
			if err := tx.QueryRow(ctx, traceContentSQL, run, step, u.ID).Scan(&brief, &body, &remoteTask, &remoteContext, &allowed); err != nil {
				return err
			}
			if !allowed {
				return web.Fail(403, "引用资料、团队规则或个人偏好的当前权限不足")
			}
			content = map[string]any{"step": step, "brief": traceExcerpt(brief), "committedOutput": traceExcerpt(body), "remoteTask": traceExcerpt(remoteTask), "remoteContext": traceExcerpt(remoteContext)}
		}
		if err := authorize(); err != nil {
			return err
		}
		if step > 0 || download {
			action := "trace.read_content"
			if download {
				action = "trace.export"
			}
			return database.Audit(ctx, tx, u.ID, action, run, map[string]any{"step": step, "project": project, "includedContent": step > 0})
		}
		return nil
	})
	if err != nil {
		return err
	}
	if download {
		w.Header().Set("Content-Disposition", `attachment; filename="trace-`+run+`.json"`)
	}
	return web.JSON(w, 200, map[string]any{"trace": spans, "rights": rights, "content": content, "asOf": time.Now().UTC()})
}

// Project members can locate a run from its business status. Reading the technical
// timeline and each optional field is checked again by the run endpoint.
func (s Server) traceDirectory(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var data json.RawMessage
	err := s.DB.QueryRow(r.Context(), `select jsonb_build_object('runs',array(
 select jsonb_build_object('id',r.id,'start',r.created_at,'status',r.status,'plan',r.plan_version,'kind',case when cb.run_id is null then 'execution' else 'revision' end,
 'structure',(p.owner=$2 or exists(select from trace_grants where run_id=r.id and member_id=$2 and scope='structure' and expires_at>clock_timestamp())),
 'expires',r.trace_expires_at,'expired',(r.trace_expires_at is not null and r.trace_expires_at<=clock_timestamp()),
 'grants',case when p.owner=$2 then array(select jsonb_build_object('member',member_id,'scope',scope,'expires',expires_at) from trace_grants
 where run_id=r.id and expires_at>clock_timestamp() order by member_id,scope) else null end)
 from runs r left join change_batches cb on cb.run_id=r.id where r.project_id=p.id and ($3='' or (r.created_at,r.id)<(select before.created_at,before.id from runs before where before.id=nullif($3,'')::uuid and before.project_id=p.id))
 order by r.created_at desc,r.id desc limit 41))
 from projects p join project_members m on m.project_id=p.id where p.id=$1 and m.member_id=$2`, r.PathValue("id"), u.ID, r.URL.Query().Get("before")).Scan(&data)
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}
