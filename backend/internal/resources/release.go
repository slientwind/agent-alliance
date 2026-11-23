package resources

import (
	"context"
	"net/http"
	"strings"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type releaseRequest struct {
	Revision int
	Previous string
}

// The agent lock serializes draft creation, duplicate requests and publication.
func (c Catalog) requestRelease(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in releaseRequest
	if err := web.Read(r, &in); err != nil {
		return err
	}
	ctx, id := r.Context(), r.PathValue("id")
	var request string
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := lockTeamAgent(ctx, tx, u.ID, id, ""); err != nil {
			return err
		}
		if err := currentReleaseDraft(ctx, tx, id, in.Revision); err != nil {
			return err
		}
		var state string
		err := tx.QueryRow(ctx, `select id,state from agent_release_requests where agent_id=$1 and revision=$2 order by requested_at desc limit 1`, id, in.Revision).Scan(&request, &state)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		if state == "pending" {
			return nil
		}
		if in.Previous != request {
			return web.Fail(409, "发布处理记录已更新，请刷新后重试")
		}
		var ready bool
		if err := tx.QueryRow(ctx, "select agent_revision_ready($1,$2)", id, in.Revision).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return web.Fail(409, "请先恢复资源并完成本版必要验证")
		}
		if err := tx.QueryRow(ctx, `insert into agent_release_requests(agent_id,revision,requester) values ($1,$2,$3) returning id`, id, in.Revision, u.ID).Scan(&request); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "agent.release.request", id, map[string]any{"request": request, "revision": in.Revision})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]string{"id": request})
}

func currentReleaseDraft(ctx context.Context, tx pgx.Tx, id string, revision int) error {
	var current bool
	err := tx.QueryRow(ctx, `select published_revision is distinct from $2 and $2=(select max(revision) from agent_revisions where agent_id=a.id) from agents a where id=$1`, id, revision).Scan(&current)
	if err != nil {
		return err
	}
	if !current {
		return web.Fail(409, "草稿版本或发布状态已变化，请核对最新版本")
	}
	return nil
}

func (c Catalog) decideRelease(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Revision       int
		Action, Reason string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Action != "publish" && in.Action != "return" || in.Action == "return" && (in.Reason == "" || len(in.Reason) > 4096) {
		return web.Fail(422, "请选择发布或填写退回原因")
	}
	ctx, id, request := r.Context(), r.PathValue("id"), r.PathValue("request")
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := lockTeamAgent(ctx, tx, u.ID, id, ""); err != nil {
			return err
		}
		var state string
		if err := tx.QueryRow(ctx, `select state from agent_release_requests where (id,agent_id,revision)=($1,$2,$3) for update`, request, id, in.Revision).Scan(&state); err != nil {
			return err
		}
		if state != "pending" {
			return web.Fail(409, "此申请已有处理结果")
		}
		if err := currentReleaseDraft(ctx, tx, id, in.Revision); err != nil {
			return err
		}
		if in.Action == "publish" {
			return c.publishRevision(ctx, tx, u.ID, id, in.Revision, false)
		}
		if _, err := tx.Exec(ctx, `update agent_release_requests set state='returned',decider=$2,reason=$3,decided_at=now() where id=$1`, request, u.ID, in.Reason); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "agent.release.return", id, map[string]any{"request": request, "revision": in.Revision})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(204)
	return nil
}
