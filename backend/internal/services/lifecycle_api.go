package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type apiRun struct {
	subject
	Project, Status, Stage string
	Control, Version       int
}

// Read and mutation endpoints share the credential, delegation, team and project gates.
func authorizeRun(ctx context.Context, tx pgx.Tx, r *http.Request) (apiRun, error) {
	sub, err := authorize(ctx, tx, r)
	v := apiRun{subject: sub}
	if err != nil {
		return v, err
	}
	err = tx.QueryRow(ctx, `select p.id,r.status,p.stage,r.control_revision,r.service_version from runs r join projects p on p.id=r.project_id join services s on s.id=r.service_id
 join team_members principal on principal.team_id=s.team_id and principal.member_id=$2 join team_members actor on actor.team_id=s.team_id and actor.member_id=$3 join project_members pm on pm.project_id=r.project_id and pm.member_id=$3
 where r.id=$1 and r.app_id=$2 and r.actor=$3 and r.service_id=$4 and r.delegated_by is not distinct from nullif($5,'')::uuid for share of principal,actor,pm`, r.PathValue("id"), sub.App, sub.Actor, sub.Service, sub.Delegate).Scan(&v.Project, &v.Status, &v.Stage, &v.Control, &v.Version)
	return v, err
}

func (s Server) describe(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var data json.RawMessage
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		sub, err := authorize(ctx, tx, r)
		if err != nil {
			return err
		}
		if sub.Service != r.PathValue("id") {
			return pgx.ErrNoRows
		}
		return tx.QueryRow(ctx, `select jsonb_build_object('id',s.id,'name',s.name,'version',v.version,'instructions',v.instructions,'example',v.example_input,'team',t.name,'reviewer',m.display_name,'allowDocumentAttachments',`+serviceAcceptsDocuments+`,'requiresPictures',`+serviceUsesPictures+`,'editsImage',`+serviceEditsPicture+`,
 'authorizeCalls',(select count(*) from service_nodes where service_id=s.id and version=v.version),
 'ready',m.enabled and exists(select from team_members where team_id=s.team_id and member_id=m.id) and not exists(select from service_nodes n where n.service_id=s.id and n.version=v.version and not agent_admissible(n.agent_id,n.agent_revision,$2))
 and not exists(select from service_tool_choices c where (c.service_id,c.version)=(s.id,v.version) and not agent_admissible(c.alternative,c.alternative_revision,$2)),
 'steps',array(select jsonb_build_object('step',n.ordinal,'name',ar.name,'revision',n.agent_revision,'lookupPhase',n.lookup_phase,'operation',agent_output_operation(ar),'toolChoice',
 (select jsonb_build_object('selector',c.selector,'name',other.name,'revision',other.revision,'primaryConfig',aggregate_scope(ar.tool),'config',aggregate_scope(other.tool)) from service_tool_choices c
 join agent_revisions other on(other.agent_id,other.revision)=(c.alternative,c.alternative_revision)
 where(c.service_id,c.version,c.ordinal)=(n.service_id,n.version,n.ordinal))) from service_nodes n join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) where n.service_id=s.id and n.version=v.version order by n.ordinal))
 from services s join teams t on t.id=s.team_id join service_versions v on(v.service_id,v.version)=(s.id,s.current_version) join members m on m.id=v.reviewer
 join team_members a on a.team_id=s.team_id and a.member_id=$2 join team_members app on app.team_id=s.team_id and app.member_id=$3 where s.id=$1 and s.enabled`, sub.Service, sub.Actor, sub.App).Scan(&data)
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, data)
}

func (s Server) suggestColumns(w http.ResponseWriter, r *http.Request) error {
	return s.Planner.HandleColumnSuggestions(w, r, func(ctx context.Context, tx pgx.Tx) (string, error) {
		run, err := authorizeRun(ctx, tx, r)
		if err != nil {
			return "", err
		}
		if run.Delegate == "" {
			return "", web.Fail(403, "字段建议须由原委托成员或项目网页的审核人授权")
		}
		return run.Actor, nil
	})
}

func (s Server) previewRuleBranch(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	var preview projects.RuleBranchPreview
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		run, err := authorizeRun(ctx, tx, r)
		if err != nil {
			return err
		}
		preview, err = projects.PreviewRuleBranch(ctx, tx, r.PathValue("id"), run.Actor)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, preview)
}

