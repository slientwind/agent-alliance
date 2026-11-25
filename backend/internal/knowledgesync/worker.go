package knowledgesync

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"alliance/internal/database"
	"alliance/internal/identity"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
)

type syncJob struct {
	id, project, actor, name, lease string
	revision                        int
	request                         providerRequest
	cipher                          []byte
}

func (s Server) Serve(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if _, err := s.syncOne(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("enterprise knowledge sync deferred")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s Server) syncOne(ctx context.Context) (bool, error) {
	var j syncJob
	err := s.DB.QueryRow(ctx, `with job as (
		select id from enterprise_knowledge_sources where enabled and next_at<=clock_timestamp()
		order by next_at,id for update skip locked limit 1
	) update enterprise_knowledge_sources k set status='syncing',lease=gen_random_uuid(),next_at=clock_timestamp()+interval '2 minutes'
	from job where job.id=k.id returning k.id,k.project_id,k.configured_by,k.name,k.lease,k.revision,k.provider,k.settings,k.encrypted_credentials`).
		Scan(&j.id, &j.project, &j.actor, &j.name, &j.lease, &j.revision, &j.request.provider, &j.request.settings, &j.cipher)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var allowed bool
	err = s.DB.QueryRow(ctx, `select exists(select from enterprise_knowledge_sources k join projects p on p.id=k.project_id and p.owner=k.configured_by
		join project_members pm on (pm.project_id,pm.member_id)=(p.id,p.owner)
		join members m on m.id=p.owner and m.enabled and m.role='admin' and m.kind='person'
		where k.id=$1 and k.revision=$2 and k.lease=$3 and k.enabled)`, j.id, j.revision, j.lease).Scan(&allowed)
	if err != nil {
		return true, err
	}
	verdict := "owner"
	result := ""
	if allowed {
		j.request.secret, err = s.Vault.Open(j.cipher, sourceScope(j.id))
		if err != nil || !credentialValid(j.request.provider, j.request.secret) || !j.request.settings.valid(j.request.provider) {
			verdict = "credential"
		} else {
			fetch := s.fetch
			if fetch == nil {
				fetch = (providerClient{egress: s.Egress}).fetch
			}
			request, cancel := context.WithTimeout(ctx, 25*time.Second)
			result, err = fetch(request, j.request)
			cancel()
			switch {
			case err == nil:
				verdict = ""
			case errors.Is(err, errDenied):
				verdict = "access"
			case errors.Is(err, errInvalid):
				verdict = "format"
			case errors.Is(err, errEgress):
				verdict = "egress"
			default:
				verdict = "remote"
			}
		}
	}
	return true, s.finish(ctx, j, result, verdict)
}

func (s Server) finish(ctx context.Context, j syncJob, text, verdict string) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := identity.RequireAdmin(ctx, tx, j.actor); err != nil {
			if _, ok := err.(web.Problem); !ok {
				return err
			}
			verdict = "owner"
		}
		if err := lockTarget(ctx, tx, j.project, j.actor, true); err != nil {
			if _, ok := err.(web.Problem); !ok {
				return err
			}
			verdict = "owner"
		}
		var source, previousHash *string
		var version *int
		err := tx.QueryRow(ctx, `select source_id,content_hash,published_version from enterprise_knowledge_sources
			where id=$1 and revision=$2 and lease=$3 and enabled for update`, j.id, j.revision, j.lease).Scan(&source, &previousHash, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if verdict == "access" || verdict == "owner" || verdict == "credential" {
			if source != nil {
				if err := s.Objects.RevokeSyncedText(ctx, tx, j.project, *source, j.actor); err != nil {
					return err
				}
			}
			source, version, previousHash = nil, nil, nil
		}
		if verdict == "" {
			contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(j.name+"\x00"+text)))
			verdict, err = s.publish(ctx, tx, j, source, version, previousHash, contentHash, text)
			if err != nil {
				return err
			}
			if verdict == "" {
				_, err = tx.Exec(ctx, `update enterprise_knowledge_sources set status='ready',verdict='',lease=null,
					fetched_at=clock_timestamp(),next_at=clock_timestamp()+interval '15 minutes' where id=$1`, j.id)
				return err
			}
		}
		_, err = tx.Exec(ctx, `update enterprise_knowledge_sources
			set status='unavailable',verdict=$2,lease=null,next_at=clock_timestamp()+interval '5 minutes',
			source_id=$3,published_version=$4,content_hash=$5 where id=$1`, j.id, verdict, source, version, previousHash)
		return err
	})
}

func (s Server) publish(ctx context.Context, tx pgx.Tx, j syncJob, source *string, version *int, previousHash *string, hash, text string) (string, error) {
	filename := j.name + ".txt"
	if version != nil {
		if source == nil || previousHash == nil {
			return "local", nil
		}
		var current int
		err := tx.QueryRow(ctx, `select current_version from sources where id=$1 and project_id=$2 and revoked_at is null for update`, *source, j.project).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && current != *version) {
			return "local", nil
		}
		if err != nil {
			return "", err
		}
		if hash == *previousHash {
			return "", nil
		}
		if err := s.Objects.UpdateSyncedText(ctx, tx, j.project, *source, j.actor, filename, text, current); err != nil {
			return "", err
		}
		*version = current + 1
	} else {
		id, err := s.Objects.NewTextSource(ctx, tx, j.project, j.actor, filename, text)
		if err != nil {
			return "", err
		}
		first := 1
		source, version = &id, &first
	}
	_, err := tx.Exec(ctx, `update enterprise_knowledge_sources set source_id=$2,published_version=$3,content_hash=$4 where id=$1`, j.id, source, version, hash)
	return "", err
}
