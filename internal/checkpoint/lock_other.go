//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package checkpoint

import (
	"errors"
	"os"
)

func lockFile(*os.File) error {
	return errors.New("此平台尚不支持可靠的跨进程任务恢复租约")
}
func unlockFile(*os.File) error            { return nil }
func syncDirectory(*os.Root, string) error { return nil }
