package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/resources"
	"alliance/internal/web"
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"strings"
	"unicode/utf8"
)

type Server struct {
	DB    *pgxpool.Pool
	Vault resources.Vault
}
type Project struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Stage       string `json:"stage"`
	Revision    int    `json:"revision"`
	PlanVersion int    `json:"planVersion"`
	Owner       string `json:"owner"`
	Reviewer    string `json:"reviewer"`
	IsPartial   bool   `json:"isPartial,omitempty"`
}

const callReviewSummarySQL = `case when i.run_id is not null and (i.status in ('unknown','submitted','input_rejected') or exists(select from call_reviews v where(v.run_id,v.ordinal)=(i.run_id,i.ordinal))) then (select jsonb_build_object(
'inputIssue',case when jsonb_array_length(issue.list)=1 then issue.list->0 end,'inputIssues',issue.list,
'columnSuggestion',case when issue.list is not null then (
 select jsonb_build_object('status',s.current_status,'entries',case when s.current_status='ready' then s.entries end)
 from current_column_suggestions s where (s.run_id,s.ordinal,s.attempt_no,s.actor)=(i.run_id,i.ordinal,i.attempt_no,$1)) end,
'step',i.ordinal,'attempt',i.attempt_no,'status',i.status,'hasReceipt',i.remote_task is not null,
 'window',(select jsonb_build_object('executionDue',execution_due,'receiptDue',receipt_due,'overdue',execution_due<=clock_timestamp(),'expired',receipt_due<=clock_timestamp()) from submission_windows w where(w.run_id,w.ordinal)=(i.run_id,i.ordinal)),
 'lateReceipts',array(select jsonb_build_object('attempt',l.attempt_no,'outcome',l.outcome,'time',l.received_at,'fingerprint',coalesce(l.file_sha256,l.body_sha256),'hasRemoteTask',l.remote_task is not null) from late_call_receipts l where(l.run_id,l.ordinal)=(i.run_id,i.ordinal) order by l.attempt_no),
'authorized',exists(select from active_call_redos v where(v.run_id,v.ordinal,v.attempt_no)=(i.run_id,i.ordinal,i.attempt_no) and (v.input_mapping is null or v.authority_current)),
'reconfirmationRequired',exists(select from pending_column_reconfirmations pending where(pending.run_id,pending.ordinal,pending.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)),
'history',array(select jsonb_build_object('attempt',v.attempt_no,'decision',v.decision,'corrected',v.input_mapping is not null,'superseded',v.superseded_by is not null,
'correction',case when jsonb_typeof(v.input_mapping)='object' then history.list->0 end,'corrections',history.list,
'timedOut',v.submission_timed_out,'receiptDue',v.receipt_due,'actor',u.display_name,'evidence',case when run_review_notes_readable(i.run_id,$1) then c.reason end,'time',c.created_at) from call_reviews v join controls c on c.id=v.control_id join members u on u.id=c.actor
 cross join lateral (select visible_column_mappings(i.run_id,v.input_mapping) list) history
 where(v.run_id,v.ordinal)=(i.run_id,i.ordinal) order by c.revision))
 from (select case when i.status='input_rejected' then visible_column_issues(i.run_id,$1,i.input_issue) end as list) issue) end`

const remoteSummarySQL = `(select jsonb_build_object('step',j.ordinal,'state',i.remote_status,'phase',j.phase,'detail',j.detail,'deadline',j.deadline,'checks',j.checks,'cancelRequested',j.cancel_attempted,
 'canQuery',coalesce((scope.operations->>'query')::boolean,true) and not coalesce(last_input.state in ('queued','submitted'),false),
 'canSupplement',coalesce((scope.operations->>'supplement')::boolean,false) and j.phase='manual'
    and i.remote_status='input-required' and i.remote_context<>'' and j.input_revision>0
    and exists(select from runs current_run where current_run.id=j.run_id and current_run.status='reconcile')
    and not j.cancel_attempted and (last_input.state is null or last_input.state='rejected' or last_input.input_revision<>j.input_revision),
 'inputRevision',j.input_revision,'inputPrompt',case when i.remote_status='input-required' and run_review_notes_readable(i.run_id,$1) then j.input_prompt end,
 'inputState',last_input.state,'inputDetail',last_input.detail,'destination',a.name)
 from remote_jobs j join effective_run_nodes n on(n.run_id,n.ordinal)=(j.run_id,j.ordinal)
 join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision)
 cross join lateral (select remote_operation_availability(agent_released_remote_operation_evidence(n.agent_id,n.agent_revision)) operations) scope
 left join lateral (select q.state,q.detail,q.input_revision from remote_task_inputs q where (q.run_id,q.ordinal)=(j.run_id,j.ordinal)
   order by q.created_at desc,q.id desc limit 1) last_input on true
 where(j.run_id,j.ordinal)=(i.run_id,i.ordinal))`
const fields = "p.id,p.title,p.stage,p.revision,p.plan_version,p.owner,p.final_reviewer"
const access = " from projects p join project_members m on m.project_id=p.id where m.member_id=$1 "

