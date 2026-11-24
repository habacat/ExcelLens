//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package localstore

import (
	"io/fs"
	"os"
	"syscall"
)

func allocatedSize(_ *os.Root, _ string, st fs.FileInfo) (int64, error) {
	return st.Sys().(*syscall.Stat_t).Blocks * 512, nil
}
