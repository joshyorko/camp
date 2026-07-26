//go:build linux

package campkit

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishNoReplace(oldPath, newPath string) error {
	err := unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
	if err == unix.EEXIST {
		return os.ErrExist
	}
	return err
}
