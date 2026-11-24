package resources

import (
	"alliance/internal/builder"
	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type publicationVersion struct {
	Revision           int       `json:"revision"`
	Name               string    `json:"name"`
	Output             string    `json:"output"`
	PublishedAt        time.Time `json:"publishedAt"`
	Ready              bool      `json:"ready"`
	RequiresModelBatch bool      `json:"requiresModelBatch"`
	PriorRevision      *int      `json:"priorRevision,omitempty"`
	RestoreReason      string    `json:"restoreReason,omitempty"`
}

func (c Catalog) publicationHistory(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	ctx, id := r.Context(), r.PathValue("id")
	result := struct {
		Generation      int64                `json:"generation"`
		CurrentRevision int                  `json:"currentRevision"`
		CurrentRecorded bool                 `json:"currentRecorded"`
		Versions        []publicationVersion `json:"versions"`
	}{Versions: []publicationVersion{}}
	err := pgx.BeginTxFunc(ctx, c.DB, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select a.publication_generation,coalesce(a.published_revision,0),
          exists(select from agent_publications p where (p.agent_id,p.generation,p.revision)=(a.id,a.publication_generation,a.published_revision))
          from agents a join team_members t on t.team_id=a.team_id join members m on m.id=t.member_id
          where a.id=$1 and m.id=$2 and m.enabled and m.role='admin' and not a.removed`, id, u.ID).
			Scan(&result.Generation, &result.CurrentRevision, &result.CurrentRecorded); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select p.revision,r.name,b.definition->>'output',p.published_at,
          agent_admissible($1,p.revision,$2),agent_requires_model_restore($1,p.revision),p.prior_revision,p.restore_reason
          from (select distinct on (revision) * from agent_publications where agent_id=$1 order by revision,generation desc) p
          join agent_revisions r on (r.agent_id,r.revision)=(p.agent_id,p.revision)
          join agent_build_manifests b on (b.agent_id,b.revision,b.digest)=(p.agent_id,p.revision,p.build_digest)
          order by p.generation desc`, id, u.ID)
		if err != nil {
			return err
		}
		result.Versions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[publicationVersion])
		return err
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, result)
}

func (c Catalog) restorePublication(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		builder.Selection
		Generation int64
		Revision   int
		Reason     string
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Generation < 1 || in.Revision < 1 || len(in.BuildDigest) != 64 || len(in.EvidenceDigest) != 64 ||
		in.Reason == "" || utf8.RuneCountInString(in.Reason) > 1000 {
		return web.Fail(422, "需要当前发布记录、目标修订、清单、验收证据和恢复原因")
	}
	ctx, id := r.Context(), r.PathValue("id")
	err := database.WithTx(ctx, c.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
		if err := lockTeamAgent(ctx, tx, u.ID, id, ""); err != nil {
			return err
		}
		var generation int64
		var current int
		var currentRecorded, previous, ready, replay bool
		if err := tx.QueryRow(ctx, `select a.publication_generation,coalesce(a.published_revision,0),
          exists(select from agent_publications p where (p.agent_id,p.generation,p.revision)=(a.id,a.publication_generation,a.published_revision)),
          exists(select from agent_publications p where (p.agent_id,p.revision)=(a.id,$2)),
          agent_admissible(a.id,$2,$3),
          exists(select from agent_publications p where p.agent_id=a.id and p.revision=$2 and p.actor=$3
            and p.restored_from_generation=$4 and p.restore_reason=$5 and p.build_digest=$6 and p.evidence_digest=$7)
          from agents a where a.id=$1`, id, in.Revision, u.ID, in.Generation, in.Reason, in.BuildDigest, in.EvidenceDigest).
			Scan(&generation, &current, &currentRecorded, &previous, &ready, &replay); err != nil {
			return err
		}
		if replay {
			return nil
		}
		if generation != in.Generation || !currentRecorded || current == in.Revision {
			return web.Fail(409, "当前发布版本已变化或缺少对应清单，请重新核对发布历史")
		}
		if !previous || !ready {
			return web.Fail(409, "目标发布修订的清单、资源或证据当前不可用")
		}
		if err := c.publishRevision(ctx, tx, u.ID, id, in.Revision, publicationChange{restoreReason: in.Reason}, in.Selection); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "agent.publish.restore", id, map[string]any{
			"fromRevision": current, "fromGeneration": generation, "revision": in.Revision, "reason": in.Reason,
		})
	})
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
