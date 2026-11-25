package monitor

import (
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type qualitySignalInput struct {
	RunID string `json:"runId"`
	Kind  string `json:"kind"`
}

func (s Server) qualitySignal(w http.ResponseWriter, r *http.Request, member identity.Member) error {
	var in qualitySignalInput
	if err := web.ReadLimited(w, r, &in, 4<<10); err != nil {
		return err
	}
	id, err := uuid.Parse(in.RunID)
	if err != nil || id.String() != in.RunID || id == uuid.Nil || !validQualitySignal(in.Kind) {
		return web.Fail(422, "运行编号或反馈类型无效")
	}
	if err := recordQualitySignal(r.Context(), s.DB, in, member.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func validQualitySignal(kind string) bool {
	switch kind {
	case "artifact_downloaded", "reply_copied", "feedback_thumbs_up", "feedback_thumbs_down",
		"followup_deepen", "reply_regenerated", "user_cancelled_midway":
		return true
	default:
		return false
	}
}

func recordQualitySignal(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, in qualitySignalInput, actor string) error {
	var status string
	// Hold the membership and member rows in the same statement as the insert,
	// including for retries after a member loses access.
	return db.QueryRow(ctx, `with authorized as materialized (
	 select r.id,r.status from runs r join project_members pm on pm.project_id=r.project_id
	 join members m on m.id=pm.member_id where r.id=$1 and m.id=$2 and m.enabled and m.kind='person'
	 for share of pm,m
	), saved as (
	 insert into run_quality_signals(run_id,actor,kind) select id,$2,$3 from authorized
	 where $3 <> 'user_cancelled_midway' or status='cancelled'
	 on conflict do nothing
	)
	select status from authorized where $3 <> 'user_cancelled_midway' or status='cancelled'`, in.RunID, actor, in.Kind).Scan(&status)
}
