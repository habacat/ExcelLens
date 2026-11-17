//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package checkpoint

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrBusy
	}
	return err
}

func unlockFile(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }

func syncDirectory(root *os.Root, name string) error {
	dir, err := root.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