func (s Server) Routes(m *http.ServeMux, g identity.Server) {
	m.Handle("GET /api/projects/{id}/events", web.Handler(s.events(g)))
	m.Handle("GET /api/projects/{id}/runs/{run}/draft-events", s.draftEvents(g))
	m.Handle("GET /api/projects/{id}/session-events", s.sessionEvents(g))
	actions := map[string]identity.Action{
		"GET /api/projects/{id}/artifacts/{artifact}/feedback/{feedback}/source-quote": s.feedbackQuote,
		"GET /api/projects/{id}/artifacts/{artifact}/source-comparison/{source}":       s.sourceComparison,
		"GET /api/teams/{team}/review-policy":                                          s.reviewPolicy,
		"PUT /api/teams/{team}/review-policy":                                          s.reviewPolicy,
		"GET /api/projects/{project}/review-policy":                                    s.reviewPolicy,
		"PUT /api/projects/{project}/review-policy":                                    s.reviewPolicy,
		"GET /api/projects/review-reminders":                                           s.reminders,
		"POST /api/projects/{id}/review-reminders/{clock}/extend":                      s.extendReview,
		"GET /api/project-agent-grants/inbox":                                          s.pendingAgentGrants,
		"GET /api/project-agent-grants":                                                s.listAgentGrants,
		"GET /api/projects/{id}/agent-grants":                                          s.listAgentGrants,
		"POST /api/projects/{id}/agent-grants":                                         s.offerAgent,
		"POST /api/project-agent-grants/{grant}":                                       s.decideAgentGrant,
		"POST /api/projects/{id}/feedback-assignments":                                 s.projectFeedback,
		"POST /api/projects/{id}/artifacts/{artifact}/source-assessments/{source}":     s.assessSource,
		"GET /api/project-recoveries":                                                  s.recoveryList,
		"POST /api/project-recoveries/{id}":                                            s.recoveryOffer,
		"POST /api/project-recoveries/{id}/{offer}":                                    s.recoveryDecision,
		"GET /api/projects/reviewer-handoffs":                                          s.handoffQueue,
		"POST /api/projects/{id}/takeover":                                             s.takeover,
		"POST /api/projects/{id}/handoffs/{handoff}":                                   s.decideHandoff,
		"GET /api/projects/review-inbox":                                               s.reviewInbox,
		"GET /api/workflow-templates":                                                  s.workflowTemplateList,
		"GET /api/workflow-templates/{template}":                                       s.workflowTemplateDetail,
		"POST /api/projects/{id}/workflow-templates":                                   s.saveWorkflowTemplate,
		"GET /api/projects/{id}/workflow-template-update":                              s.previewWorkflowTemplateUpdate,
		"POST /api/projects/{id}/workflow-template-update":                             s.adoptWorkflowTemplateUpdate,
		"POST /api/workflow-templates/{template}/publication":                          s.workflowTemplatePublication,
		"POST /api/workflow-templates/{template}/projects":                             s.useWorkflowTemplate,
		"POST /api/runs/{id}/remote/{step}/check":                                      s.recheckRemote,
		"POST /api/runs/{id}/remote/{step}/inputs":                                     s.queueRemoteInput,
		"GET /api/projects/{id}/diagnostics":                                           s.traceDirectory,
		"GET /api/projects/{id}/runs/{run}/trace":                                      s.trace,
		"PATCH /api/projects/{id}/runs/{run}/trace-access":                             s.traceGrant,
		"POST /api/projects/{id}/artifacts/{artifact}/feedback/{feedback}/change-runs": s.startChange,
		"POST /api/projects/{id}/change-runs/{change}/decision":                        s.decideChange,
		"POST /api/projects/{id}/artifacts/{artifact}/feedback":                        s.feedback,
		"POST /api/projects/{id}/artifacts/{artifact}/feedback/{feedback}":             s.feedback,
		"PATCH /api/projects/{id}/cost-access":                                         s.costAccess,
		"GET /api/projects/{id}/artifacts/{artifact}/versions":                         s.versions,
		"GET /api/runs/{id}/calls/{step}/attempts/{attempt}/late-receipt":              s.lateReceipt,
		"POST /api/runs/{id}/calls/{step}/review":                                      s.reviewCall,
		"GET /api/runs/{id}/receipt-passages":                                          s.receiptPassages,
		"POST /api/runs/{id}/calculation-receipts":                                     s.calculationReceipt,
		"POST /api/runs/{id}/lookup-receipts":                                          s.lookupReceipt,
		"POST /api/runs/{id}/tool-choice":                                              s.confirmToolChoice,
		"POST /api/runs/{id}/control":                                                  s.control,
		"POST /api/projects":                                                           s.create, "GET /api/projects": s.list, "GET /api/projects/{id}": s.detail,
		"POST /api/projects/{id}/messages": s.message, "POST /api/projects/{id}/plan": s.plan, "POST /api/projects/{id}/runs": s.start,
		"POST /api/projects/{id}/participants": s.participant, "POST /api/projects/{id}/artifacts/{artifact}/review": s.review,
		"POST /api/projects/{id}/artifacts/{artifact}/file-revisions": s.reviseFile,
		"POST /api/projects/{id}/artifacts/{artifact}/revise":         s.revise, "POST /api/projects/{id}/deliver": s.deliver, "GET /api/projects/{id}/delivery": s.download,
		"POST /api/projects/{id}/distribution-reviews": s.reviewDistribution,
		"POST /api/projects/{id}/delivery/revisions":   s.reviseDelivery,
		"POST /api/projects/{id}/delivery/assessments": s.assessDeliveryItem,
		"GET /api/projects/{id}/delivery/receipt":      s.deliveryReceipt,
	}
	for path, h := range actions {
		m.Handle(path, g.Protect(false, h))
	}
}
func lock(ctx context.Context, tx pgx.Tx, id, actor string) (Project, error) {
	var p Project
	err := tx.QueryRow(ctx, "select "+fields+access+"and p.id=$2 for update of p", actor, id).Scan(&p.ID, &p.Title, &p.Stage, &p.Revision, &p.PlanVersion, &p.Owner, &p.Reviewer)
	return p, err
}
func Note(ctx context.Context, tx pgx.Tx, id, kind, body string, actor any) error {
	_, err := tx.Exec(ctx, "insert into messages(project_id,author,kind,body) values($1,$2,$3,$4)", id, actor, kind, body)
	return err
}
func Touch(ctx context.Context, tx pgx.Tx, id, stage string) error {
	_, err := tx.Exec(ctx, "update projects set stage=$1,revision=revision+1,updated_at=now() where id=$2", stage, id)
	return err
}
func (s Server) create(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Goal, Team, Agent string
		AgentRevision     int
		Agents            []string
		AgentRevisions    []int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	goal := strings.TrimSpace(in.Goal)
	if goal == "" || utf8.RuneCountInString(goal) > 16000 {
		return web.Fail(422, "请描述工作目标，最多 16000 字")
	}
	if in.Agent != "" || in.AgentRevision != 0 {
		if in.Agents != nil || in.AgentRevisions != nil {
			return web.Fail(422, "请使用一种方式指定 Agent 版本")
		}
		in.Agents = []string{in.Agent}
		in.AgentRevisions = []int{in.AgentRevision}
	}
	if in.Agents != nil || in.AgentRevisions != nil {
		if len(in.Agents) < 1 || len(in.Agents) > 12 || len(in.Agents) != len(in.AgentRevisions) {
			return web.Fail(422, "请选择 1 到 12 个 Agent，并为每一步指定版本")
		}
		for i, id := range in.Agents {
			if _, err := uuid.Parse(id); err != nil || in.AgentRevisions[i] < 1 {
				return web.Fail(422, "请选择有效的 Agent 和已发布版本")
			}
		}
	}
	var id string
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		id, err = createProjectDraft(ctx, tx, u.ID, in.Team, goal)
		if err != nil || in.Agents == nil {
			return err
		}
		return SavePlan(ctx, tx, id, u.ID, PlanInput{Brief: goal, Expected: 1, Agents: in.Agents, AgentRevisions: in.AgentRevisions})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"id": id})
}

