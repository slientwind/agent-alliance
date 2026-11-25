package resources

import (
	"bytes"
	"strings"
	"testing"
)

func TestVaultRotationAcceptsOlderCiphertextInItsOriginalScope(t *testing.T) {
	oldKey, currentKey := strings.Repeat("09", 32), strings.Repeat("17", 32)
	old, err := NewVault(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewVault(currentKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewVault(currentKey)
	if err != nil {
		t.Fatal(err)
	}
	const value = "synthetic-adapter-credential"
	for _, scope := range []string{"model-provider/model", "mcp-server/tool/bearer", "agent/remote/1", "ecosystem/source/repo/https://foo.example"} {
		box, err := old.Seal(value, scope)
		if err != nil || bytes.Contains(box, []byte(value)) {
			t.Fatalf("failed to seal the original value: %v", err)
		}
		if got, err := rotated.Open(box, scope); err != nil || got != value {
			t.Fatalf("rotation could not read the original value: %v", err)
		}
		if _, err := rotated.Open(box, scope+"/wrong"); err == nil {
			t.Fatal("previous key accepted a foreign credential scope")
		}
		updated, err := rotated.Seal(value, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.Open(updated, scope); err == nil {
			t.Fatal("rotation sealed a new credential with the previous key")
		}
		if got, err := current.Open(updated, scope); err != nil || got != value {
			t.Fatalf("new credential cannot be opened with the current key: %v", err)
		}
	}
}
