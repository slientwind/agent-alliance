package main

import (
	"alliance/internal/database"
	"alliance/internal/knowledge"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
)

// Only the current database may name a file to repair. A backup can supply
// content by hash, but never identities, references, decisions or permissions.
func repair(ctx context.Context, o options, b *bundle) error {
	if o.delivery == "" {
		return errors.New("请指定当前允许分发的交付批次")
	}
	c, err := connect(ctx, o.business)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	if err := database.RequireServingDatabase(ctx, c); err != nil {
		return err
	}
	var intent int64
	// Persist intent independently of the filesystem. An interruption leaves a
	// reviewable request, and retrying checks each hash against the current edition.
	if err = c.QueryRow(ctx, "insert into audit_events(action,subject,detail) select 'operations.delivery_restore_requested',d.id::text,$2 from deliveries d cross join enterprise e where d.id=$1 and e.default_team=$3 returning seq", o.delivery, map[string]any{"backupFinished": b.info.Finished}, b.info.Enterprise).Scan(&intent); err != nil {
		return err
	}
	count := 0
	err = pgx.BeginFunc(ctx, c, func(tx pgx.Tx) error {
		if err := database.RequireServingDatabase(ctx, tx); err != nil {
			return err
		}
		// Coordinate with source withdrawal, distribution decisions and file sealing.
		if _, err := tx.Exec(ctx, "lock table sources,distribution_reviews,deliveries,delivery_items,artifact_files in share mode"); err != nil {
			return err
		}
		var state, enterprise string
		if err := tx.QueryRow(ctx, "select distribution_state(d.id),e.default_team from deliveries d cross join enterprise e where d.id=$1", o.delivery).Scan(&state, &enterprise); err != nil {
			return err
		}
		if enterprise != b.info.Enterprise {
			return errors.New("备份来自其他企业实例")
		}
		if err := knowledge.DistributionError(state); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select o.hash,o.size_bytes from objects o where exists(select from delivery_items i join artifact_files f on(f.artifact_id,f.version)=(i.artifact_id,i.version) where i.delivery_id=$1 and f.object_hash=o.hash) order by o.hash for update of o`, o.delivery)
		if err != nil {
			return err
		}
		type object struct {
			Hash string
			Size int64
		}
		items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[object])
		if err != nil {
			return err
		}
		if err = os.MkdirAll(o.objects, 0700); err != nil {
			return err
		}
		var missing []object
		for _, item := range items {
			if !digestPattern.MatchString(item.Hash) {
				return errors.New("正式库文件摘要无效")
			}
			path := filepath.Join(o.objects, item.Hash)
			if err = copyObject(io.Discard, path, item.Hash, item.Size); err == nil {
				continue
			}
			f := b.files["objects/"+item.Hash]
			if f == nil || int64(f.UncompressedSize64) != item.Size {
				return errors.New("此恢复点缺少所选批次的一份原件，可使用更新的备份重试")
			}
			missing = append(missing, item)
		}
		for _, item := range missing {
			f := b.files["objects/"+item.Hash]
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = replaceVerified(o.objects, item.Hash, item.Size, r)
			r.Close()
			if err != nil {
				return err
			}
			count++
		}
		if err = syncDir(o.objects); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "insert into audit_events(action,subject,detail) values('operations.delivery_files_checked',$1,$2)", o.delivery, map[string]any{"request": intent, "files": len(items), "repaired": count, "backupFinished": b.info.Finished})
		return err
	})
	if err != nil {
		end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, auditErr := c.Exec(end, "insert into audit_events(action,subject,detail) values('operations.delivery_restore_unfinished',$1,$2)", o.delivery, map[string]any{"request": intent, "repaired": count})
		return errors.Join(err, auditErr)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"delivery": o.delivery, "repaired": count})
}
func replaceVerified(dir, hash string, size int64, r io.Reader) error {
	f, err := os.CreateTemp(dir, ".recover-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = verifiedCopy(f, r, hash, size); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, hash))
}
