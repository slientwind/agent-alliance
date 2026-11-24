package resources

import (
	"alliance/internal/builder"
	"alliance/internal/identity"
	"alliance/internal/web"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
)

type releaseSnapshot struct {
	ID             string
	State          string
	BuildDigest    string
	EvidenceDigest string
	BaseGeneration int64
}

func latestRelease(ctx context.Context, q Query, id string, revision int) (releaseSnapshot, error) {
	var r releaseSnapshot
	err := q.QueryRow(ctx, `select id,state,coalesce(build_digest,''),coalesce(evidence_digest,''),coalesce(base_generation,-1)
        from agent_release_requests where (agent_id,revision)=($1,$2)
        order by requested_at desc,id desc limit 1`, id, revision,
	).Scan(&r.ID, &r.State, &r.BuildDigest, &r.EvidenceDigest, &r.BaseGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return r, err
}

func (r releaseSnapshot) matches(build builder.Manifest, evidence builder.Evidence, generation int64) bool {
	return r.BuildDigest == build.Digest && r.EvidenceDigest == evidence.Digest && r.BaseGeneration == generation
}

func (c Catalog) buildManifest(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 32)
	if err != nil || revision < 1 {
		return web.Fail(422, "请选择 Agent 修订")
	}
	ctx, id := r.Context(), r.PathValue("id")
	rev := int(revision)
	result := struct {
		Build                     builder.Manifest  `json:"build"`
		Evidence                  builder.Evidence  `json:"evidence"`
		Review                    json.RawMessage   `json:"review,omitempty"`
		Publication               json.RawMessage   `json:"publication,omitempty"`
		PublishedRemoteOperations *RemoteOperations `json:"publishedRemoteOperations,omitempty"`
	}{}
	err = pgx.BeginTxFunc(ctx, c.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var generation int64
		if err := tx.QueryRow(ctx, `select a.publication_generation from agents a join team_members t on t.team_id=a.team_id
            where a.id=$1 and t.member_id=$2 and not a.removed`, id, u.ID).Scan(&generation); err != nil {
			return err
		}
		build, err := builder.Read(ctx, tx, id, rev)
		if err != nil {
			return err
		}
		evidence, err := builder.EvidenceFor(ctx, tx, id, rev, build.Digest)
		if err != nil {
			return err
		}
		result.Build, result.Evidence = build, evidence
		snapshot, err := latestRelease(ctx, tx, id, rev)
		if err != nil {
			return err
		}
		if snapshot.State == "pending" {
			result.Review, err = json.Marshal(map[string]any{
				"id": snapshot.ID, "legacy": snapshot.BuildDigest == "",
				"stale":       !snapshot.matches(build, evidence, generation),
				"buildDigest": snapshot.BuildDigest, "evidenceDigest": snapshot.EvidenceDigest,
			})
			if err != nil {
				return err
			}
		}
		err = tx.QueryRow(ctx, `select (select jsonb_build_object('generation',generation,'buildDigest',build_digest,
            'evidenceDigest',evidence_digest,'publishedAt',published_at,'priorRevision',prior_revision)
            from agent_publications where (agent_id,revision)=($1,$2) order by generation desc limit 1)`,
			id, rev).Scan(&result.Publication)
		if err == nil && evidence.RemoteOperations != nil {
			result.PublishedRemoteOperations, err = ReadRemoteOperations(ctx, tx, id, rev)
		}
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}