func createProjectDraft(ctx context.Context, tx pgx.Tx, actor, team, goal string) (string, error) {
	title := []rune(goal)
	if len(title) > 55 {
		title = title[:55]
	}
	var id string
	if err := tx.QueryRow(ctx, `insert into projects(owner, final_reviewer, team_id, title)
select $1, $1, t.team_id, $2 from team_members t cross join enterprise e
where t.member_id=$1 and case when $3<>'' then t.team_id=nullif($3,'')::uuid
    else t.team_id=e.default_team or not exists(
        select from team_members other where other.member_id=$1 and other.team_id<>t.team_id
    ) end
for share of t returning id`, actor, string(title), team).Scan(&id); err != nil {
		if err == pgx.ErrNoRows {
			if team == "" {
				var joined bool
				if err := tx.QueryRow(ctx, "select exists(select from team_members where member_id=$1)", actor).Scan(&joined); err != nil {
					return "", err
				}
				if joined {
					return "", web.Fail(422, "有多个可用团队，请选择本次项目归属")
				}
			}
			return "", web.Fail(403, "当前未加入项目所属团队，请重新选择")
		}
		return "", err
	}
	if _, err := tx.Exec(ctx, "insert into project_memberships values($1,$2,true)", id, actor); err != nil {
		return "", err
	}
	return id, Note(ctx, tx, id, "user", goal, actor)
}
func (s Server) list(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	rows, err := s.DB.Query(r.Context(), "select "+fields+
		",(p.stage='delivered' and coalesce((select d.partial from deliveries d where d.project_id=p.id order by d.edition desc limit 1),false))"+
		access+"order by p.updated_at desc", u.ID)
	if err != nil {
		return err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Project])
	if err != nil {
		return err
	}
	return web.JSON(w, 200, items)
}
func (s Server) detail(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx := r.Context()
	id := r.PathValue("id")
	var result []byte
	err := s.DB.QueryRow(ctx, `with artifact_access as materialized (
 select a.id,run_review_notes_readable(a.run_id,$1) as notes from artifacts a where a.project_id=$2
 ), feedback_access as materialized (
 select f.*,case when f.submission_id is null then rights.notes else
 feedback_notes_readable(f,$1) end as notes_readable
 from feedback f join artifact_access rights on rights.id=f.artifact_id
 ) select jsonb_build_object('id',p.id,'title',p.title,'stage',p.stage,'revision',p.revision,'planVersion',p.plan_version,'owner',p.owner,'reviewer',p.final_reviewer,'canReview',m.can_review,'canViewCosts',(m.can_view_costs or p.owner=$1),
 'team',p.team_id,'workflowTemplate',`+projectWorkflowTemplateSQL+`,
 'sharedAgents',array(select jsonb_build_object('id',g.agent_id,'name',v.name,'publishedName',v.name,'publishedOperation',agent_output_operation(v),'publishedLookup',v.resource_id is not null and agent_output_operation(v)='text' and not agent_uses_picture(v),'publishedVision',agent_uses_picture(v) and not agent_edits_picture(v),'publishedImageEdit',agent_edits_picture(v),'publishedOutline',coalesce(v.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb,false),'published',g.revision,'publishedReady',project_agent_admissible(p.id,g.agent_id,g.revision,$1)) from effective_project_agent_grants g join agent_revisions v on(v.agent_id,v.revision)=(g.agent_id,g.revision) where g.project_id=p.id and not project_agent_access(null,g.agent_id,g.revision,$1) order by v.name,g.agent_id),
 'adoption',(select jsonb_build_object('actor',member.display_name,'at',a.at) from answer_adoptions a join members member on member.id=a.actor where a.project_id=p.id),
 'distributionCases',`+distributionCasesSQL+`,'deliveries',array(select jsonb_build_object('id',d.id,'edition',d.edition,'time',d.created_at,'isPartial',d.partial,'distribution',distribution_state(d.id),'handoff',case when distribution_state(d.id) in('clear','reference') then d.handoff else '{}'::jsonb end) from deliveries d where d.project_id=p.id order by d.edition desc),
 'deliveryCoverage',project_delivery_coverage(p.id,p.plan_version,$1),
 'deliveryRevisionTargets',case when m.can_review and p.stage='delivered' then array(select artifact_id
   from delivery_revision_targets((select id from deliveries where project_id=p.id order by edition desc limit 1))) end,
 'deliveryParticipants',case when m.can_review and p.stage='review' and exists(select from plan_delivery_contracts c where (c.project_id,c.plan_version)=(p.id,p.plan_version) and c.allow_partial)
   then array(select jsonb_build_object('id',u.id,'name',u.display_name) from project_members t join members u on u.id=t.member_id and u.enabled and u.kind='person'
              where t.project_id=p.id and t.recovery_assignment is null order by u.display_name,u.id) end,
 'planningModel',(select jsonb_build_object('id',v.resource_id,'revision',v.revision,'name',m.name,'enabled',m.enabled and not m.emergency and not v.key_blocked,
 'estimate',v.estimate_cny::text,'payer',t.name)
 from model_revisions v join model_resources m on m.id=v.resource_id join teams t on t.id=m.team_id join team_members tm on tm.team_id=m.team_id and tm.member_id=$1
 where(v.resource_id,v.revision)=(p.planning_resource,p.planning_revision)),
 'budget',(select jsonb_build_object('limit',limit_cny::text,'warningPercent',warning_percent,'revision',revision,'warning',warning,'exhausted',exhausted,'pending',(select count(*) from budget_requests br where br.project_id=p.id and br.state='pending'),'canManage',exists(select from teams t join team_members tm on tm.team_id=t.id and tm.member_id=t.budget_owner where t.id=p.team_id and t.budget_owner=$1)) from project_budget_status where project_id=p.id),
 'costSummary',(select jsonb_build_object('recorded',coalesce(sum(amount_cny),0)::text,'reserved',coalesce(sum(greatest(reserved_cny-amount_cny,0)) filter(where not settled),0)::text,'unknown',count(*) filter(where reserved_cny is null and not settled),'calls',count(*)) from spend where project_id=p.id),
 'costDetails',case when m.can_view_costs or p.owner=$1 then coalesce((select jsonb_agg(jsonb_build_object(
   'id',s.id,'scope',s.kind,'sourceName',s.source_name,'sourceRevision',s.source_revision,'step',coalesce(i.ordinal,cs.ordinal),'attempt',coalesce(i.attempt_no,cs.attempt_no),'agent',ar.name,'model',model.model,'recorded',s.amount_cny::text,'estimate',s.reserved_cny::text,'settled',s.settled,'time',s.created_at,'tokens',coalesce(i.tokens,plan.tokens,cs.tokens),'payer',t.name)
   order by s.created_at) from spend s join teams t on t.id=s.team_id left join runs run on run.id=s.run_id
   left join call_attempts i on i.run_id=run.id and s.op_key=i.op_key
   left join effective_run_nodes n on(n.run_id,n.ordinal)=(run.id,i.ordinal)
   left join validations trial on s.kind='validation' and s.op_key='validation/'||trial.id::text
   left join agent_revisions ar on(ar.agent_id,ar.revision)=(coalesce(n.agent_id,trial.agent_id),coalesce(n.agent_revision,trial.revision))
   left join planning_requests plan on s.op_key='planning/'||plan.id::text
   left join column_suggestions cs on s.op_key='column-suggestion/'||cs.id::text
   left join model_revisions model on(model.resource_id,model.revision)=(coalesce(ar.resource_id,plan.resource_id,cs.resource_id),coalesce(ar.resource_revision,plan.resource_revision,cs.resource_revision))
   where s.project_id=p.id),'[]') else null end,
 'canTakeover',exists(select from members adm join team_members tm on tm.member_id=adm.id where adm.id=$1 and adm.enabled and adm.role='admin' and tm.team_id=p.team_id) and `+unavailableReviewer+`,
 'recoveryUntil',(select h.recovery_until from reviewer_handoffs h where h.id=m.recovery_assignment),
 'takeoverAssignment',(select h.id from reviewer_handoffs h where h.id=p.reviewer_assignment and h.emergency and h.successor=$1 and p.final_reviewer=$1),
 'handoffs',coalesce((select jsonb_agg(jsonb_build_object('id',h.id,'previous',o.display_name,'next',n.display_name,'successor',h.successor,'author',h.proposer,'by',a.display_name,'emergency',h.emergency,'reason',h.reason,'end',h.end_condition,'state',h.state,'created',h.created_at,'recoveryUntil',h.recovery_until,'accessEnd',coalesce(h.access_end_reason,case when h.recovery_until<=now() then '授权已到期' end),'decidedAt',h.decided_at,'decidedBy',d.display_name,'decisionReason',h.decision_reason) order by h.created_at desc) from reviewer_handoffs h join members o on o.id=h.previous_reviewer join members n on n.id=h.successor join members a on a.id=h.proposer left join members d on d.id=h.decision_by where h.project_id=p.id),'[]'),
 'participants',(select jsonb_agg(jsonb_build_object('id',u.id,'name',u.display_name,'canReview',pm.can_review,'canViewCosts',(pm.can_view_costs or p.owner=pm.member_id),'enabled',u.enabled) order by u.display_name,u.id) from project_members pm join members u on u.id=pm.member_id where pm.project_id=p.id),
 'previousPlanningOutputs',(select string_to_array(q.required_output,',') from planning_requests q where q.project_id=p.id and q.actor=$1 and p.owner=$1 and p.stage in ('brief','plan') order by q.created_at desc,q.id desc limit 1),
 'previousPlanningOutput',(select case when strpos(q.required_output,',')=0 then q.required_output else '' end from planning_requests q where q.project_id=p.id and q.actor=$1 and p.owner=$1 and p.stage in ('brief','plan') order by q.created_at desc,q.id desc limit 1),
 'previousPlanningSources',(select q.input_sources from planning_requests q where q.project_id=p.id and q.actor=$1 and p.owner=$1 and p.stage in ('brief','plan') and q.input_sources is not null order by q.created_at desc,q.id desc limit 1),
 'suggestion',(select jsonb_build_object('id',q.id,'status',q.current_status,'result',case when q.current_status='ready' then q.result end,'inputSources',q.input_sources,'requiredOutput',case when strpos(q.required_output,',')=0 then q.required_output else '' end,'requiredOutputs',string_to_array(q.required_output,','),'baseRevision',q.project_revision,'resource',q.resource_id,'resourceRevision',q.resource_revision,'requestKey',q.request_key,
 'bindings',coalesce((select jsonb_agg(jsonb_build_object('slot',g.slot,'revision',g.revision,'agent',g.agent_id,'version',g.agent_revision,'name',v.name,'example',g.example_input,'expected',g.expected_output,'operation',agent_output_operation(v),'imagePayer',case when mr.operation='png' and member_team.member_id is not null then payer.name end,'imageEstimate',case when mr.operation='png' and member_team.member_id is not null then mr.estimate_cny::text end,
 'exactCheck',(select c.id from agent_checks c where member_team.member_id is not null and (c.agent_id,c.revision)=(g.agent_id,g.agent_revision)
 and c.match_kind='exact' and c.sample=g.example_input and c.expected=g.expected_output and c.reviewer is null order by c.ordinal limit 1),
 'validated',agent_verified(g.agent_id,g.agent_revision),
 'ready',coalesce((a.published_revision=g.agent_revision or exists(select from effective_project_agent_grants e where (e.project_id,e.agent_id,e.revision)=(p.id,a.id,g.agent_revision))) and project_agent_admissible(p.id,g.agent_id,g.agent_revision,$1),false)) order by g.slot)
 from gap_bindings g join agents a on a.id=g.agent_id join agent_revisions v on(v.agent_id,v.revision)=(g.agent_id,g.agent_revision) left join model_revisions mr on (mr.resource_id,mr.revision)=(v.resource_id,v.resource_revision) join teams payer on payer.id=a.team_id left join team_members member_team on member_team.team_id=a.team_id and member_team.member_id=$1 where g.request_id=q.id and q.current_status='ready'),'[]')) from current_planning_requests q where project_id=p.id and project_revision>=p.revision-1 and actor=$1 order by created_at desc limit 1),
 'messages',coalesce((select jsonb_agg(jsonb_build_object('seq',seq,'kind',m.kind,'body',case when m.planning_request is null or (planning_rules_readable(q.rule_pins,$1) and planning_sources_readable(q.project_id,q.input_sources)) then m.body else '此答复采用的规则或资料已失效或当前无权查看，请重新核对。' end) order by seq) from messages m left join current_planning_requests q on q.id=m.planning_request where m.project_id=p.id),'[]'),
 'plan',(select jsonb_build_object('brief',brief,'quote',`+planQuoteSQL+`,
 'sources',coalesce((select jsonb_agg(jsonb_strip_nulls(jsonb_build_object('id',ps.source_id,'version',ps.source_version,'receiptStep',ps.receipt_step,
 'picture',case when source_is_picture(sv.media_type) then true end)) order by coalesce(ps.picture_order,ps.receipt_order,0),ps.source_id)
 from plan_sources ps join source_versions sv on (sv.source_id,sv.version)=(ps.source_id,ps.source_version)
 where ps.project_id=p.id and ps.plan_version=p.plan_version),'[]'),
 'rules',array(select jsonb_build_object('revision',pr.rule_revision,'available',rule_usable(pr.rule_id,pr.rule_revision,$1),'body',case when rule_usable(pr.rule_id,pr.rule_revision,$1) then v.body end)
 from plan_rules pr join rule_versions v on(v.rule_id,v.revision)=(pr.rule_id,pr.rule_revision) where pr.project_id=p.id and pr.plan_version=p.plan_version order by pr.rule_id),
 'preferences',coalesce((select jsonb_agg(jsonb_build_object('id',pp.preference_id,'revision',pp.revision) order by pp.preference_id) from plan_preferences pp join preferences f on f.id=pp.preference_id where pp.project_id=p.id and pp.plan_version=p.plan_version and f.member_id=$1),'[]'),
 'nodes',(select jsonb_agg(jsonb_build_object('agent',n.agent_id,'name',a.name,'revision',n.agent_revision,'operation',agent_output_operation(a),'vision',agent_uses_picture(a) and not agent_edits_picture(a),'editsImage',agent_edits_picture(a),'outline',coalesce(a.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb,false),'lookupPhase',n.lookup_phase,'toolChoice',
 (select jsonb_build_object('selector',c.selector,'agent',v.agent_id,'name',v.name,'revision',v.revision,'primaryConfig',aggregate_scope(a.tool),'config',aggregate_scope(v.tool),
   'selected',coalesce(pick.manual_choice,pick.choice),'manual',pick.manual_choice is not null) from plan_tool_choices c join agent_revisions v on (v.agent_id,v.revision)=(c.alternative,c.alternative_revision)
   left join run_tool_choices pick on (pick.run_id,pick.ordinal)=((select r.id from runs r
      where(r.project_id,r.plan_version)=(p.id,p.plan_version) and not exists(select from change_batches cb where cb.run_id=r.id)
      order by r.created_at desc limit 1),n.ordinal)
   where(c.project_id,c.plan_version,c.ordinal)=(n.project_id,n.plan_version,n.ordinal)), 'dependsOn',coalesce((select jsonb_agg(depends_on order by depends_on) from node_edges e where(e.project_id,e.plan_version,e.ordinal)=(n.project_id,n.plan_version,n.ordinal)),'[]')) order by ordinal) from nodes n join agent_revisions a on(a.agent_id,a.revision)=(n.agent_id,n.agent_revision) where n.project_id=p.id and n.plan_version=p.plan_version)) from plans where project_id=p.id and version=p.plan_version),
 'feedbackOriginals',coalesce((select jsonb_object_agg(s.id,s.original) from project_feedback_submissions s where s.project_id=p.id
 and not exists(select from feedback_access f where f.submission_id=s.id and not f.notes_readable)),'{}'),
 'artifacts',coalesce((select jsonb_agg(jsonb_build_object('id',a.id,'name',a.name,'ordinal',a.ordinal,'reviewNotesReadable',rights.notes,'verifiedWord',distribution_readable(a.id,v.version,$1) and coalesce(verified_document_format(a.id,v.version)='docx',false),'editMode',(select case when agent_edits_picture(agent) then 'image_edit' when agent_output_operation(agent)='png' then 'regenerate' when agent.tool is null then 'feedback' when agent.tool->>'operation'='pptx' and distribution_readable(a.id,v.version,$1) and editable_slide_outline(a.id,v.version) is not null then 'outline' when agent.tool @> '{"operation":"pptx","contract":"pptx-outline-v1"}'::jsonb then 'externalOutline' when agent.tool->>'operation' in ('docx','xlsx','pptx') then 'replace' else 'manual' end from runs origin join effective_run_nodes n on n.run_id=origin.id join agent_revisions agent on(agent.agent_id,agent.revision)=(n.agent_id,n.agent_revision) where origin.id=a.run_id and n.ordinal=a.ordinal),'version',v.version,'body',case when distribution_readable(a.id,v.version,$1) then v.body else '此成果版本已暂停分发' end,'approved',e.reviewer is not null,'needsTakeoverReview',takeover_review_needed(p.id,a.id,v.version),
    'sourceChanges',array(select jsonb_build_object('source',x.source_id,'originalName',x.original_name,'name',x.current_name,'pinned',x.pinned,'observed',x.observed,'revoked',x.revoked,'decision',x.decision,'selectableAssessment',case when not x.revoked and not exists(select from effective_run_nodes en join agent_revisions er on(er.agent_id,er.revision)=(en.agent_id,en.agent_revision) where (en.run_id,en.ordinal)=(a.run_id,a.ordinal) and agent_edits_picture(er)) and usable_change_source(x.artifact_id,x.source_id,x.pinned,x.observed) then x.assessment end,'feedbackId',x.feedback_id,'reason',case when rights.notes then x.reason end,'actor',assessor.display_name) from changed_artifact_sources x left join members assessor on assessor.id=x.actor where x.artifact_id=a.id order by x.current_name,x.source_id),
    'feedback',coalesce((select jsonb_agg(jsonb_build_object('id',f.id,'submission',f.submission_id,'submissionItem',f.submission_item,'quoteAvailable',f.notes_readable and exists(select from feedback_source_quotes q join sources qs on qs.id=q.source_id and qs.revoked_at is null where q.feedback_id=f.id),'kind',f.kind,'body',case when f.notes_readable then f.body end,'position',case when f.notes_readable then f.position end,'state',f.state,'author',f.author,'reviewer',f.reviewer,'base',f.base_version,'candidate',f.candidate_version,'revision',f.revision,
    'changes',coalesce((select jsonb_agg(jsonb_build_object('run',cb.run_id,'base',cb.base_version,'slidePage',cb.slide_page,'imageRegion',cb.image_region,'sourceInput',(select receipt from change_source_receipts where run_id=cb.run_id),'disposition',cb.disposition,'applied',cb.applied_version,'status',r.status,'controlRevision',r.control_revision,'canConcludeFailure',run_failure_confirmable(r.id),'actor',r.actor,'callReview',`+callReviewSummarySQL+`,'remote',`+remoteSummarySQL+`,
    'body',case when cb.disposition='conflict' and distribution_readable(cb.artifact_id,cb.base_version,$1) and run_working_set_usable(r.id,i.ordinal,i.attempt_no) then i.result end,'file',case when cb.disposition='conflict' and distribution_readable(cb.artifact_id,cb.base_version,$1)
      and run_working_set_usable(r.id,i.ordinal,i.attempt_no) then ff.filename end) order by r.created_at desc)
    from change_batches cb join runs r on r.id=cb.run_id left join invocations i on i.run_id=r.id left join invocation_files ff on ff.run_id=r.id where cb.feedback_id=f.id and f.notes_readable),'[]'),
    'events',(select jsonb_agg(jsonb_build_object('action',action,'reason',case when f.notes_readable then ev.reason end,'version',artifact_version,'actor',m.display_name) order by ev.revision) from feedback_events ev join members m on m.id=ev.actor where ev.feedback_id=f.id)) order by f.created_at) from feedback_access f where f.artifact_id=a.id),'[]'),'file',(select filename from artifact_files where artifact_id=a.id and version=v.version and distribution_readable(a.id,v.version,$1))) order by a.ordinal) from artifacts a join artifact_access rights on rights.id=a.id join artifact_versions v on(v.artifact_id,v.version)=(a.id,a.current_version) left join reviews e on (e.artifact_id,e.version)=(a.id,a.current_version) where a.project_id=p.id),'[]'),
 'run',(select jsonb_build_object('id',r.id,'status',r.status,'controlRevision',r.control_revision,'canResolveToolChoice',p.owner=$1 or (r.app_id is not null and r.delegated_by is null and p.final_reviewer=$1 and m.can_review),'receiptRequest',(select jsonb_build_object('step',ordinal,'reason',reason,'kind',kind) from calculation_requests where run_id=r.id and r.status='blocked' order by ordinal limit 1),'indexWait',`+LookupIndexWaitSQL+`,
 'draftAvailable',r.actor=$1 and r.service_id is null and exists(select from effective_run_nodes en
     join agent_revisions ar on(ar.agent_id,ar.revision)=(en.agent_id,en.agent_revision)
     join model_revisions mr on(mr.resource_id,mr.revision)=(ar.resource_id,ar.resource_revision)
     where en.run_id=r.id and en.lookup_phase='' and mr.stream_text),
 'lookupPending',r.status='running' and exists(select from lookup_turns t where t.run_id=r.id and t.decision is not null
     and (t.action_kind is null or t.action_kind='refine_lookup')),
 'calculationPending',r.status='running' and exists(select from lookup_turns t where t.run_id=r.id and t.decision is not null
     and t.action_kind='calculate_amounts'),
 'canConcludeFailure',run_failure_confirmable(r.id),'attempts',(select coalesce(jsonb_agg(jsonb_build_object('step',ordinal,'status',status,
 'workingSet',case when r.actor=$1 then (select jsonb_build_object(
    'sources',count(*) filter(where d.kind='source'),'preferences',count(*) filter(where d.kind='preference'),
    'rules',count(*) filter(where d.kind='rule'),
    'parents',(select count(*) from run_working_parents p where (p.run_id,p.ordinal,p.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)),
    'usable',run_working_set_usable(i.run_id,i.ordinal,i.attempt_no))
    from run_working_sets c left join run_working_inputs d using(run_id,ordinal,attempt_no)
    where (c.run_id,c.ordinal,c.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
    having count(c.run_id)>0) end,'callReview',`+callReviewSummarySQL+`,'remote',`+remoteSummarySQL+`,'tokens',case when m.can_view_costs or p.owner=$1 then tokens end,'diagnostic',case when r.trace_expires_at is null or r.trace_expires_at>clock_timestamp() then diagnostic end) order by ordinal),'[]') from invocations i where run_id=r.id)) from runs r where project_id=p.id and not exists(select from change_batches cb where cb.run_id=r.id) order by created_at desc limit 1))`+access+"and p.id=$2", u.ID, id).Scan(&result)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(result)
	return err
}
func (s Server) message(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct{ Body string }
	if err := web.Read(r, &in); err != nil {
		return err
	}
	if web.Blank(in.Body) {
		return web.Fail(422, "消息为空")
	}
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if err = Note(ctx, tx, p.ID, "user", in.Body, u.ID); err != nil {
			return err
		}
		return Touch(ctx, tx, p.ID, p.Stage)
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func (s Server) participant(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Member, Email, Reason string
		Review, Final         bool
		Expected              int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	var offer string
	ctx := r.Context()
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if (!in.Final && p.Owner != u.ID) || (in.Final && p.Reviewer != u.ID && p.Owner != u.ID) {
			return web.Fail(403, "需要项目邀请或最终确认转交权限")
		}
		if p.Revision != in.Expected || p.Stage == "delivered" {
			return web.Fail(409, "项目已变更")
		}
		if in.Final && web.Blank(in.Reason) {
			return web.Fail(422, "请说明转交最终确认权的原因")
		}
		if in.Member == "" && !web.Blank(in.Email) {
			if err = tx.QueryRow(ctx, "select id from members where email=$1 and enabled and kind='person'", strings.ToLower(strings.TrimSpace(in.Email))).Scan(&in.Member); err != nil {
				return err
			}
		}
		if in.Member == "" {
			return web.Fail(422, "请选择成员或输入企业成员邮箱")
		}
		if in.Final {
			offer, err = nominateReviewer(ctx, tx, p, u.ID, in.Member, in.Reason, "", false)
			return err
		}
		if !in.Review {
			if in.Member == p.Reviewer {
				return web.Fail(409, "请先正式转交最终确认权")
			}
			var pending bool
			err := tx.QueryRow(ctx, "select exists(select from feedback f join artifacts a on a.id=f.artifact_id where a.project_id=$1 and f.reviewer=$2 and f.state in ('suggested','required','investigate','verification'))", p.ID, in.Member).Scan(&pending)
			if err != nil {
				return err
			}
			if pending {
				return web.Fail(409, "请先为未处理反馈转交审核职责")
			}
		}
		tag, err := tx.Exec(ctx, "insert into project_memberships select $1,id,$3 from members where id=$2 and enabled and kind='person' on conflict(project_id,member_id) do update set can_review=excluded.can_review,recovery_assignment=null", p.ID, in.Member, in.Review)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(404, "成员不可用")
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "project.grant", p.ID, map[string]any{"member": in.Member, "review": in.Review, "final": in.Final, "reason": in.Reason})
	})
	if err != nil {
		return err
	}
	if in.Final {
		return web.JSON(w, 202, map[string]string{"id": offer, "state": "pending"})
	}
	w.WriteHeader(204)
	return nil
}

