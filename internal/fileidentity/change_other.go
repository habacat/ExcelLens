//go:build !windows && !solaris && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package fileidentity

import "os"

func ChangeHint(_ *os.File, _ os.FileInfo) int64 { return 0 }
