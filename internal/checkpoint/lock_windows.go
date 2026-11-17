//go:build windows

package checkpoint

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var lockFileEx = kernel32.NewProc("LockFileEx")
var unlockFileEx = kernel32.NewProc("UnlockFileEx")

func lockFile(file *os.File) error {
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

func unlockFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, err := unlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(file)
	if result == 0 {
		return err
	}
	return nil
}

// Windows has no portable directory-fsync through os.Root. The data and both
// manifest slots are individually synced before the atomic Root.Rename.
// Recovery validates the resulting slot; no filesystem-wide power-loss promise.
func syncDirectory(*os.Root, string) error { return nil }
