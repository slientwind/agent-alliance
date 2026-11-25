package knowledge

import (
	"alliance/internal/database"
	"alliance/internal/web"
	"bytes"
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (o Objects) ServeArtifactPreviews(ctx context.Context, db *pgxpool.Pool) {
	for ctx.Err() == nil {
		progress, err := o.prepareArtifactPreview(ctx, db)
		if err != nil && ctx.Err() == nil {
			slog.Warn("artifact preview remains pending")
		}
		if !progress || err != nil {
			timer := time.NewTimer(time.Second)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
		}
	}
}

func (o Objects) prepareArtifactPreview(ctx context.Context, db *pgxpool.Pool) (bool, error) {
	var artifact, lease string
	var version, attempts int
	var ref fileRef
	err := db.QueryRow(ctx, `with due as (
		select artifact_id,version from artifact_preview_jobs
		where state='pending' and next_at<=now() order by next_at for update skip locked limit 1
	)
	update artifact_preview_jobs j set attempts=j.attempts+1,lease=gen_random_uuid(),next_at=now()+interval '1 minute'
	from due d join artifact_files f using(artifact_id,version)
	where (j.artifact_id,j.version)=(d.artifact_id,d.version)
	returning j.artifact_id,j.version,j.lease,j.attempts,f.object_hash,f.filename,f.media_type`).
		Scan(&artifact, &version, &lease, &attempts, &ref.hash, &ref.name, &ref.media)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, media, err := o.renderPreview(ctx, ref)
	if err != nil {
		state := "pending"
		var problem web.Problem
		if attempts >= 5 || errors.As(err, &problem) && problem.Status < 500 {
			state = "unavailable"
		}
		_, err = db.Exec(ctx, `update artifact_preview_jobs set state=$4,lease=null,next_at=now()+$5*interval '1 second'
			where (artifact_id,version,lease)=($1,$2,$3)`, artifact, version, lease, state, 1<<min(attempts, 6))
		return true, err
	}
	err = database.WithTx(ctx, db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, "select 1 from artifact_preview_jobs where (artifact_id,version,lease)=($1,$2,$3) for update",
			artifact, version, lease).Scan(new(int))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		hash, _, err := o.Put(ctx, tx, bytes.NewReader(data), 16<<20)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update artifact_preview_jobs set state='ready',lease=null,preview_hash=$3,preview_media=$4
			where (artifact_id,version)=($1,$2)`, artifact, version, hash, media)
		return err
	})
	return true, err
}
