package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"io"
	"os"
	"path/filepath"
)

type Objects struct{ Root string }

// Put holds the same per-digest transaction lock as the cleanup worker until
// the caller has committed its source, artifact or validation reference.
func (o Objects) Put(ctx context.Context, tx pgx.Tx, input io.Reader, limit int64) (string, int64, error) {
	if err := os.MkdirAll(o.Root, 0700); err != nil {
		return "", 0, err
	}
	f, err := os.CreateTemp(o.Root, ".upload-")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	digest := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(input, limit+1))
	if err != nil {
		return "", 0, err
	}
	if size > limit {
		return "", 0, ErrLarge
	}
	if err = f.Sync(); err != nil {
		return "", 0, err
	}
	if err = f.Close(); err != nil {
		return "", 0, err
	}
	hash := fmt.Sprintf("%x", digest.Sum(nil))
	if err = lockObject(ctx, tx, hash); err != nil {
		return "", 0, err
	}
	if _, err = tx.Exec(ctx, "insert into objects(hash,size_bytes) values($1,$2) on conflict do nothing", hash, size); err != nil {
		return "", 0, err
	}
	if err = os.Link(f.Name(), filepath.Join(o.Root, hash)); err != nil && !os.IsExist(err) {
		return "", 0, err
	}
	return hash, size, nil
}

func lockObject(ctx context.Context, tx pgx.Tx, hash string) error {
	_, err := tx.Exec(ctx, "select pg_advisory_xact_lock(hashtextextended($1,17952104))", hash)
	return err
}

var ErrLarge = errors.New("source too large")

func (o Objects) Open(hash string) (*os.File, error) {
	raw, err := hex.DecodeString(hash)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("invalid object hash")
	}
	return os.Open(filepath.Join(o.Root, hash))
}

func ConfiguredObjects() Objects {
	root := os.Getenv("OBJECT_ROOT")
	if root == "" {
		root = "./var/objects"
	}
	return Objects{Root: root}
}
