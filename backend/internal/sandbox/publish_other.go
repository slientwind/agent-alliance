//go:build !darwin && !linux

package sandbox

import (
	"os"
	"path/filepath"
)

func publishTaskResults(staged, result string) error {
	entries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	if err = os.Mkdir(result, 0700); err != nil {
		return err
	}
	for _, entry := range entries {
		if err = os.Rename(filepath.Join(staged, entry.Name()), filepath.Join(result, entry.Name())); err != nil {
			_ = os.RemoveAll(result)
			return err
		}
	}
	return nil
}
