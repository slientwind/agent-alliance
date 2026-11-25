package sandbox

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"alliance/internal/database"
	"alliance/internal/knowledge"
	"alliance/internal/projects"
	"alliance/internal/web"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PublishedArtifact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
	Hash    string `json:"sha256"`
}

// ImportArtifacts publishes the results of a completed workspace task. An
// identical retry keeps the same version and preview job.
func ImportArtifacts(ctx context.Context, db *pgxpool.Pool, objects knowledge.Objects, run, directory string) ([]PublishedArtifact, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var paths []string
	count := 0
	var total int64
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		count++
		if count > maxEntries {
			return errors.New("the task contains too many output paths")
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || !utf8.ValidString(name) || utf8.RuneCountInString(path.Base(name)) > 250 {
			return errors.New("the task output must have a valid filename and contain a regular file")
		}
		total += info.Size()
		if total > maxBytes {
			return errors.New("the task outputs exceed the allowed size")
		}
		paths = append(paths, name)
		return nil
	})
	if err != nil {
		return nil, err
	}
	artifacts := make([]PublishedArtifact, 0, len(paths))
	err = database.WithTx(ctx, db, func(tx pgx.Tx) error {
		var project, actor, stage string
		err := tx.QueryRow(ctx, `select p.id,r.actor,p.stage
			from runs r join projects p on (p.id,p.plan_version)=(r.project_id,r.plan_version)
			join project_members pm on (pm.project_id,pm.member_id)=(p.id,r.actor) join members m on m.id=r.actor and m.enabled
			where r.id=$1 and r.status='completed' and p.stage in ('brief','plan','review')
			for update of p,r`, run).Scan(&project, &actor, &stage)
		if errors.Is(err, pgx.ErrNoRows) {
			return web.Fail(409, "所选任务尚未完成、访问已撤回或成果已封存")
		}
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `select 1 from project_members pm join members m on m.id=pm.member_id and m.enabled
			where pm.project_id=$1 and pm.member_id=$2`, project, actor).Scan(new(int)); err != nil {
			return err
		}
		var ordinal int
		if err = tx.QueryRow(ctx, "select coalesce(max(ordinal),0) from artifacts where run_id=$1", run).Scan(&ordinal); err != nil {
			return err
		}
		changed := false
		remaining := maxBytes
		for _, name := range paths {
			item, added, size, err := importArtifact(ctx, tx, objects, root, run, project, name, &ordinal, remaining)
			if err != nil {
				return err
			}
			remaining -= size
			changed = changed || added
			artifacts = append(artifacts, item)
		}
		if changed {
			if err = projects.Touch(ctx, tx, project, stage); err != nil {
				return err
			}
			return database.Audit(ctx, tx, actor, "sandbox.outputs", run, map[string]int{"files": len(artifacts)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return artifacts, nil
}

func importArtifact(ctx context.Context, tx pgx.Tx, objects knowledge.Objects, root *os.Root, run, project, name string, ordinal *int, remaining int64) (PublishedArtifact, bool, int64, error) {
	item := PublishedArtifact{Name: path.Base(name)}
	source, err := root.Open(filepath.FromSlash(name))
	if err != nil {
		return item, false, 0, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return item, false, 0, errors.New("the task output changed before it could be registered")
	}
	if part := knowledge.OfficePart(item.Name); part != "" {
		if err := knowledge.ValidateOffice(source, info.Size(), part); err != nil {
			return item, false, 0, err
		}
	}
	hash, size, err := objects.Put(ctx, tx, source, min(64<<20, remaining))
	if err != nil {
		return item, false, 0, err
	}
	item.Hash = hash
	var previous string
	var imported int
	err = tx.QueryRow(ctx, `select a.id,a.current_version,s.imported_version,f.object_hash from sandbox_artifacts s
		join artifacts a on a.id=s.artifact_id join artifact_files f on (f.artifact_id,f.version)=(a.id,s.imported_version)
		where s.run_id=$1 and s.output_path=$2 for update of a`, run, name).Scan(&item.ID, &item.Version, &imported, &previous)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		*ordinal = *ordinal + 1
		item.Version = 1
		err = tx.QueryRow(ctx, "insert into artifacts(project_id,run_id,ordinal,name) values($1,$2,$3,$4) returning id",
			project, run, *ordinal, item.Name).Scan(&item.ID)
		if err == nil {
			_, err = tx.Exec(ctx, "insert into sandbox_artifacts(run_id,output_path,artifact_id) values($1,$2,$3)", run, name, item.ID)
		}
	case err != nil:
		return item, false, size, err
	case previous == hash:
		item.Version = imported
		return item, false, size, nil
	default:
		item.Version++
		_, err = tx.Exec(ctx, "update artifacts set current_version=$2 where id=$1", item.ID, item.Version)
		if err == nil {
			_, err = tx.Exec(ctx, "update sandbox_artifacts set imported_version=$3 where run_id=$1 and output_path=$2", run, name, item.Version)
		}
	}
	if err != nil {
		return item, false, size, err
	}
	media := sandboxOutputMedia(item.Name)
	if _, err = tx.Exec(ctx, "insert into artifact_versions(artifact_id,version,body,reason) values($1,$2,$3,'沙箱产物')",
		item.ID, item.Version, item.Name); err != nil {
		return item, false, size, err
	}
	if _, err = tx.Exec(ctx, "insert into artifact_files(artifact_id,version,object_hash,filename,media_type) values($1,$2,$3,$4,$5)",
		item.ID, item.Version, hash, item.Name, media); err != nil {
		return item, false, size, err
	}
	if _, err = tx.Exec(ctx, "insert into artifact_preview_jobs(artifact_id,version) values($1,$2)", item.ID, item.Version); err != nil {
		return item, false, size, err
	}
	return item, true, size, nil
}

func sandboxOutputMedia(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".txt", ".md", ".json":
		return "text/plain; charset=utf-8"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".tsv":
		return "text/tab-separated-values; charset=utf-8"
	}
	return "application/octet-stream"
}
