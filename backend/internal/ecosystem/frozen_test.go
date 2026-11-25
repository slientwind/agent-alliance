package ecosystem

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFrozenFilesKeepTheirOriginalContent(t *testing.T) {
	t.Setenv("OBJECT_ROOT", t.TempDir())
	body := []byte("inspected fixed package data")
	hash := snapshotDigest(body)
	for range 2 {
		if err := freezeSnapshot(hash, body); err != nil {
			t.Fatal(err)
		}
	}
	if actual, err := ReadFrozen(hash); err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("frozen content changed: %v", err)
	}
	path := filepath.Join(frozenDirectory(), hash)
	if err := os.WriteFile(path, []byte("wrong bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := freezeSnapshot(hash, body); err == nil {
		t.Fatal("freeze silently replaced an existing invalid object")
	}
	if _, err := ReadFrozen(hash); err == nil {
		t.Fatal("read returned bytes that failed integrity verification")
	}
	if _, err := ReadFrozen("../object"); err == nil {
		t.Fatal("read accepted an invalid hash path")
	}
}
