package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
)

const LookupIndexWaitSQL = `(select jsonb_build_object('step',w.ordinal,'until',w.deadline,'automatic',w.deadline>clock_timestamp())
 from lookup_index_waits w where w.run_id=r.id and w.active and r.status='blocked')`

func (s Server) control(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason string
		Expected       int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 180 || web.Blank(in.Reason) {
		return web.Fail(422, "需要幂等键与操作原因")
	}
	ctx := r.Context()
	id := r.PathValue("id")
	var rev int
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var err error
		rev, err = ControlTx(ctx, tx, id, u.ID, key, in.Action, in.Expected, in.Reason)
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 202, map[string]int{"controlRevision": rev})
}
func ControlTx(ctx context.Context, tx pgx.Tx, id, actor, key, action string, expected int, reason string) (int, error) {
	return controlTx(ctx, tx, id, actor, key, action, expected, reason, false)
}

func controlTx(ctx context.Context, tx pgx.Tx, id, actor, key, action string, expected int, reason string, allowServiceReviewer bool) (int, error) {
	sig := web.Hash(fmt.Sprintf("%s/%s/%d/%s", id, action, expected, reason))
	var rev int
	var state, pid string
	err := tx.QueryRow(ctx, `select r.status,r.control_revision,r.project_id from runs r join projects p on p.id=r.project_id where r.id=$1 and (p.owner=$2 or (r.actor=$2 and exists(select from change_batches where run_id=r.id) and exists(select from project_members m where m.member_id=$2 and m.project_id=p.id and m.can_review))
 or ($3 and r.app_id is not null and r.delegated_by is null and p.final_reviewer=$2 and exists(select from project_members pm join members m on m.id=pm.member_id
 where(pm.project_id,pm.member_id)=(p.id,$2) and pm.can_review and m.enabled and m.kind='person'))) for update of r`, id, actor, allowServiceReviewer).Scan(&state, &rev, &pid)
	if err != nil {
		return 0, err
	}
	var ok bool
	var prev int
	err = tx.QueryRow(ctx, "select signature=$1,revision from controls where actor=$2 and request_key=$3", sig, actor, key).Scan(&ok, &prev)
	if err == nil {
		if !ok {
			return 0, web.Fail(409, "幂等键内容冲突")
		}
		return prev, nil
	}
	if err != pgx.ErrNoRows {
		return 0, err
	}
	if rev != expected {
		return 0, web.Fail(409, "运行控制版本已变化")
	}
	var target string
	switch action {
	case "pause":
		if state != "running" && state != "queued" {
			return 0, web.Fail(409, "当前状态不可暂停")
		}
		target = "paused"
	case "resume":
		var needsReceipt, needsFieldConfirmation bool
		if err := tx.QueryRow(ctx, `select exists(select from calculation_requests cr where run_id=$1
 and not exists(select from lookup_index_waits w where (w.run_id,w.ordinal)=(cr.run_id,cr.ordinal) and w.active)),
 exists(select from pending_column_reconfirmations where run_id=$1)`, id).Scan(&needsReceipt, &needsFieldConfirmation); err != nil {
			return 0, err
		}
		if needsReceipt {
			return 0, web.Fail(409, "请先补充本步骤所需的原件再继续")
		}
		if needsFieldConfirmation {
			return 0, web.Fail(409, "请由当前审核人重新确认原表字段后继续")
		}
		if state != "paused" && state != "blocked" {
			return 0, web.Fail(409, "当前状态需要核对而非重试")
		}
		target = "running"
	case "confirm_failure":
		var confirmable bool
		if err := tx.QueryRow(ctx, "select run_failure_confirmable($1)", id).Scan(&confirmable); err != nil {
			return 0, err
		}
		if !confirmable {
			return 0, web.Fail(409, "需要明确的失败或拒绝回执，且全部已发调用均须核对完成")
		}
		target = "failed"
	case "cancel":
		if state == "completed" || state == "cancelled" || state == "failed" {
			return 0, web.Fail(409, "运行已结束")
		}
		target = "cancelled"
	default:
		return 0, web.Fail(422, "未知控制动作")
	}
	rev++
	if err := ClearLookupIndexGate(ctx, tx, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "update runs set status=$1,control_revision=$2 where id=$3", target, rev, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, "insert into controls(run_id,actor,request_key,signature,action,reason,revision) values($1,$2,$3,$4,$5,$6,$7)", id, actor, key, sig, action, reason, rev); err != nil {
		return 0, err
	}
	if action == "cancel" {
		if _, err := tx.Exec(ctx, `update remote_jobs set phase='checking',deadline=now()+check_window_seconds*interval '1 second',next_at=now(),consecutive_errors=0,detail='remote_cancel_requested' where run_id=$1 and phase<>'finished'`, id); err != nil {
			return 0, err
		}
	}
	if target == "cancelled" || target == "failed" {
		if _, err := tx.Exec(ctx, "update change_batches set disposition='discarded' where run_id=$1 and disposition='pending'", id); err != nil {
			return 0, err
		}
	}
	if err := TouchRun(ctx, tx, id, pid, target); err != nil {
		return 0, err
	}
	return rev, database.Audit(ctx, tx, actor, "run."+action, id, map[string]any{"revision": rev, "reason": reason})
}

// A different blocker or control must take precedence over an automatic wake.
// Only the optional receipt belonging to that index wait is withdrawn.
func ClearLookupIndexGate(ctx context.Context, tx pgx.Tx, run string) error {
	_, err := tx.Exec(ctx, `with cleared as (
 update lookup_index_waits set active=false where run_id=$1 and active returning run_id,ordinal
) delete from calculation_requests cr using cleared w where (cr.run_id,cr.ordinal)=(w.run_id,w.ordinal) and cr.kind='lookup'`, run)
	return err
}