type apiAction struct {
	InputMappings                            []knowledge.ColumnCorrection `json:"inputMappings,omitempty"`
	Attempt                                  int                          `json:"attempt,omitempty"`
	AuthorizeOneCall                         bool                         `json:"authorizeOneCall,omitempty"`
	ExcerptStart                             *int                         `json:"excerptStart,omitempty"`
	Attachment                               *Attachment                  `json:"attachment,omitempty"`
	Excerpt                                  string                       `json:"excerpt,omitempty"`
	ReceiptRevision                          int                          `json:"receiptRevision,omitempty"`
	WaitRevision                             int                          `json:"waitRevision,omitempty"`
	RuleRevision                             int                          `json:"ruleRevision,omitempty"`
	RebuildDigest                            string                       `json:"rebuildDigest,omitempty"`
	AuthorizeCalls                           int                          `json:"authorizeCalls,omitempty"`
	Action, Reason, Body, Artifact, Position string
	Expected, Step, Version                  int
}

func (s Server) action(w http.ResponseWriter, r *http.Request) error {
	var in apiAction
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 160 {
		return web.Fail(422, "请提供有效操作请求编号")
	}
	if utf8.RuneCountInString(in.Body) > 4000 || utf8.RuneCountInString(in.Position) > 1000 || utf8.RuneCountInString(in.Reason) > 1000 {
		return web.Fail(422, "操作说明过长")
	}
	if in.Action != "submit_receipt" && in.Action != "submit_lookup_receipt" && (in.Attachment != nil || in.Excerpt != "" || in.ExcerptStart != nil || in.ReceiptRevision != 0) {
		return web.Fail(422, "请使用补交原件动作提交附件")
	}
	if (in.Action != "recheck_source_index" && in.Action != "recheck_step_rule" && in.WaitRevision != 0) ||
		(in.Action != "recheck_step_rule" && in.RuleRevision != 0) {
		return web.Fail(422, "请使用对应的待办核对动作")
	}
	if in.Action != "rebuild_rule_branch" && (in.RebuildDigest != "" || in.AuthorizeCalls != 0) {
		return web.Fail(422, "重做清单仅用于授权重做分支")
	}
	if in.Action == "correct_columns" {
		if !in.AuthorizeOneCall || len(in.InputMappings) == 0 {
			return web.Fail(422, "请提交字段对应并授权一次重新计算")
		}
		var err error
		in.InputMappings, err = projects.CanonicalColumnCorrections(in.InputMappings)
		if err != nil {
			return err
		}
	} else if in.InputMappings != nil || in.AuthorizeOneCall || in.Attempt != 0 {
		return web.Fail(422, "字段对应和单次授权用于字段补正动作")
	}
	ctx, id := r.Context(), r.PathValue("id")
	payload, _ := json.Marshal(in)
	sig := web.Hash(string(payload))
	var result json.RawMessage
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		v, err := authorizeRun(ctx, tx, r)
		if err != nil {
			return err
		}
		// The worker takes the same run lock before recording its invocation intent.
		var control int
		var status string
		if err := tx.QueryRow(ctx, "select status,control_revision from runs where id=$1 for update", id).Scan(&status, &control); err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, "select signature=$3,result from service_action_receipts where run_id=$1 and request_key=$2", id, key, sig).Scan(&same, &result)
		if err == nil {
			if !same {
				return web.Fail(409, "操作编号已用于不同请求")
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		var out map[string]any
		switch in.Action {
		case "rebuild_rule_branch":
			next, err := projects.RebuildRuleBranch(ctx, tx, id, v.Actor, fmt.Sprintf("api/%s/%x", id, web.Hash(key)),
				projects.RuleBranchInput{Digest: in.RebuildDigest, AuthorizeCalls: in.AuthorizeCalls})
			if err != nil {
				return err
			}
			out = map[string]any{"action": in.Action, "run": next}
		case "recheck_step_rule":
			if err := projects.RecheckStepRule(ctx, tx, id, v.Actor, fmt.Sprintf("api/%s/%x", id, web.Hash(key)),
				projects.RuleRecheckInput{Step: in.Step, Expected: in.Expected, WaitRevision: in.WaitRevision, RuleRevision: in.RuleRevision}); err != nil {
				return err
			}
			out = map[string]any{"action": in.Action, "step": in.Step, "controlRevision": in.Expected}
		case "recheck_source_index":
			if err := projects.RecheckSourceIndex(ctx, tx, id, v.Actor, fmt.Sprintf("api/%s/%x", id, web.Hash(key)),
				projects.IndexRecheckInput{Step: in.Step, Expected: in.Expected, WaitRevision: in.WaitRevision}); err != nil {
				return err
			}
			out = map[string]any{"action": in.Action, "step": in.Step, "controlRevision": in.Expected}
		case "cancel", "pause", "resume", "confirm_failure":
			if web.Blank(in.Reason) {
				return web.Fail(422, "请说明控制原因")
			}
			rev, err := projects.ControlTx(ctx, tx, id, v.Actor, fmt.Sprintf("api/%s/%x", id, web.Hash(key)), in.Action, in.Expected, in.Reason)
			if err != nil {
				return err
			}
			out = map[string]any{"action": in.Action, "controlRevision": rev}
		case "correct_columns":
			if v.Delegate == "" {
				return web.Fail(403, "字段补正须由原委托成员或项目网页的审核人确认")
			}
			revision, err := projects.ReviewCallTx(ctx, tx, id, v.Actor, fmt.Sprintf("api/%s/%x", id, web.Hash(key)), in.Step,
				projects.CallReviewInput{Attempt: in.Attempt, Expected: in.Expected, Decision: "redo", Evidence: in.Reason,
					AuthorizeOneCall: true, InputMappings: in.InputMappings})
			if err != nil {
				return err
			}
			out = map[string]any{"action": in.Action, "step": in.Step, "controlRevision": revision}
		case "submit_receipt", "submit_lookup_receipt":
			if in.Attachment == nil || in.Step < 1 || utf8.RuneCountInString(in.Excerpt) > 320 {
				return web.Fail(422, "请提供待补件步骤、原件以及最多 320 字的原句")
			}
			if err := in.Attachment.Validate(); err != nil {
				return err
			}
			kind := projects.CalculationReceipt
			if in.Action == "submit_lookup_receipt" {
				kind = projects.LookupReceipt
			}
			revision, err := projects.ApplySourceReceipt(ctx, tx, id, v.Actor, fmt.Sprintf("api/%s/%x", id, web.Hash(key)), kind, projects.ReceiptSelection{
				Expected: in.Expected, Step: in.Step, ReceiptRevision: in.ReceiptRevision, Excerpt: in.Excerpt, ExcerptStart: in.ExcerptStart,
			}, &projects.ReceiptFile{Name: in.Attachment.Name, Text: string(in.Attachment.Data)})
			if err != nil {
				return err
			}
			out = map[string]any{"action": in.Action, "step": in.Step, "controlRevision": revision}
		case "propose_revision":
			var linked bool
			if err := tx.QueryRow(ctx, "select exists(select from artifacts where id=$1 and run_id=$2)", in.Artifact, id).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return pgx.ErrNoRows
			}
			feedback, err := projects.ProposeRevision(ctx, tx, v.Project, in.Artifact, v.Actor, in.Version, in.Position, in.Body)
			if err != nil {
				return err
			}
			out = map[string]any{"feedback": feedback, "state": "suggested", "version": in.Version}
		case "supplement":
			if web.Blank(in.Body) || in.Step < 1 {
				return web.Fail(422, "请指定步骤并填写补充信息")
			}
			if control != in.Expected || (status != "queued" && status != "running" && status != "paused" && status != "blocked") {
				return web.Fail(409, "请重新核对运行状态与控制版本")
			}
			var acceptable bool
			if err := tx.QueryRow(ctx, `select exists(select from runs r join nodes n on(n.project_id,n.plan_version)=(r.project_id,r.plan_version) join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision) where r.id=$1 and n.ordinal=$2 and ar.tool is null and not exists(select from invocations where run_id=r.id and ordinal=n.ordinal) and not exists(select from run_candidate_artifacts where run_id=r.id and ordinal=n.ordinal) and not exists(select from rule_branch_steps b where b.run_id=r.id and b.ordinal=n.ordinal and b.finished)) and (select count(*)<8 from service_supplements where run_id=$1 and ordinal=$2)`, id, in.Step).Scan(&acceptable); err != nil {
				return err
			}
			if !acceptable {
				return web.Fail(409, "该步骤已提交、不接收文本补充或已达到八条上限")
			}
			control++
			var seq int64
			if err = tx.QueryRow(ctx, "insert into messages(project_id,author,kind,body) values($1,$2,'user',$3) returning seq", v.Project, v.Actor, fmt.Sprintf("第 %d 步补充：%s", in.Step, in.Body)).Scan(&seq); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "insert into service_supplements values($1,$2,$3,$4)", id, in.Step, control, seq); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "update runs set control_revision=$2 where id=$1", id, control); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "select record_service_event($1,'step','supplemented',$2)", id, in.Step); err != nil {
				return err
			}
			out = map[string]any{"step": in.Step, "state": "recorded", "controlRevision": control}
		default:
			return web.Fail(422, "不支持的运行动作")
		}
		result, err = json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into service_action_receipts values($1,$2,$3,$4)", id, key, sig, result)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (s Server) events(w http.ResponseWriter, r *http.Request) error {
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	var after int64
	var err error
	if cursor != "" {
		after, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || after < 0 {
			return web.Fail(422, "事件游标无效")
		}
	}
	stream := strings.Contains(r.Header.Get("Accept"), "text/event-stream")
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	sentHeaders := false
	for {
		var events []json.RawMessage
		err = database.WithTx(r.Context(), s.DB, func(tx pgx.Tx) error {
			if _, err := authorizeRun(r.Context(), tx, r); err != nil {
				return err
			}
			rows, err := tx.Query(r.Context(), `select jsonb_strip_nulls(jsonb_build_object('seq',seq,'kind',kind,'state',state,'step',ordinal,'artifact',artifact_id,'version',version,'at',at)) from service_events where run_id=$1 and seq>$2 order by seq limit 100`, r.PathValue("id"), after)
			if err != nil {
				return err
			}
			events, err = pgx.CollectRows(rows, pgx.RowTo[json.RawMessage])
			return err
		})
		if err != nil {
			if sentHeaders {
				return nil
			}
			return err
		}
		if !stream {
			return web.JSON(w, 200, map[string]any{"events": events})
		}
		if !sentHeaders {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			sentHeaders = true
		}
		for _, e := range events {
			var record struct{ Seq int64 }
			if err := json.Unmarshal(e, &record); err != nil {
				return nil
			}
			if _, err := fmt.Fprintf(w, "id: %d\nevent: update\ndata: %s\n\n", record.Seq, e); err != nil {
				return nil
			}
			after = record.Seq
		}
		if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
			return nil
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			return nil
		}
		if len(events) == 100 {
			continue
		}
		select {
		case <-r.Context().Done():
			return nil
		case <-deadline.C:
			return nil
		case <-time.After(time.Second):
		}
	}
}

