//go:build windows

package localstore

import (
	"path/filepath"
	"strings"
	"syscall"
)

// MoveFileW fails if the destination exists, including on volumes without
// hard links. Moves are always within one directory tree on the same volume.
func publishNew(from, to string) error {
	source, err := widePath(from)
	if err != nil {
		return err
	}
	dest, err := widePath(to)
	if err != nil {
		return err
	}
	return syscall.MoveFile(source, dest)
}

func widePath(name string) (*uint16, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(absolute, `\\?\`) {
		if strings.HasPrefix(absolute, `\\`) {
			absolute = `\\?\UNC\` + absolute[2:]
		} else {
			absolute = `\\?\` + absolute
		}
	}
	return syscall.UTF16PtrFromString(absolute)
}
