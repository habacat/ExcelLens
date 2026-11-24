//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package checkpoint

import "os"

func syncDirectory(*os.Root, string) error { return nil }
