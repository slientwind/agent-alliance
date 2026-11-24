package projects

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/knowledge"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type CallReviewInput struct {
	Attempt, Expected  int
	Decision, Evidence string
	AuthorizeOneCall   bool
	InputMapping       *knowledge.ColumnCorrection
	InputMappings      []knowledge.ColumnCorrection
}

func (s Server) reviewCall(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in CallReviewInput
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ordinal, err := strconv.Atoi(r.PathValue("step"))
	if err != nil {
		return web.Fail(422, "请选择有效步骤")
	}
	ctx, run, key := r.Context(), r.PathValue("id"), r.Header.Get("Idempotency-Key")
	var revision int
	err = database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		revision, err = ReviewCallTx(ctx, tx, run, u.ID, key, ordinal, in)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]int{"controlRevision": revision})
}

// Canonicalize the same authorized choices regardless of file order.
func CanonicalColumnCorrections(corrections []knowledge.ColumnCorrection) ([]knowledge.ColumnCorrection, error) {
	if corrections == nil {
		return nil, nil
	}
	if len(corrections) < 1 || len(corrections) > 20 {
		return nil, web.Fail(422, "请选择 1 至 20 份待核对资料")
	}
	corrections = slices.Clone(corrections)
	slices.SortFunc(corrections, func(a, b knowledge.ColumnCorrection) int { return strings.Compare(a.Source, b.Source) })
	for i := 1; i < len(corrections); i++ {
		if corrections[i].Source == corrections[i-1].Source {
			return nil, web.Fail(422, "每份资料只能提交一组字段对应")
		}
	}
	return corrections, nil
}

