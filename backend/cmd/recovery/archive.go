package main

import (
	"alliance/internal/database"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
)

var roles = []string{"business", "temporal", "visibility"}
var safeName = regexp.MustCompile("^[A-Za-z0-9_][A-Za-z0-9_.-]*$")
var digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")

type point struct {
	Role                  string
	Taken                 time.Time
	Snapshot, LSN, SHA256 string
}
type manifest struct {
	Version    int
	Enterprise string
	Databases  []point
	Objects    int
	Finished   time.Time
	Expires    time.Time
}
type bundle struct {
	info  manifest
	files map[string]*zip.File
}

func backup(ctx context.Context, o options, secret string) error {
	if o.retention <= 0 || o.retention > 30*24*time.Hour {
		return errors.New("整库备份保留期须大于零且最长 720 小时")
	}
	names := []string{o.business, o.temporal, o.visibility}
	services := []string{"", o.historyService, o.visibilityService}
	seen := map[string]bool{}
	for i, name := range names {
		identity := services[i] + "/" + name
		if !safeName.MatchString(name) || seen[identity] {
			return errors.New("三份数据库必须分别指定")
		}
		seen[identity] = true
	}
	if _, err := os.Lstat(o.archive); err == nil {
		return fmt.Errorf("备份文件已存在 %s", o.archive)
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(o.archive)
	f, err := os.CreateTemp(dir, ".alliance-backup-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	recipient, err := age.NewScryptRecipient(secret)
	if err != nil {
		return err
	}
	enc, err := age.Encrypt(f, recipient)
	if err != nil {
		return err
	}
	z := zip.NewWriter(enc)
	info := manifest{Version: 2}
	for i, name := range names {
		if err := dump(ctx, z, name, roles[i], services[i], o.objects, &info); err != nil {
			return err
		}
	}
	info.Finished = time.Now().UTC()
	info.Expires = info.Databases[0].Taken.Add(o.retention).UTC()
	w, err := z.Create("manifest.json")
	if err != nil {
		return err
	}
	if err = json.NewEncoder(w).Encode(info); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	if err = enc.Close(); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// A concurrent or repeated backup must never replace a completed archive.
	if err = os.Link(f.Name(), o.archive); err != nil {
		return err
	}
	if err = syncDir(dir); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(info)
}
func dump(ctx context.Context, z *zip.Writer, name, role, service, root string, info *manifest) error {
	conn, err := connectService(ctx, name, service)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if role == "business" {
		// conn.Close releases the session lock after the database and files are copied.
		if _, err = conn.Exec(ctx, "select pg_advisory_lock_shared(17952103)"); err != nil {
			return err
		}
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	p := point{Role: role}
	if err = tx.QueryRow(ctx, "select pg_export_snapshot(),clock_timestamp(),pg_current_snapshot()::text,pg_current_wal_lsn()::text").Scan(&id, &p.Taken, &p.Snapshot, &p.LSN); err != nil {
		return err
	}
	if role == "business" {
		if err = database.RequireServingDatabase(ctx, tx); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "select default_team from enterprise").Scan(&info.Enterprise); err != nil {
			return err
		}
	} else {
		table := "executions"
		if role == "visibility" {
			table = "executions_visibility"
		}
		var exists bool
		if err = tx.QueryRow(ctx, "select to_regclass($1) is not null", table).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%s 数据库缺少 %s", role, table)
		}
	}
	w, err := z.CreateHeader(&zip.FileHeader{Name: role + ".dump", Method: zip.Store})
	if err != nil {
		return err
	}
	h := sha256.New()
	cmd := pgTool(ctx, "pg_dump", name, "--no-password", "--format=custom", "--no-owner", "--no-acl", "--snapshot="+id, "--dbname="+name)
	if service != "" {
		cmd.Env = append(cmd.Env, "PGSERVICE="+service)
	}
	cmd.Stdout = io.MultiWriter(w, h)
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("%s dump: %w", role, err)
	}
	p.SHA256 = hex.EncodeToString(h.Sum(nil))
	info.Databases = append(info.Databases, p)
	if role == "business" {
		rows, err := tx.Query(ctx, "select hash,size_bytes from objects order by hash")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hash string
			var size int64
			if err = rows.Scan(&hash, &size); err != nil {
				return err
			}
			if !digestPattern.MatchString(hash) {
				return errors.New("对象摘要无效")
			}
			w, err = z.CreateHeader(&zip.FileHeader{Name: "objects/" + hash, Method: zip.Store})
			if err != nil {
				return err
			}
			if err = copyObject(w, filepath.Join(root, hash), hash, size); err != nil {
				return err
			}
			info.Objects++
		}
		if err = rows.Err(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func copyObject(w io.Writer, path, hash string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != size {
		return fmt.Errorf("对象大小异常 %s", hash)
	}
	return verifiedCopy(w, f, hash, size)
}
func verifiedCopy(w io.Writer, r io.Reader, hash string, size int64) error {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), r)
	if err != nil {
		return err
	}
	if n != size || fmt.Sprintf("%x", h.Sum(nil)) != hash {
		return fmt.Errorf("完整性检查失败 %s", hash)
	}
	return nil
}
func readArchive(path, secret string) (*bundle, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	identity, err := age.NewScryptIdentity(secret)
	if err != nil {
		return nil, nil, err
	}
	identity.SetMaxWorkFactor(18)
	plain, err := age.Decrypt(f, identity)
	if err != nil {
		return nil, nil, err
	}
	// The complete encrypted stream must authenticate before any SQL is executed.
	tmp, err := os.CreateTemp("", "alliance-recovery-")
	if err != nil {
		return nil, nil, err
	}
	// Remove the directory entry now so a killed process leaves no plaintext bundle.
	if err = os.Remove(tmp.Name()); err != nil {
		tmp.Close()
		return nil, nil, err
	}
	cleanup := func() { tmp.Close() }
	size, err := io.Copy(tmp, plain)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	z, err := zip.NewReader(tmp, size)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	b, err := inspect(z)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return b, cleanup, nil
}
func inspect(z *zip.Reader) (*bundle, error) {
	b := &bundle{files: map[string]*zip.File{}}
	for _, f := range z.File {
		if b.files[f.Name] != nil {
			return nil, errors.New("重复的备份条目")
		}
		b.files[f.Name] = f
	}
	meta := b.files["manifest.json"]
	if meta == nil || meta.UncompressedSize64 > 1<<20 {
		return nil, errors.New("缺少备份清单或清单过大")
	}
	r, err := meta.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &b.info) != nil || (b.info.Version != 1 && b.info.Version != 2) || b.info.Enterprise == "" || len(b.info.Databases) != len(roles) {
		return nil, errors.New("备份清单格式无效")
	}
	taken := b.info.Databases[0].Taken
	if b.info.Version == 1 {
		b.info.Expires = taken.Add(30 * 24 * time.Hour)
	}
	if taken.IsZero() || !b.info.Expires.After(taken) || b.info.Expires.Sub(taken) > 30*24*time.Hour {
		return nil, errors.New("备份保留期限无效")
	}
	expected := map[string]string{}
	for i, p := range b.info.Databases {
		if p.Role != roles[i] || !digestPattern.MatchString(p.SHA256) {
			return nil, errors.New("数据库恢复点无效")
		}
		expected[p.Role+".dump"] = p.SHA256
	}
	objects := 0
	for name, f := range b.files {
		if name == "manifest.json" {
			continue
		}
		h := expected[name]
		if h == "" {
			if len(name) != 72 || name[:8] != "objects/" || !digestPattern.MatchString(name[8:]) {
				return nil, fmt.Errorf("未知备份条目 %q", name)
			}
			h = name[8:]
			objects++
		} else {
			delete(expected, name)
		}
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		err = verifiedCopy(io.Discard, r, h, int64(f.UncompressedSize64))
		r.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(expected) != 0 || objects != b.info.Objects {
		return nil, errors.New("备份文件数量不匹配")
	}
	return b, nil
}
