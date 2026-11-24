//go:build windows

package localstore

import (
	"golang.org/x/sys/windows"
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
	return windows.MoveFileEx(source, dest, windows.MOVEFILE_WRITE_THROUGH)
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

// Windows commits same-volume renames with WRITE_THROUGH, and files are flushed
// before publication. Directory handles do not support FlushFileBuffers here.
func syncDir(string) error { return nil }
func replaceFile(from, to string) error {
	src, err := widePath(from)
	if err != nil {
		return err
	}
	dst, err := widePath(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(src, dst, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
