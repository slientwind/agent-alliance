package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s Server) sourceLifecycle(w http.ResponseWriter, r *http.Request, u identity.Member) error {
	var in struct {
		Action, Reason           string
		Version, ExpectedProject int
	}
	if err := web.Read(r, &in); err != nil {
		return err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || utf8.RuneCountInString(in.Reason) > 500 || strings.ContainsRune(in.Reason, 0) || in.Version < 1 || in.ExpectedProject < 1 {
		return web.Fail(422, "请核对资料版本、引用影响并填写处理原因")
	}
	ctx := r.Context()
	project, id := r.PathValue("id"), r.PathValue("source")
	var restored *string
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		var owner, author string
		var revision, version int
		var recycled, purged, withinRecovery bool
		if err := tx.QueryRow(ctx, `select p.owner,p.revision from projects p join project_members m on m.project_id=p.id join members u on u.id=m.member_id where p.id=$1 and u.id=$2 and u.enabled for update of p for share of m,u`, project, u.ID).Scan(&owner, &revision); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select created_by,current_version,recycled_at is not null,purged_at is not null,coalesce(purge_due>clock_timestamp(),false),recovered_as from sources where project_id=$1 and id=$2 for update`, project, id).Scan(&author, &version, &recycled, &purged, &withinRecovery, &restored); err != nil {
			return err
		}
		if u.ID != author && u.ID != owner {
			return web.Fail(403, "需要资料负责人或项目发起人权限")
		}
		if in.Action == "purge" && purged || in.Action == "recycle" && recycled || in.Action == "restore" && restored != nil {
			return nil
		}
		if in.Version != version || in.ExpectedProject != revision {
			return web.Fail(409, "资料版本或引用已变化，请重新核对")
		}
		switch in.Action {
		case "recycle":
			if _, err := tx.Exec(ctx, `update sources set originally_revoked=revoked_at is not null, revoked_at=coalesce(revoked_at,now()),recycled_at=now(),purge_due=now()+interval '30 days' where id=$1`, id); err != nil {
				return err
			}
			// Keep earlier decisions as history and require a fresh decision for deletion.
			if _, err := tx.Exec(ctx, `insert into distribution_reviews(source_id,artifact_id,version,revision,actor,decision,reason)
    select source_id,artifact_id,version,max(revision)+1,$2,'pending','资料移入回收区，需重新核对分发' from distribution_reviews where source_id=$1 group by source_id,artifact_id,version`, id, u.ID); err != nil {
				return err
			}
		case "restore":
			if purged || !withinRecovery {
				return web.Fail(409, "可恢复期限已结束")
			}
			if err := s.Objects.verifySourceOriginals(ctx, tx, id); err != nil {
				return err
			}
			var newID string
			if err := tx.QueryRow(ctx, `insert into sources(project_id,name,current_version,created_by,revoked_at)
    select project_id,name,current_version,created_by,case when originally_revoked then revoked_at end from sources where id=$1 returning id`, id).Scan(&newID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `insert into source_versions(source_id,version,filename,object_hash,media_type,extracted,embedding,author,created_at,extraction_state,extraction_locations)
    select $2,version,filename,object_hash,media_type,extracted,embedding,author,created_at,extraction_state,extraction_locations from source_versions where source_id=$1`, id, newID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "update sources set recovered_as=$2 where id=$1", id, newID); err != nil {
				return err
			}
			restored = &newID
			if _, err := tx.Exec(ctx, "select purge_source_content($1)", id); err != nil {
				return err
			}
		case "purge":
			if !recycled {
				return web.Fail(409, "请先核对引用并将资料移入回收区")
			}
			if _, err := tx.Exec(ctx, "select purge_source_content($1)", id); err != nil {
				return err
			}
		default:
			return web.Fail(422, "未知的资料处理方式")
		}
		if err := recordSourceChange(ctx, tx, project, id, "source_"+in.Action); err != nil {
			return err
		}
		return database.Audit(ctx, tx, u.ID, "source."+in.Action, id, map[string]any{"reason": in.Reason, "version": version, "recoveredAs": restored})
	})
	if err != nil {
		return err
	}
	return web.JSON(w, 200, map[string]any{"recoveredAs": restored})
}

// Restore verifies every distinct original before publishing the new identifier.
func (o Objects) verifySourceOriginals(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := tx.Query(ctx, `select distinct o.hash,o.size_bytes from source_versions v left join objects o on o.hash=v.object_hash where v.source_id=$1`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var hash *string
		var size *int64
		if err := rows.Scan(&hash, &size); err != nil {
			return err
		}
		if hash == nil || size == nil {
			return web.Fail(503, "资料原件记录缺失，请联系运维修复后再恢复")
		}
		f, err := o.Open(*hash)
		if err != nil {
			return web.Fail(503, "资料原件暂时不可读取，请修复后再恢复")
		}
		digest := sha256.New()
		n, err := io.Copy(digest, io.LimitReader(f, *size+1))
		f.Close()
		if err != nil || n != *size || hex.EncodeToString(digest.Sum(nil)) != *hash {
			return web.Fail(503, "资料原件校验失败，请修复后再恢复")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CleanSources commits the content tombstone before attempting to reclaim bytes.
// The durable queue survives worker restarts and temporary filesystem failures.
func (o Objects) CleanSources(ctx context.Context, db *pgxpool.Pool) error {
	for {
		var count int
		err := database.WithTx(ctx, db, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `with due as materialized(select id from sources where purged_at is null and purge_due<=now() order by purge_due,id limit 64 for update skip locked),
    cleared as materialized(select id,purge_source_content(id) from due),
    events as (insert into audit_events(action,subject,detail) select 'source.expire',id::text,'{}'::jsonb from cleared returning subject) select count(*) from events`).Scan(&count)
		})
		if err != nil {
			return err
		}
		if count < 64 {
			break
		}
	}
	var cursor string
	var firstErr error
	for {
		rows, err := db.Query(ctx, "select distinct hash from source_object_cleanup where hash>$1 order by hash limit 128", cursor)
		if err != nil {
			return err
		}
		hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(hashes) == 0 {
			return firstErr
		}
		for _, hash := range hashes {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := o.cleanSourceObject(ctx, db, hash); err != nil {
				_, markErr := db.Exec(ctx, "update source_object_cleanup set last_failure=now() where hash=$1", hash)
				if firstErr == nil {
					firstErr = errors.Join(err, markErr)
				}
			}
		}
		cursor = hashes[len(hashes)-1]
	}
}

func (o Objects) cleanSourceObject(ctx context.Context, db *pgxpool.Pool, hash string) error {
	return database.WithTx(ctx, db, func(tx pgx.Tx) error {
		// A backup holds the shared lock from before its snapshot until its files are copied.
		if _, err := tx.Exec(ctx, "select pg_advisory_xact_lock(17952103)"); err != nil {
			return err
		}
		if err := lockObject(ctx, tx, hash); err != nil {
			return err
		}

		// Only acknowledge entries observed before checking live references. A new
		// concurrent detachment must keep its own queue entry for the next pass.
		rows, err := tx.Query(ctx, "delete from source_object_cleanup where hash=$1 returning source_id::text", hash)
		if err != nil {
			return err
		}
		sources, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(sources) == 0 {
			return err
		}
		var referenced bool
		if err := tx.QueryRow(ctx, `select exists(select from source_versions where object_hash=$1) or exists(select from artifact_files where object_hash=$1)
   or exists(select from invocation_files where object_hash=$1) or exists(select from validation_files where object_hash=$1)
   or exists(select from validation_pictures where object_hash=$1)
   or exists(select from skill_files where object_hash=$1) or exists(select from artifact_preview_jobs where preview_hash=$1)
   or exists(select from space_publications where object_hash=$1)`, hash).Scan(&referenced); err != nil {
			return err
		}
		if referenced {
			_, err := tx.Exec(ctx, "update sources set shared_files_retained=shared_files_retained+1 where id=any($1::uuid[])", sources)
			return err
		}
		if _, err := tx.Exec(ctx, "delete from objects where hash=$1", hash); err != nil {
			return err
		}
		raw, err := hex.DecodeString(hash)
		if err != nil || len(raw) != 32 {
			return errors.New("invalid cleanup object digest")
		}
		if err := os.Remove(filepath.Join(o.Root, hash)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	})
}
