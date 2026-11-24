package projects

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

func (s Server) reviseDelivery(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Expected, Edition, Version int
		Artifact, Reason           string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || utf8.RuneCountInString(in.Reason) > 4000 || strings.ContainsRune(in.Reason, 0) || in.Edition < 1 || in.Version < 1 || in.Artifact == "" {
		return web.Fail(422, "请选择已交付的成果版本，并填写修订原因（最多 4000 字）")
	}
	ctx := r.Context()
	var feedback string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		var allowed bool
		if err = tx.QueryRow(ctx, "select can_review from project_members where project_id=$1 and member_id=$2", p.ID, u.ID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return web.Fail(403, "需要本项目的成果审核权限")
		}
		if p.Stage != "delivered" || p.Revision != in.Expected {
			return web.Fail(409, "项目状态已变化，请核对最新交付")
		}
		var base string
		var latest int
		if err = tx.QueryRow(ctx, "select id,edition from deliveries where project_id=$1 order by edition desc limit 1", p.ID).Scan(&base, &latest); err != nil {
			return err
		}
		if latest != in.Edition {
			return web.Fail(409, "请从最近一次交付发起修订")
		}
		var matches bool
		if err = tx.QueryRow(ctx, `select exists(select from delivery_revision_targets($1) i where(i.artifact_id,i.version)=($2,$3))`, base, in.Artifact, in.Version).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return web.Fail(409, "所选成果与已交付版本或批次的待补项不一致")
		}
		feedback, err = createFeedback(ctx, tx, p, in.Artifact, u.ID, in.Version, allowed, "change", "交付后修订", in.Reason)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "insert into delivery_revision_requests(base_delivery,feedback_id,requested_by) values($1,$2,$3)", base, feedback, u.ID); err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, "review"); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "delivery.revise", base, map[string]any{"feedback": feedback, "artifact": in.Artifact, "version": in.Version})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 201, map[string]string{"feedback": feedback})
}
