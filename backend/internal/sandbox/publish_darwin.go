package sandbox

import "golang.org/x/sys/unix"

func publishTaskResults(staged, result string) error {
	return unix.RenamexNp(staged, result, unix.RENAME_EXCL)
}
