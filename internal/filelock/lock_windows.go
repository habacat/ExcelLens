//go:build windows

package filelock

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var lockFileEx = kernel32.NewProc("LockFileEx")
var unlockFileEx = kernel32.NewProc("UnlockFileEx")

func Lock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, err := lockFileEx.Call(file.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result != 0 {
		return nil
	}
	if err == syscall.Errno(33) { // ERROR_LOCK_VIOLATION; never wait on another writer.
		return ErrBusy
	}
	return err
}

func Unlock(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, err := unlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result == 0 {
		return err
	}
	return nil
}
