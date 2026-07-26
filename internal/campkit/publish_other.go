//go:build !linux

package campkit

import "os"

func publishNoReplace(oldPath, newPath string) error {
	// Same-directory hard-link publication is no-replace and keeps the source
	// inode private until the owned temporary name is removed.
	if err := os.Link(oldPath, newPath); err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return err
	}
	return os.Remove(oldPath)
}
