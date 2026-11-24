//go:build !windows

package localstore

import (
	"errors"
	"os"
)

// Regular files are linked into place exclusively. The caller owns and
// removes the staging tree. A directory rename cannot replace a nonempty one.
func publishNew(from, to string) error {
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return os.Link(from, to)
	}
	if _, err := os.Lstat(to); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(from, to)
}
