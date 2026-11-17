//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package workbook

import (
	"errors"
	"os"
)

func mapIndexFile(*os.File, int64) ([]byte, error) {
	return nil, errors.New("当前平台不支持超大行索引映射")
}
func unmapIndexFile([]byte) error { return nil }
