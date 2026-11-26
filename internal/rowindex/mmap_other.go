//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package rowindex

import (
	"errors"
	"os"
)

func MapFile(*os.File, int64) ([]byte, error) {
	return nil, errors.New("当前平台不支持超大行索引映射")
}

func UnmapFile([]byte) error { return nil }
