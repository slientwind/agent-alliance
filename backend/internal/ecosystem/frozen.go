package ecosystem

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"alliance/internal/knowledge"
	"alliance/internal/skills"
)

func frozenDirectory() string { return filepath.Join(knowledge.ConfiguredObjects().Root, "skills") }

func snapshotDigest(raw []byte) string { return fmt.Sprintf("%x", sha256.Sum256(raw)) }

func ReadFrozen(hash string) ([]byte, error) {
	raw, err := hex.DecodeString(hash)
	if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != hash {
		return nil, errors.New("invalid ecosystem package digest")
	}
	root, err := os.OpenRoot(frozenDirectory())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat(hash)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > skills.ArchiveLimit {
		return nil, errors.New("invalid ecosystem object")
	}
	file, err := root.Open(hash)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, skills.ArchiveLimit+1))
	if err != nil || int64(len(body)) != info.Size() || snapshotDigest(body) != hash {
		return nil, errors.New("ecosystem object integrity check failed")
	}
	return body, nil
}

func freezeSnapshot(hash string, archive []byte) error {
	if len(archive) == 0 || len(archive) > skills.ArchiveLimit || snapshotDigest(archive) != hash {
		return errors.New("invalid ecosystem snapshot")
	}
	directory := frozenDirectory()
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".staged-")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := io.Copy(temporary, bytes.NewReader(archive)); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary.Name(), filepath.Join(directory, hash)); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	_, err = ReadFrozen(hash)
	return err
}
