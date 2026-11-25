package sandbox

import "golang.org/x/sys/unix"

func publishTaskResults(staged, result string) error {
	return unix.Renameat2(unix.AT_FDCWD, staged, unix.AT_FDCWD, result, unix.RENAME_NOREPLACE)
}
