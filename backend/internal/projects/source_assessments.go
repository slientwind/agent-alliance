package projects

import (
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"unicode/utf8"
)

func (s Server) assessSource(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Version, Pinned, Observed int
		Revoked                   bool
		Decision, Reason          string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if (in.Decision != "retain" && in.Decision != "change") || in.Reason == "" || utf8.RuneCountInString(in.Reason) > 4000 {
		return web.Fail(422, "请明确处理方式并填写核对依据，最多 4000 字")
	}
	ctx := r.Context()
	artifact, src := r.PathValue("artifact"), r.PathValue("source")
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		p, err := lock(ctx, tx, r.PathValue("id"), u.ID)
		if err != nil {
			return err
		}
		if p.Stage != "review" {
			return web.Fail(409, "项目已离开审核阶段")
		}
		var can bool
		if err = tx.QueryRow(ctx, "select can_review from project_members where project_id=$1 and member_id=$2", p.ID, u.ID).Scan(&can); err != nil {
			return err
		}
		if !can {
			return web.Fail(403, "需要本项目的审核权限")
		}
		var current, pin, observed int
		var revoked bool
		var decision *string
		if err = tx.QueryRow(ctx, `select artifact_version,pinned,observed,revoked,decision from changed_artifact_sources where project_id=$1 and artifact_id=$2 and source_id=$3`, p.ID, artifact, src).Scan(&current, &pin, &observed, &revoked, &decision); err != nil {
			return err
		}
		if decision != nil || current != in.Version || pin != in.Pinned || observed != in.Observed || revoked != in.Revoked {
			return web.Fail(409, "资料、成果版本或核对记录已变化，请重新查看")
		}
		var feedback *string
		if in.Decision == "change" {
			id, err := createFeedback(ctx, tx, p, artifact, u.ID, current, can, "change", "资料依据核对", in.Reason)
			if err != nil {
				return err
			}
			feedback = &id
		}
		var id string
		if err = tx.QueryRow(ctx, `insert into source_assessments(artifact_id,artifact_version,source_id,pinned_version,observed_version,revoked,decision,reason,actor,feedback_id)
 values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) returning id`, artifact, current, src, pin, observed, revoked, in.Decision, in.Reason, u.ID, feedback).Scan(&id); err != nil {
			return err
		}
		if err = Touch(ctx, tx, p.ID, p.Stage); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `select record_service_event(run_id,'artifact',$2,ordinal,id,current_version) from artifacts where id=$1`, artifact, "source_"+in.Decision); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "source.assess", id, map[string]any{"source": src, "artifact": artifact, "decision": in.Decision, "version": current})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