var progressSQL = `select jsonb_build_object(
 'id',r.id,'project',p.id,'service',r.service_id,'version',r.service_version,'status',r.status,'stage',p.stage,'controlRevision',r.control_revision,'canConcludeFailure',run_failure_confirmable(r.id),
 'deliveries',array(select jsonb_build_object('edition',d.edition,'id',d.id,'isPartial',d.partial,'distribution',distribution_state(d.id),'deliveryUrl','/api/v1/runs/'||r.id||'/delivery?edition='||d.edition,'packageUrl','/api/v1/runs/'||r.id||'/delivery/package?edition='||d.edition) from deliveries d where d.project_id=p.id order by d.edition desc),
 'humanActionUrl','/#'||p.id::text,'deliveryUrl','/api/v1/runs/'||r.id||'/delivery'||urls.suffix,'packageUrl','/api/v1/runs/'||r.id||'/delivery/package'||urls.suffix,
 'eventsUrl','/api/v1/runs/'||r.id||'/events','actionsUrl','/api/v1/runs/'||r.id||'/actions',
 'nextAction',case when exists(select from rule_branch_rebuilds b where b.source_run=r.id) then 'read_replacement_run'
 when exists(select from step_rule_waits w where w.run_id=r.id and w.rebuild_required and r.status='running')
   or p.stage='review' and r.status='completed' and exists(select from run_candidate_artifacts a where a.run_id=r.id and not artifact_rule_draft_readable(a.id,a.current_version))
   then 'preview_rule_branch'
 when p.stage='delivered' and (select distribution_state(id) from deliveries where project_id=p.id order by edition desc limit 1) not in ('clear','reference') then 'check_distribution' when p.stage='review' and candidate_distribution_held(p.id) then 'check_distribution'
 when p.stage='review' and exists(select from plan_delivery_contracts c where(c.project_id,c.plan_version)=(p.id,p.plan_version))
   then case when exists(select from review_actions a where a.project_id=p.id and a.kind='deliver') then 'confirm_delivery' else 'review_artifacts' end
 when p.stage='review' then case when exists(select from run_candidate_artifacts a where a.run_id=r.id and (source_check_needed(a.id) or takeover_review_needed(p.id,a.id,a.current_version) or not exists(select from reviews v where(v.artifact_id,v.version)=(a.id,a.current_version)) or exists(select from feedback f where f.artifact_id=a.id and f.state in ('required','investigate','verification')))) then 'review_artifacts' else 'confirm_delivery' end
 when r.status='blocked' and exists(select from run_tool_choices choice where choice.run_id=r.id and choice.choice=0 and choice.manual_choice is null) then 'select_tool'
 when r.status='running' and exists(select from step_rule_waits w where w.run_id=r.id) then 'check_step_rule'
 when r.status='blocked' and exists(select from lookup_index_waits w where w.run_id=r.id and w.active and w.step_wait_revision is null) then
   (select case when w.deadline>clock_timestamp() then 'wait_source_index' else 'resume' end
      from lookup_index_waits w where w.run_id=r.id and w.active and w.step_wait_revision is null)
 when r.status='running' and exists(select from lookup_index_waits w where (w.run_id,w.plan_version)=(r.id,r.plan_version)
      and w.active and w.step_wait_revision is not null and w.deadline<=statement_timestamp()) then 'recheck_source_index'
 when exists(select from current_source_receipt_requests cr where cr.run_id=r.id) then
   (select case when cr.kind='lookup' then 'submit_lookup_receipt' else 'submit_receipt' end from current_source_receipt_requests cr where cr.run_id=r.id order by cr.ordinal limit 1)
 when jsonb_array_length(repair.requests)>0 then 'correct_columns'
 when repair.needs_human then 'review_columns'
 when r.status='running' and exists(select from lookup_index_waits w where (w.run_id,w.plan_version)=(r.id,r.plan_version)
      and w.active and w.step_wait_revision is not null) then 'wait_source_index'
 when run_failure_confirmable(r.id) then 'review_failure_receipt' when r.status='blocked' then 'restore_prerequisite' when r.status='reconcile' then 'check_original_receipt' when r.status='paused' then 'resume_or_cancel' when p.stage='delivered' then 'download' end,
 'columnCorrections',repair.requests,
 'receiptRequests',` + projects.SourceReceiptRequestsSQL + `,
 'receiptRequest',(select jsonb_strip_nulls(jsonb_build_object('step',cr.ordinal,'reason',cr.reason,'kind',cr.kind,'receiptRevision',cr.request_revision)) from current_source_receipt_requests cr where cr.run_id=r.id order by cr.ordinal limit 1),
 'indexWait',` + projects.LookupIndexWaitSQL + `,
 'indexWaits',` + projects.StepIndexWaitsSQL + `,
 'ruleWaits',` + projects.StepRuleWaitsSQL("$2") + `,
 'ruleRebuildUrl','/api/v1/runs/'||r.id||'/rule-rebuild',
 'replacementRun',(select b.run_id from rule_branch_rebuilds b where b.source_run=r.id),
 'finalReviewer',reviewer.display_name,
 'steps',array(select jsonb_strip_nulls(jsonb_build_object('step',n.ordinal,'name',ar.name,'revision',n.agent_revision,'lookupPhase',nullif(n.lookup_phase,''),'toolChoice',n.choice,'status',case when branch.mode='reuse' then 'reused'
   when o.payload is not null then 'checking_observation'
   when t.action_count>1 then 'refining_evidence'
   when n.lookup_phase in ('review_evidence','synthesize') and evidence_follow_up_skipped(r.id,n.ordinal) then 'skipped' else coalesce(i.status,case when n.lookup_phase in ('answer','answer_calc') and a.id is not null then 'skipped' else 'pending' end) end,
 'lookupBatch',case when t.action_count>1 then jsonb_build_object('current',t.action_cursor,'total',t.action_count) end,
 'canSupplement',r.status in('queued','running','blocked','paused') and i.run_id is null and a.id is null and not coalesce(branch.finished,false) and ar.tool is null and (select count(*) from service_supplements x where(x.run_id,x.ordinal)=(r.id,n.ordinal))<8,
 'artifact',a.id,'version',a.current_version,'needsSourceReview',case when a.id is not null then source_check_needed(a.id) end,'needsTakeoverReview',case when a.id is not null then takeover_review_needed(p.id,a.id,a.current_version) end,'approved',case when a.id is not null then exists(select from reviews rv where(rv.artifact_id,rv.version)=(a.id,a.current_version)) end))
 from effective_run_nodes n join agent_revisions ar on(ar.agent_id,ar.revision)=(n.agent_id,n.agent_revision)
 left join rule_branch_steps branch on(branch.run_id,branch.ordinal)=(r.id,n.ordinal)
 left join invocations i on(i.run_id,i.ordinal)=(r.id,n.ordinal) left join run_candidate_artifacts a on(a.run_id,a.ordinal)=(r.id,n.ordinal)
 left join lookup_turns t on (t.run_id,t.ordinal,t.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
   and r.status='running' and t.decision is not null and run_working_set_usable(t.run_id,t.ordinal,t.attempt_no)
 left join local_action_observations o on (o.run_id,o.ordinal,o.attempt_no,o.action_round)=(t.run_id,t.ordinal,t.attempt_no,t.action_cursor)
 where n.run_id=r.id order by n.ordinal),
 'blocker',case when r.status in('blocked','reconcile') then (select jsonb_build_object('step',e.ordinal,'reason',e.code) from trace_events e where e.run_id=r.id and e.kind='checkpoint' order by e.seq desc limit 1) end,
 'externalTasks',array(select jsonb_build_object('step',j.ordinal,'state',i.remote_status,'phase',j.phase,'detail',j.detail,'deadline',j.deadline,'cancelRequested',j.cancel_attempted) from remote_jobs j join invocations i using(run_id,ordinal) where j.run_id=r.id order by j.ordinal))
 from runs r join projects p on p.id=r.project_id join members reviewer on reviewer.id=p.final_reviewer
 cross join lateral (select coalesce('?edition='||max(d.edition) filter(where d.edition>1),'') as suffix from deliveries d where d.project_id=p.id) urls
 cross join lateral (
 SELECT coalesce(jsonb_agg(jsonb_strip_nulls(jsonb_build_object(
    'step',i.ordinal,'attempt',i.attempt_no,'files',issues.files,'reconfirmationRequired',pending.control_id is not null,
    'suggestionUrl',case when exists(select from column_suggestion_targets t where (t.run_id,t.ordinal,t.attempt_no,t.actor)=(r.id,i.ordinal,i.attempt_no,$2))
      then '/api/v1/runs/'||r.id||'/calls/'||i.ordinal||'/column-suggestion' end,
    'suggestion',(select jsonb_build_object('status',s.current_status,'entries',case when s.current_status='ready' then s.entries end)
      from current_column_suggestions s where (s.run_id,s.ordinal,s.attempt_no,s.actor)=(r.id,i.ordinal,i.attempt_no,$2))
 )) order by i.ordinal) filter(where issues.files is not null),'[]') requests,
 (r.delegated_by is null and count(*)>0) needs_human
 FROM invocations i LEFT JOIN pending_column_reconfirmations pending
 ON (pending.run_id,pending.ordinal,pending.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
 CROSS JOIN LATERAL (select case when r.delegated_by is not null then visible_column_issues(r.id,$2,i.input_issue) end as files) issues
 WHERE i.run_id=r.id AND i.status='input_rejected' AND i.input_issue IS NOT NULL AND (
 pending.control_id IS NOT NULL OR (r.status='reconcile'
  AND NOT EXISTS(select from active_call_redos v where (v.run_id,v.ordinal,v.attempt_no)=(r.id,i.ordinal,i.attempt_no))))
 ) repair
 where r.id=$1`
