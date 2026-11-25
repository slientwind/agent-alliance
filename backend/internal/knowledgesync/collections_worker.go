package knowledgesync

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"github.com/jackc/pgx/v5"
)

type directoryEntry struct {
	ID, Title, Revision, Media, Kind string
	FolderID, ParentID               string
	Path                             []string
}

type remoteDirectory interface {
	List(context.Context, string, string) ([]directoryEntry, string, error)
	Open(context.Context, directoryEntry) (io.ReadCloser, error)
}

type remoteDirectoryACL interface {
	SourceACL(context.Context, directoryEntry) ([][]string, error)
}

type collectionJob struct {
	collectionView
	space, maintainer, scan, lease string
	cipher                         []byte
}

type directoryPage struct {
	folder, cursor string
	path           []string
}

var errCollectionChanged = errors.New("knowledge collection changed during sync")

func (s Server) ServeCollections(ctx context.Context) {
	for ctx.Err() == nil {
		busy, err := s.syncCollection(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Warn("knowledge collection sync deferred")
		}
		delay := 3 * time.Second
		if busy && err == nil {
			delay = 200 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s Server) syncCollection(ctx context.Context) (bool, error) {
	var job collectionJob
	var page directoryPage
	err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `with target as (
		 select id from kb_collections where enabled and sync_provider<>'manual' and next_sync_at<=clock_timestamp()
		 order by next_sync_at,id limit 1 for update skip locked
		), claim as (
		 update kb_collections c set lease=gen_random_uuid(),scan_id=coalesce(scan_id,gen_random_uuid()),
		 sync_status=case when sync_status='denied' then 'denied' else 'syncing' end,next_sync_at=clock_timestamp()+interval '2 minutes'
		 from target t where c.id=t.id returning c.*
		) select id,space_id,maintainer,scan_id,lease,sync_provider,remote_root_id,settings,credential_cipher,name,acl_mode,
		 sync_interval_minutes,revision from claim`).Scan(&job.ID, &job.space, &job.maintainer, &job.scan, &job.lease, &job.Provider,
			&job.Root, &job.Settings, &job.cipher, &job.Name, &job.ACLMode, &job.Interval, &job.Revision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into kb_sync_pages(collection_id,scan_id,folder_id) values($1,$2,$3) on conflict do nothing`, job.ID, job.scan, job.rootFolder()); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select folder_id,cursor,folder_path from kb_sync_pages
		 where (collection_id,scan_id)=($1,$2) and not done order by folder_id,cursor limit 1`, job.ID, job.scan).
			Scan(&page.folder, &page.cursor, &page.path)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var authorized bool
	if err := s.DB.QueryRow(ctx, `select coalesce(space_role($1,$2) in ('space_admin','member'),false)`, job.space, job.maintainer).Scan(&authorized); err != nil {
		return true, err
	}
	if !authorized {
		return true, s.failCollection(ctx, job, errDenied)
	}
	var secret collectionSecret
	raw, err := s.Vault.Open(job.cipher, collectionScope(job.ID))
	if err != nil || json.Unmarshal([]byte(raw), &secret) != nil || !secret.valid(job.Provider) || !job.valid() {
		return true, s.failCollection(ctx, job, errDenied)
	}
	work, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	open := s.directory
	if open == nil {
		open = (providerClient{egress: s.Egress}).openDirectory
	}
	dir, err := open(work, job.collectionView, secret)
	if err == nil {
		err = s.syncDirectoryPage(work, job, page, dir)
	}
	if errors.Is(err, errCollectionChanged) || err == nil || ctx.Err() != nil {
		return true, nil
	}
	return true, s.failCollection(ctx, job, err)
}

func (j collectionJob) rootFolder() string {
	if j.Provider == "feishu_wiki" || j.Provider == "confluence" {
		return ""
	}
	return j.Root
}

func (s Server) failCollection(ctx context.Context, job collectionJob, problem error) error {
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		state, postpone := "unavailable", 1
		denied := errors.Is(problem, errDenied)
		if denied {
			state, postpone = "denied", 5
		}
		tag, err := tx.Exec(ctx, `update kb_collections set lease=null,
		 sync_status=case when sync_status='denied' then 'denied' else $3 end,
		 scan_id=case when $4 then null else scan_id end,next_sync_at=clock_timestamp()+make_interval(mins=>$5)
		 where (id,lease)=($1,$2)`, job.ID, job.lease, state, denied, postpone)
		if err != nil || tag.RowsAffected() == 0 || !denied {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from kb_sync_pages where collection_id=$1", job.ID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "update kb_documents set source_state='withdrawn' where collection_id=$1 and source_state='present'", job.ID)
		return err
	})
}