// ReviewCallTx shares the operation and ledger between browser and delegated
// service decisions. The service transport authenticates the run owner first.
func ReviewCallTx(ctx context.Context, tx pgx.Tx, run, actor, key string, ordinal int, in CallReviewInput) (int, error) {
	if ordinal < 1 || ordinal > 12 || in.Attempt < 1 || in.Expected < 1 || key == "" || len(key) > 180 || web.Blank(in.Evidence) || utf8.RuneCountInString(in.Evidence) > 1600 {
		return 0, web.Fail(422, "请选择调用批次，填写核对依据（最多 1600 字）并提供幂等键")
	}
	if (in.Decision != "hold" && in.Decision != "redo") || in.AuthorizeOneCall != (in.Decision == "redo") {
		return 0, web.Fail(422, "重新执行需要明确承担重复执行风险并授权一次新调用")
	}
	corrections := in.InputMappings
	if in.InputMapping != nil {
		if corrections != nil {
			return 0, web.Fail(422, "请选择一种字段补正提交格式")
		}
		corrections = []knowledge.ColumnCorrection{*in.InputMapping}
	}
	if corrections != nil && (in.Decision != "redo" || len(corrections) < 1 || len(corrections) > 20) {
		return 0, web.Fail(422, "字段补正需要授权一次重新计算")
	}
	corrections, err := CanonicalColumnCorrections(corrections)
	if err != nil {
		return 0, err
	}
	document := knowledge.ColumnDocument(corrections)
	signature := web.Hash(fmt.Sprintf("call-review/%s/%d/%d/%d/%s/%q", run, ordinal, in.Attempt, in.Expected, in.Decision, in.Evidence))
	if document != nil {
		mapping, err := json.Marshal(document)
		if err != nil {
			return 0, err
		}
		signature = web.Hash(fmt.Sprintf("%x/%s", signature, mapping))
	}
	var revision, attempt int
	var state, status, project string
	var remote, activeAuthority, overdue, expired bool
	var redoControl *string
	var priorCorrection bool
	var executionDue, receiptDue *time.Time
	correcting := document != nil
	if err := tx.QueryRow(ctx, `select r.control_revision,r.status,r.project_id from runs r where r.id=$1
 and case when $3 then exists(select from column_correction_authority a where (a.run_id,a.member_id)=(r.id,$2))
 else exists(select from call_review_authority a where (a.run_id,a.member_id)=(r.id,$2)) end for update of r`,
		run, actor, correcting).Scan(&revision, &state, &project); err != nil {
		return 0, err
	}
	if correcting {
		if _, err := tx.Exec(ctx, "select id from projects where id=$1 for update", project); err != nil {
			return 0, err
		}
		var allowed, granted bool
		if err := tx.QueryRow(ctx, "select exists(select from column_correction_authority where run_id=$1 and member_id=$2)", run, actor).Scan(&allowed); err != nil {
			return 0, err
		}
		if !allowed {
			return 0, web.Fail(403, "字段补正职责已变化")
		}
		if err := tx.QueryRow(ctx, identity.RunGrantSQL, run).Scan(&granted); err != nil {
			return 0, err
		}
		if !granted {
			return 0, web.Fail(403, "原运行的服务授权已变化")
		}
	}
	if err := tx.QueryRow(ctx, `select i.attempt_no,i.status,w.execution_due,w.receipt_due,coalesce(w.execution_due<=clock_timestamp(),false),coalesce(w.receipt_due<=clock_timestamp(),false),
  exists(select from remote_jobs j where(j.run_id,j.ordinal)=(i.run_id,i.ordinal)) or i.remote_task is not null,
  v.control_id,v.input_mapping is not null,coalesce(v.authority_current,false)
  from invocations i left join submission_windows w using(run_id,ordinal)
  left join active_call_redos v on (v.run_id,v.ordinal,v.attempt_no)=(i.run_id,i.ordinal,i.attempt_no)
  where i.run_id=$1 and i.ordinal=$2`, run, ordinal).Scan(&attempt, &status, &executionDue, &receiptDue, &overdue, &expired,
		&remote, &redoControl, &priorCorrection, &activeAuthority); err != nil {
		return 0, err
	}
	var same bool
	var prior int
	err = tx.QueryRow(ctx, "select signature=$1,revision from controls where actor=$2 and request_key=$3", signature, actor, key).Scan(&same, &prior)
	if err == nil {
		if !same {
			return 0, web.Fail(409, "幂等键已用于其他决定")
		}
		return prior, nil
	}
	if err != pgx.ErrNoRows {
		return 0, err
	}
	if correcting && status != "input_rejected" {
		return 0, web.Fail(409, "本次调用没有可补正的字段回执")
	}
	reconfirm := correcting && priorCorrection && !activeAuthority &&
		(state == "running" || state == "reconcile" || state == "blocked" || state == "paused")
	reviewable := state == "reconcile" || (state == "running" && overdue) || reconfirm
	if revision != in.Expected || attempt != in.Attempt || !reviewable || remote || (redoControl != nil && !reconfirm) ||
		(!correcting && status != "submitted" && status != "unknown") {
		return 0, web.Fail(409, "调用状态或核对记录已变化，请重新查看")
	}
	if in.Decision == "redo" && !correcting && status != "unknown" && !expired {
		return 0, web.Fail(409, "尚在核对时限内，请等待原回执或取消后续执行")
	}
	if correcting {
		if err := knowledge.ValidateColumnCorrections(ctx, tx, project, run, ordinal, corrections); err != nil {
			return 0, err
		}
	}
	var control string
	err = tx.QueryRow(ctx, `insert into controls(run_id,actor,request_key,signature,action,reason,revision,sent_at)
          values($1,$2,$3,$4,$5,$6,$7,now()) returning id`, run, actor, key, signature, "call."+in.Decision, in.Evidence, revision+1).Scan(&control)
	if err != nil {
		return 0, err
	}
	if reconfirm {
		if _, err := tx.Exec(ctx, "update call_reviews set superseded_by=$2 where control_id=$1", *redoControl, control); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, "insert into call_reviews(control_id,run_id,ordinal,attempt_no,decision,submission_timed_out,execution_due,receipt_due,input_mapping) values($1,$2,$3,$4,$5,$6,$7,$8,$9)", control, run, ordinal, attempt, in.Decision, status == "submitted" && expired, executionDue, receiptDue, document); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "update runs set control_revision=control_revision+1 where id=$1", run); err != nil {
		return 0, err
	}
	if state == "running" || (reconfirm && state == "blocked") {
		if _, err = tx.Exec(ctx, "update runs set status='reconcile' where id=$1", run); err != nil {
			return 0, err
		}
		if err = TouchRun(ctx, tx, run, project, "reconcile"); err != nil {
			return 0, err
		}
	}
	if in.Decision == "redo" {
		if status == "submitted" {
			if _, err = tx.Exec(ctx, "update invocations set status='unknown',ended_at=clock_timestamp(),diagnostic='超过核对时限仍未取得回执' where run_id=$1 and ordinal=$2", run, ordinal); err != nil {
				return 0, err
			}
		}
		if err = ReleaseReviewedCalls(ctx, tx, run); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, "insert into trace_events(run_id,ordinal,kind,code) values($1,$2,'call_review',$3)", run, ordinal, "call_"+in.Decision); err != nil {
		return 0, err
	}
	details := map[string]any{"step": ordinal, "attempt": attempt, "control": control}
	if correcting {
		event := "columns_confirmed"
		if reconfirm {
			event = "columns_reconfirmed"
			details["previousControl"] = *redoControl
		}
		if _, err := tx.Exec(ctx, "select record_service_event($1,'step',$2,$3)", run, event, ordinal); err != nil {
			return 0, err
		}
	}
	return revision + 1, database.Audit(ctx, tx, actor, "call."+in.Decision, run, details)
}

