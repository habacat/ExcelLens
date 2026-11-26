//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package rowindex

// Unknown process APIs cannot prove that an owned query file is stale.
func queryProcessAlive(int) bool { return true }
