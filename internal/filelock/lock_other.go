//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package filelock

import (
	"errors"
	"os"
)

func Lock(*os.File) error {
	return errors.New("此平台尚不支持可靠的跨进程任务恢复租约")
}
func Unlock(*os.File) error { return nil }
