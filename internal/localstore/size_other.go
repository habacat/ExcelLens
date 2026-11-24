//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package localstore

import (
	"errors"
	"io/fs"
	"os"
)

func allocatedSize(_ *os.Root, _ string, _ fs.FileInfo) (int64, error) {
	return 0, errors.New("此平台无法读取文件分配空间")
}