// A workflow wakes its ready branches together. Keep each uncertain branch
// parked until the person responsible has addressed every outstanding result.
func ReleaseReviewedCalls(ctx context.Context, tx pgx.Tx, run string) error {
	var project string
	err := tx.QueryRow(ctx, `update runs r set status='running' where r.id=$1 and r.status='reconcile' and exists(select from invocations i join active_call_redos v using(run_id,ordinal,attempt_no) where i.run_id=r.id and (v.input_mapping is null or v.authority_current) and i.status in ('unknown','input_rejected')) and not exists(
     select from invocations i left join remote_jobs j using(run_id,ordinal)
     where i.run_id=r.id and i.status in ('unknown','submitted','remote','remote_closed','input_rejected')
       and not (i.status='remote' and remote_step_input_parked(j.run_id,j.ordinal,j.phase,j.detail)) and not exists(
       select from active_call_redos v where(v.run_id,v.ordinal,v.attempt_no)=(i.run_id,i.ordinal,i.attempt_no) and (v.input_mapping is null or v.authority_current))) returning project_id`, run).Scan(&project)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "update controls set sent_at=null where id=(select c.id from controls c join active_call_redos v on v.control_id=c.id where v.run_id=$1 and (v.input_mapping is null or v.authority_current) order by c.revision desc limit 1)", run); err != nil {
		return err
	}
	return TouchRun(ctx, tx, run, project, "running")
}

// Provider identifiers are limited to members who can investigate this call.
func (s Server) lateReceipt(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ordinal, e := strconv.Atoi(r.PathValue("step"))
	attempt, err := strconv.Atoi(r.PathValue("attempt"))
	if e != nil || err != nil || ordinal < 1 || ordinal > 12 || attempt < 1 {
		return web.Fail(422, "需要有效的步骤和调用批次")
	}
	var result json.RawMessage
	err = s.DB.QueryRow(r.Context(), `select jsonb_build_object('attempt',l.attempt_no,'time',l.received_at,'outcome',l.outcome,
      'bodyDigest',l.body_sha256,'fileDigest',l.file_sha256,'remoteTask',l.remote_task,'remoteState',l.remote_state,
      'tokens',case when p.owner=$2 or pm.can_view_costs then l.tokens end)
      from late_call_receipts l join call_review_authority c on c.run_id=l.run_id and c.member_id=$2
      join runs r on r.id=l.run_id join projects p on p.id=r.project_id join project_members pm on(pm.project_id,pm.member_id)=(p.id,$2)
      where l.run_id=$1 and l.ordinal=$3 and l.attempt_no=$4`, r.PathValue("id"), u.ID, ordinal, attempt).Scan(&result)
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	return web.JSON(w, 200, result)
}
