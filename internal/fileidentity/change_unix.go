//go:build linux || openbsd || dragonfly || solaris

package fileidentity

import (
	"os"
	"syscall"
)

func ChangeHint(_ *os.File, info os.FileInfo) int64 {
	return info.Sys().(*syscall.Stat_t).Ctim.Nano()
}