func (s Server) syncDirectoryPage(ctx context.Context, job collectionJob, page directoryPage, dir remoteDirectory) error {
	items, next, err := dir.List(ctx, page.folder, page.cursor)
	if err != nil {
		return err
	}
	if len(items) > 200 || (next != "" && (!validRemoteID(next, 4096) || next == page.cursor)) {
		return errInvalid
	}
	for _, item := range items {
		if item.Media == "" {
			continue
		}
		if !validRemoteID(item.ID, 1024) || !validRemoteID(item.Title, 1200) || utf8.RuneCountInString(item.Title) > 300 ||
			(item.Revision != "" && !validRemoteID(item.Revision, 256)) {
			return errInvalid
		}
		if item.Path == nil {
			item.Path = page.path
		}
		if err := s.ingestDirectoryEntry(ctx, job, dir, item); err != nil {
			return err
		}
	}
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionJob(ctx, tx, job); err != nil {
			return err
		}
		if next != "" {
			tag, err := tx.Exec(ctx, `insert into kb_sync_pages(collection_id,scan_id,folder_id,cursor,folder_path)
			 values($1,$2,$3,$4,$5) on conflict do nothing`, job.ID, job.scan, page.folder, next, page.path)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return errInvalid
			}
		}
		for _, item := range items {
			if item.FolderID == "" {
				continue
			}
			if !validRemoteID(item.FolderID, 1024) || !validRemoteID(item.Title, 1200) || len(page.path) >= 100 {
				return errInvalid
			}
			path := append(append([]string{}, page.path...), item.Title)
			if _, err := tx.Exec(ctx, `insert into kb_sync_pages(collection_id,scan_id,folder_id,folder_path)
			 values($1,$2,$3,$4) on conflict do nothing`, job.ID, job.scan, item.FolderID, path); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `update kb_sync_pages set done=true where (collection_id,scan_id,folder_id,cursor)=($1,$2,$3,$4)`,
			job.ID, job.scan, page.folder, page.cursor); err != nil {
			return err
		}
		var more bool
		if err := tx.QueryRow(ctx, `select exists(select from kb_sync_pages where (collection_id,scan_id)=($1,$2) and not done)`, job.ID, job.scan).Scan(&more); err != nil {
			return err
		}
		if more {
			_, err := tx.Exec(ctx, "update kb_collections set lease=null,next_sync_at=clock_timestamp() where id=$1", job.ID)
			return err
		}
		if _, err := tx.Exec(ctx, `update kb_documents set source_state='withdrawn',updated_at=clock_timestamp()
		 where collection_id=$1 and source_state='present' and seen_scan_id is distinct from $2`, job.ID, job.scan); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "delete from kb_sync_pages where collection_id=$1", job.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update kb_collections set lease=null,scan_id=null,sync_status='ready',last_synced_at=clock_timestamp(),
		 next_sync_at=clock_timestamp()+make_interval(mins=>sync_interval_minutes) where id=$1`, job.ID)
		return err
	})
}

func lockCollectionJob(ctx context.Context, tx pgx.Tx, job collectionJob) error {
	var id string
	err := tx.QueryRow(ctx, `select id from kb_collections where (id,revision,scan_id,lease,maintainer)=($1,$2,$3,$4,$5)
	 and enabled for update`, job.ID, job.Revision, job.scan, job.lease, job.maintainer).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errCollectionChanged
	}
	if err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRow(ctx, `select coalesce(space_role($1,$2) in ('space_admin','member'),false)`, job.space, job.maintainer).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errDenied
	}
	return nil
}

func (s Server) ingestDirectoryEntry(ctx context.Context, job collectionJob, dir remoteDirectory, item directoryEntry) error {
	acl, verified := [][]string{}, false
	if source, ok := dir.(remoteDirectoryACL); job.ACLMode == "source" && ok {
		groups, err := source.SourceACL(ctx, item)
		if err == nil {
			acl, verified = verifiedACL(groups)
		}
	}
	var current bool
	if err := database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionJob(ctx, tx, job); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `update kb_documents set seen_scan_id=$3,title=$4,directory_path=$5,source_state='present',remote_parent_id=$8,
		 source_acl_principals=$9,acl_verified_at=case when $10 then clock_timestamp() end
		 where (collection_id,remote_id)=($1,$2) and media_type=$6 and $7<>'' and source_revision=$7`,
			job.ID, item.ID, job.scan, item.Title, item.Path, item.Media, item.Revision, item.ParentID, acl, verified)
		current = tag.RowsAffected() > 0
		if err == nil && !current {
			// A superseded upstream version cannot continue serving evidence while its replacement is downloaded.
			_, err = tx.Exec(ctx, `update kb_documents set source_state='withdrawn',acl_verified_at=null
			 where (collection_id,remote_id)=($1,$2) and source_state='present'`, job.ID, item.ID)
		}
		return err
	}); err != nil || current {
		return err
	}
	file, err := dir.Open(ctx, item)
	if errors.Is(err, errDenied) {
		return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
			if err := lockCollectionJob(ctx, tx, job); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "update kb_documents set source_state='withdrawn' where (collection_id,remote_id)=($1,$2)", job.ID, item.ID)
			return err
		})
	}
	if err != nil {
		return err
	}
	defer file.Close()
	return database.WithTx(ctx, s.DB, func(tx pgx.Tx) error {
		if err := lockCollectionJob(ctx, tx, job); err != nil {
			return err
		}
		err := s.storeDirectoryEntry(ctx, tx, job, item, file, acl, verified)
		if errors.Is(err, knowledge.ErrLarge) {
			return errInvalid
		}
		return err
	})
}

func (s Server) storeDirectoryEntry(ctx context.Context, tx pgx.Tx, job collectionJob, item directoryEntry, file io.Reader, acl [][]string, verified bool) error {
	hash, _, err := s.Objects.Put(ctx, tx, file, maxKnowledgeFile)
	if err != nil {
		return err
	}
	if item.Revision == "" {
		item.Revision = "sha256:" + hash
	}
	var id string
	var version int
	err = tx.QueryRow(ctx, `insert into kb_documents(collection_id,space_id,remote_id,directory_path,title,media_type,source_revision,
	 seen_scan_id,current_object_hash,maintainer,remote_parent_id,source_acl_principals,acl_verified_at)
	 values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,case when $13 then clock_timestamp() end)
	 on conflict(collection_id,remote_id) do update set directory_path=excluded.directory_path,title=excluded.title,
	 media_type=excluded.media_type,source_revision=excluded.source_revision,source_state='present',seen_scan_id=excluded.seen_scan_id,
	 content_version=kb_documents.content_version+case when (kb_documents.current_object_hash,kb_documents.media_type)
	  is distinct from (excluded.current_object_hash,excluded.media_type) then 1 else 0 end,
	 current_object_hash=excluded.current_object_hash,updated_at=clock_timestamp(),remote_parent_id=excluded.remote_parent_id,
	 source_acl_principals=excluded.source_acl_principals,acl_verified_at=excluded.acl_verified_at
	 returning id,content_version`, job.ID, job.space, item.ID, item.Path, item.Title, item.Media,
		item.Revision, job.scan, hash, job.maintainer, item.ParentID, acl, verified).Scan(&id, &version)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `insert into kb_document_versions(document_id,version,source_revision,object_hash,media_type)
	 values($1,$2,$3,$4,$5) on conflict do nothing`, id, version, item.Revision, hash, item.Media)
	if err != nil || tag.RowsAffected() == 0 || (item.Media != "text/plain" && item.Media != "text/markdown" && item.Media != "text/csv") {
		return err
	}
	obj, err := s.Objects.Open(hash)
	if err != nil {
		return err
	}
	defer obj.Close()
	reader := plainChunks{in: bufio.NewReader(obj), line: 1}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"kb_chunks"}, []string{"document_id", "version", "ordinal", "body", "citation_anchor"},
		pgx.CopyFromFunc(func() ([]any, error) {
			body, first, last, err := reader.next()
			if errors.Is(err, io.EOF) {
				return nil, nil
			}
			if err != nil {
				return nil, err
			}
			return []any{id, version, reader.ordinal, body, fmt.Sprintf(`{"kind":"lines","startLine":%d,"endLine":%d}`, first, last)}, nil
		}))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "update kb_document_versions set parsed_at=clock_timestamp() where (document_id,version)=($1,$2)", id, version)
	return err
}

type plainChunks struct {
	in            *bufio.Reader
	line, ordinal int
}

func (p *plainChunks) next() (string, int, int, error) {
	for {
		var body strings.Builder
		first, last := p.line, p.line
		for body.Len() < 7000 {
			r, size, err := p.in.ReadRune()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "", 0, 0, err
			}
			if r == 0 || (r == utf8.RuneError && size == 1) {
				return "", 0, 0, errInvalid
			}
			last = p.line
			body.WriteRune(r)
			if r == '\n' {
				p.line++
				if body.Len() >= 5500 {
					break
				}
			}
		}
		if body.Len() == 0 {
			return "", 0, 0, io.EOF
		}
		if text := strings.TrimSpace(body.String()); text != "" {
			p.ordinal++
			return text, first, last, nil
		}
	}
}
