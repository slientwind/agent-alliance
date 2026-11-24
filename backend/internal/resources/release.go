package resources

import (
	"context"
	"net/http"
	"strings"

	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type releaseRequest struct {
	builder.Selection
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
		previous, err := latestRelease(ctx, tx, id, in.Revision)
		if err != nil {
			return err
		}
		if previous.State != "pending" && in.Previous != previous.ID {
			return web.Fail(409, "发布处理记录已更新，请刷新后重试")
		}
		build, err := builder.Capture(ctx, tx, id, in.Revision)
		if err != nil {
			return err
		}
		proof, err := builder.EvidenceFor(ctx, tx, id, in.Revision, build.Digest)
		if err != nil {
			return err
		}
		if !in.Match(build, proof) {
			return web.Fail(409, "清单或验收证据已变化，请核对后重新申请")
		}
		var generation int64
		if err := tx.QueryRow(ctx, `select publication_generation from agents where id=$1`, id).Scan(&generation); err != nil {
			return err
		}
		if previous.State == "pending" {
			if previous.matches(build, proof, generation) {
				request = previous.ID
				return nil
			}
			if in.Previous != previous.ID {
				return web.Fail(409, "待审清单已变化，请核对并更新发布申请")
			}
		}
		var ready bool
		if err := tx.QueryRow(ctx, "select agent_revision_ready($1,$2)", id, in.Revision).Scan(&ready); err != nil {
			return err
		}
		if !ready || !proof.Passed {
			return web.Fail(409, "请先恢复资源并完成本版必要验证")
		}
		if previous.State == "pending" {
			if _, err := tx.Exec(ctx, "update agent_release_requests set state='superseded',decider=$2,decided_at=now() where id=$1", previous.ID, u.ID); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `insert into agent_release_requests(agent_id,revision,requester,build_digest,evidence_digest,evidence_snapshot,base_generation)
            values ($1,$2,$3,$4,$5,$6,$7) returning id`, id, in.Revision, u.ID, build.Digest, proof.Digest, proof.Snapshot, generation).Scan(&request); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "agent.release.request", id, map[string]any{"request": request, "revision": in.Revision, "build": build.Digest, "evidence": proof.Digest})
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
		builder.Selection
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
			return c.publishRevision(ctx, tx, u.ID, id, in.Revision, false, in.Selection)
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

func (c Catalog) publishRevision(ctx context.Context, tx pgx.Tx, actor, id string, revision int, rollback bool, seen builder.Selection) error {
	if err := guardReplacementPublication(ctx, tx, id, revision, rollback); err != nil {
		return err
	}
	model, _, err := c.Resolve(ctx, tx, actor, id, revision)
	if err != nil {
		return err
	}
	if err := builder.CheckPublished(ctx, tx, id, revision); err != nil {
		return err
	}
	build, err := builder.Capture(ctx, tx, id, revision)
	if err != nil {
		return err
	}
	proof, err := builder.EvidenceFor(ctx, tx, id, revision, build.Digest)
	if err != nil {
		return err
	}
	if !seen.Match(build, proof) {
		return web.Fail(409, "清单或验收证据已变化，请核对后再发布")
	}
	var prior *int
	var generation int64
	if err := tx.QueryRow(ctx, "select published_revision,publication_generation from agents where id=$1", id).Scan(&prior, &generation); err != nil {
		return err
	}
	if model.External {
		var legacyUse bool
		if err := tx.QueryRow(ctx, `select agent_released_remote_operation_evidence($1,$2) is null and (
          exists(select from agents where id=$1 and published_revision=$2) or
          exists(select from agent_publications where (agent_id,revision)=($1,$2)) or
          exists(select from nodes n join runs r on(r.project_id,r.plan_version)=(n.project_id,n.plan_version)
            where (n.agent_id,n.agent_revision)=($1,$2)))`, id, revision).Scan(&legacyUse); err != nil {
			return err
		}
		if legacyUse {
			return web.Fail(409, "此发布记录早于操作验收，请创建并验证新的外部修订")
		}
	}
	request, err := latestRelease(ctx, tx, id, revision)
	if err != nil {
		return err
	}
	if request.State == "pending" && !request.matches(build, proof, generation) {
		return web.Fail(409, "待审清单、验收证据或发布基线已变化，请更新发布申请")
	}
	if !proof.Passed {
		return web.Fail(409, "指定修订的必要验证尚未通过")
	}
	var requestID string
	if request.State == "pending" {
		requestID = request.ID
	}
	if err := tx.QueryRow(ctx, `update agents set published_revision=$1 where id=$2 and not disabled and agent_verified($2,$1)
            returning publication_generation`, revision, id).Scan(&generation); err != nil {
		if err == pgx.ErrNoRows {
			return web.Fail(409, "指定修订的必要验证尚未通过")
		}
		return err
	}
	tag, err := tx.Exec(ctx, `insert into agent_publications(agent_id,generation,revision,build_digest,evidence_digest,evidence_snapshot,prior_revision,request_id,actor)
            values($1,$2,$3,$4,$5,$6,$7,nullif($8,'')::uuid,$9) on conflict do nothing`,
		id, generation, revision, build.Digest, proof.Digest, proof.Snapshot, prior, requestID, actor)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	if requestID != "" {
		if _, err := tx.Exec(ctx, "update agent_release_requests set state='published',decider=$2,decided_at=now() where id=$1", requestID, actor); err != nil {
			return err
		}
	}
	return database.Audit(ctx, tx, actor, "agent.publish", id, map[string]any{"revision": revision, "generation": generation, "build": build.Digest, "evidence": proof.Digest})
}
