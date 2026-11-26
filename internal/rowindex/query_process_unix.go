//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package rowindex

import (
	"errors"
	"os"
	"syscall"
)

func queryProcessAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	defer p.Release()
	// Signal zero probes existence/permission; it does not deliver a signal.
	err = p.Signal(syscall.Signal(0))
	return !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH)
}