type SourcePin struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}
type PlanInput struct {
	DeliveryContract *DeliveryContract
	ToolChoices      []ToolChoicePin
	Replacements     []StepReplacement
	ReplacementBase  int
	SourceLookup     bool
	CalculationSteps []int
	LookupSteps      []int
	Brief            string
	Suggestion       string
	Agents           []string
	Dependencies     [][]int
	AgentRevisions   []int
	Sources          []SourcePin
	Preferences      []struct {
		ID       string
		Revision int
	}
	Expected int
}

func (s Server) plan(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in PlanInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	err := database.WithTx(r.Context(), s.DB, func(tx pgx.Tx) error { return SavePlan(r.Context(), tx, r.PathValue("id"), u.ID, in) })
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
func SavePlan(ctx context.Context, tx pgx.Tx, project, actor string, in PlanInput) error {
	if web.Blank(in.Brief) || len(in.Agents) < 1 || len(in.Agents) > 12 {
		return web.Fail(422, "请提供目标和 1 到 12 个已发布 Agent")
	}
	p, err := lock(ctx, tx, project, actor)
	if err != nil {
		return err
	}
	if p.Owner != actor {
		return web.Fail(403, "需要发起人权限")
	}
	if p.Revision != in.Expected || (p.Stage != "brief" && p.Stage != "plan") {
		return web.Fail(409, "方案状态或修订已变化")
	}
	if len(in.Replacements) > 0 && (p.PlanVersion < 1 || p.PlanVersion != in.ReplacementBase) {
		return web.Fail(409, "被替换的方案已更新，请重新选择该步骤的 Agent")
	}
	ver := p.PlanVersion + 1
	if _, err = tx.Exec(ctx, "insert into plans(project_id,version,brief) values($1,$2,$3)", p.ID, ver, in.Brief); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into plan_rules select p.id,$2,r.id,r.published_revision from projects p join team_rules r on r.team_id=p.team_id where p.id=$1 and not r.disabled and r.published_revision is not null`, p.ID, ver); err != nil {
		return err
	}
	if in.Suggestion != "" {
		var stored []byte
		err := tx.QueryRow(ctx, `select q.result from current_planning_requests q where q.id=$1 and q.project_id=$2 and q.actor=$3 and q.current_status='ready' and q.project_revision=$4-1 and q.result->>'kind'='plan'
 and (q.input_sources is null or (q.input_sources @> $6::jsonb and q.input_sources <@ $6::jsonb))
 and q.rule_pins=(select coalesce(jsonb_agg(jsonb_build_object('id',rule_id,'rev',rule_revision) order by rule_id),'[]') from plan_rules where project_id=$2 and plan_version=$5)`, in.Suggestion, p.ID, actor, p.Revision, ver, append([]SourcePin{}, in.Sources...)).Scan(&stored)
		if err == pgx.ErrNoRows {
			return web.Fail(409, "原建议、资料范围或团队规则已变化，请重新核对方案")
		}
		if err != nil {
			return err
		}
		if err := checkSuggestedToolChoice(ctx, tx, in, stored); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "update plans set suggestion_id=$1 where project_id=$2 and version=$3", in.Suggestion, p.ID, ver); err != nil {
			return err
		}
	}

	for index, source := range in.Sources {
		tag, err := tx.Exec(ctx, `insert into plan_sources(project_id,plan_version,source_id,source_version,picture_order)
 select $1,$2,s.id,$4,case when source_is_picture(v.media_type) then $5::integer end from sources s join source_versions v on v.source_id=s.id and v.version=$4
 where s.id=$3 and s.project_id=$1 and s.revoked_at is null and (v.extracted is not null or source_is_picture(v.media_type))`, p.ID, ver, source.ID, source.Version, index+1)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "选定资料不可引用")
		}
	}
	for _, pref := range in.Preferences {
		tag, err := tx.Exec(ctx, `insert into plan_preferences select $1,$2,id,revision from preferences where id=$3 and member_id=$4 and revision=$5 and enabled and deleted_at is null and expires_at>now()`, p.ID, ver, pref.ID, actor, pref.Revision)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return web.Fail(409, "个人偏好已失效")
		}
	}
	nodes, err := executionNodes(in)
	if err != nil {
		return err
	}
	for i, node := range nodes {
		tag, err := tx.Exec(ctx, `insert into nodes select $1,$2,$3,a.id,chosen.revision,$8::text from agents a
 left join effective_project_agent_grants scoped on scoped.project_id=$1 and scoped.agent_id=a.id and not project_agent_access(null,a.id,scoped.revision,$4)
 cross join lateral(select coalesce($6::integer,scoped.revision,a.published_revision) revision) chosen
 where a.id=$5 and not disabled and published_revision is not null
 and ($8::text='' or exists(select from agent_revisions v join model_revisions mr on (mr.resource_id,mr.revision)=(v.resource_id,v.resource_revision) where (v.agent_id,v.revision)=(a.id,chosen.revision) and mr.operation='text'))
 and project_agent_admissible($1,a.id,chosen.revision,$4) and
             ($6::integer is null or a.published_revision=$6 or exists(select from effective_project_agent_grants g where (g.project_id,g.agent_id,g.revision)=($1,a.id,$6)) or exists (select from nodes prev where prev.project_id=$1 and prev.plan_version=$7 and prev.agent_id=a.id and prev.agent_revision=$6))`, p.ID, ver, i+1, actor, node.agent, node.revision, p.PlanVersion, node.phase)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return web.Fail(422, "所选能力尚未发布或未获授权")
		}
	}
	if err := saveDeliveryContract(ctx, tx, p, ver, in.DeliveryContract, nodes); err != nil {
		return err
	}
	if err := checkStepReplacements(ctx, tx, p.ID, p.PlanVersion, ver, in.Replacements); err != nil {
		return err
	}
	if err := checkPlanPictures(ctx, tx, p.ID, ver, false); err != nil {
		return err
	}
	if err := saveToolChoices(ctx, tx, p.ID, ver, actor, nodes, in.ToolChoices); err != nil {
		return err
	}

	if in.Suggestion != "" {
		var missing string
		err := tx.QueryRow(ctx, `select f.format from planning_requests q
         cross join lateral jsonb_array_elements_text(coalesce(q.result->'outputFormats',
           case when coalesce(q.result->>'outputFormat','') <> '' then jsonb_build_array(q.result->>'outputFormat') else '[]'::jsonb end)) f(format)
         where q.id=$1 and not exists (
           select from nodes n join agent_revisions v on (v.agent_id,v.revision)=(n.agent_id,n.agent_revision)
           where n.project_id=$2 and n.plan_version=$3 and agent_file_format(v)=f.format
             and not exists(select from plan_tool_choices c where (c.project_id,c.plan_version,c.selector)=(n.project_id,n.plan_version,n.ordinal))
             and agent_accepts_planning_inputs(v,q.input_sources)) limit 1`, in.Suggestion, p.ID, ver).Scan(&missing)
		if err == nil {
			return web.Fail(422, "方案缺少可交付 "+missing+" 成果的 Agent")
		}
		if err != pgx.ErrNoRows {
			return err
		}
	}
	for i, node := range nodes {
		for _, d := range node.dependencies {
			if _, err := tx.Exec(ctx, "insert into node_edges values($1,$2,$3,$4)", p.ID, ver, i+1, d); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(ctx, "update projects set plan_version=$1 where id=$2", ver, p.ID); err != nil {
		return err
	}
	if len(in.Replacements) > 0 {
		if err := database.Audit(ctx, tx, actor, "plan.replace_agents", p.ID, map[string]any{
			"fromVersion": p.PlanVersion, "toVersion": ver, "steps": in.Replacements,
		}); err != nil {
			return err
		}
	}
	return Touch(ctx, tx, p.ID, "plan")
}
func (s Server) start(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected                int
		AuthorizeCalls          int
		AwaitCalculationReceipt bool
		AwaitLookupReceipt      bool
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 180 {
		return web.Fail(422, "需要 Idempotency-Key")
	}
	ctx := r.Context()
	var run string
	id := r.PathValue("id")
	sig := web.Hash(fmt.Sprintf("%s/%d/%d", id, in.Expected, in.AuthorizeCalls))
	if in.AwaitCalculationReceipt {
		sig = web.Hash(fmt.Sprintf("%s/%d/%d/receipt", id, in.Expected, in.AuthorizeCalls))
	}
	if in.AwaitLookupReceipt {
		sig = web.Hash(fmt.Sprintf("%x/lookup-receipt", sig))
	}
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, id, u.ID)
		if err != nil {
			return err
		}
		if p.Owner != u.ID {
			return web.Fail(403, "需要项目发起人确认")
		}
		var matches bool
		err = tx.QueryRow(ctx, "select id,request_hash=$3 from runs where actor=$1 and request_key=$2", u.ID, key, sig).Scan(&run, &matches)
		if err == nil {
			if !matches {
				return web.Fail(409, "幂等键已用于不同请求")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		if p.Stage != "plan" || p.Revision != in.Expected {
			return web.Fail(409, "方案已变化，请重新确认")
		}
		var count int
		var unavailable bool
		if err = tx.QueryRow(ctx, "select exists(select from nodes where project_id=$1 and plan_version=$2 and not project_agent_admissible(project_id,agent_id,agent_revision,$3))", id, p.PlanVersion, u.ID).Scan(&unavailable); err != nil {
			return err
		}
		if unavailable {
			return web.Fail(409, "方案中的能力待复验、停用或未获授权，请检查绑定修订")
		}
		var hasCalculation, hasLookup bool
		if err = tx.QueryRow(ctx, "select count(*),count(*) filter(where lookup_phase='answer_calc')>0,count(*) filter(where lookup_phase='answer')>0 from nodes where project_id=$1 and plan_version=$2", id, p.PlanVersion).Scan(&count, &hasCalculation, &hasLookup); err != nil {
			return err
		}
		if in.AwaitCalculationReceipt && !hasCalculation {
			return web.Fail(422, "此方案没有可等待补件的金额核算步骤")
		}
		if in.AwaitLookupReceipt && !hasLookup {
			return web.Fail(422, "此方案没有可等待补件的原文问答步骤")
		}
		if err := CheckAdmission(ctx, tx, p.ID, p.PlanVersion, u.ID); err != nil {
			return err
		}
		if in.AuthorizeCalls != count {
			return web.Fail(422, "请逐次明确授权本方案的未知费用调用上限")
		}
		if err = tx.QueryRow(ctx, `insert into runs(project_id,plan_version,actor,charge_team,request_key,request_hash,max_calls,await_calculation_receipt,await_lookup_receipt) select id,plan_version,$2,team_id,$3,$4,$5,$6,$7 from projects where id=$1 returning id`, id, u.ID, key, sig, in.AuthorizeCalls, in.AwaitCalculationReceipt, in.AwaitLookupReceipt).Scan(&run); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into outbox(run_id) values($1)", run); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "update plans set confirmed_by=$1 where project_id=$2 and version=$3", u.ID, id, p.PlanVersion); err != nil {
			return err
		}
		if err = Touch(ctx, tx, id, "running"); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "run.confirm", run, map[string]any{"plan": p.PlanVersion, "maxCalls": count, "awaitCalculationReceipt": in.AwaitCalculationReceipt, "awaitLookupReceipt": in.AwaitLookupReceipt})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]string{"id": run})
}

// CheckAdmission rechecks the fixed plan before a new run is accepted. In-flight runs
// subsequently use the normal-stop/emergency-stop distinction at each operation.
func CheckAdmission(ctx context.Context, tx pgx.Tx, project string, version int, actor string) error {
	if err := checkPlanPictures(ctx, tx, project, version, true); err != nil {
		return err
	}
	var missing bool
	if err := tx.QueryRow(ctx, `select exists(select from project_workflow_template_binding t join workflow_template_versions v using(template_id,version)
 where t.project_id=$1 and v.requires_sources and not exists(select from plan_sources p join sources s on s.id=p.source_id and s.revoked_at is null
 join source_versions r on(r.source_id,r.version)=(p.source_id,p.source_version) and (r.extracted is not null or source_is_picture(r.media_type))
 where(p.project_id,p.plan_version)=($1,$2)))`, project, version).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return web.Fail(409, "请为本次模板任务选择新的可读资料")
	}
	if err := tx.QueryRow(ctx, `select exists(select from plan_preferences pp join preferences pref on pref.id=pp.preference_id join members m on m.id=pref.member_id
 where pp.project_id=$1 and pp.plan_version=$2 and (pref.member_id<>$3 or not m.enabled or not pref.enabled or pref.revision<>pp.revision or pref.deleted_at is not null or pref.expires_at<=now()))`, project, version, actor).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return web.Fail(409, "已采用的个人偏好发生变化，请核对后重新保存方案")
	}
	if err := tx.QueryRow(ctx, `select exists(select from plan_rules where project_id=$1 and plan_version=$2 and not rule_usable(rule_id,rule_revision,$3))`, project, version, actor).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return web.Fail(409, "方案采用的团队规则已到期、停用或授权变化，请核对后重新保存方案")
	}
	if err := tx.QueryRow(ctx, `select exists(select from plan_tool_choices c where (c.project_id,c.plan_version)=($1,$2)
 and not agent_admissible(c.alternative,c.alternative_revision,$3))`, project, version, actor).Scan(&missing); err != nil {
		return err
	}
	if missing {
		return web.Fail(409, "候选汇总能力已失效，请重新核对方案")
	}

	err := tx.QueryRow(ctx, `select exists(select from nodes n join agents a on a.id=n.agent_id join agent_revisions r on(r.agent_id,r.revision)=(n.agent_id,n.agent_revision) left join model_resources m on m.id=r.resource_id left join model_revisions v on (v.resource_id,v.revision)=(r.resource_id,r.resource_revision) where n.project_id=$1 and n.plan_version=$2 and (a.disabled or a.emergency or (r.resource_id is not null and (not m.enabled or (v.key_blocked and r.key_override is null))) or not project_agent_access(n.project_id,n.agent_id,n.agent_revision,$3)))`, project, version, actor).Scan(&missing)
	if err != nil {
		return err
	}
	if missing {
		return web.Fail(409, "当前方案的能力或资源已停用或授权已变化")
	}
	return nil
}
