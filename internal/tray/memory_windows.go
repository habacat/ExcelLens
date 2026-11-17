//go:build windows

package tray

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	pGetCurrentProcess  = kernel32.NewProc("GetCurrentProcess")
	pReadProcessMemory  = kernel32.NewProc("ReadProcessMemory")
	pWriteProcessMemory = kernel32.NewProc("WriteProcessMemory")
)

// readLocalMemory copies from a Win32-owned address in this process into a
// Go-owned destination. Windows validates the source range before copying.
// In particular, addresses arriving in WM_COPYDATA must not be converted
// from uintptr into unsafe.Pointer and dereferenced by the Go runtime.
func readLocalMemory(address uintptr, dst unsafe.Pointer, size uintptr) bool {
	if address == 0 || dst == nil || size == 0 {
		return false
	}
	process, _, _ := pGetCurrentProcess.Call()
	if process == 0 {
		return false
	}
	var copied uintptr
	ok, _, _ := pReadProcessMemory.Call(process, address, uintptr(dst), size, uintptr(unsafe.Pointer(&copied)))
	runtime.KeepAlive(dst)
	return ok != 0 && copied == size
}

// writeLocalMemory copies Go-owned bytes into a Win32-owned buffer allocated
// in this process (for example, memory obtained using GlobalLock). It avoids
// fabricating a Go pointer from an integer Win32 address.
func writeLocalMemory(address uintptr, src unsafe.Pointer, size uintptr) error {
	if address == 0 || src == nil || size == 0 {
		return errors.New("invalid memory copy arguments")
	}
	process, _, _ := pGetCurrentProcess.Call()
	if process == 0 {
		return errors.New("GetCurrentProcess returned NULL")
	}
	var copied uintptr
	ok, _, err := pWriteProcessMemory.Call(process, address, uintptr(src), size, uintptr(unsafe.Pointer(&copied)))
	runtime.KeepAlive(src)
	if ok == 0 {
		if err == syscall.Errno(0) {
			return errors.New("WriteProcessMemory returned FALSE")
		}
		return fmt.Errorf("WriteProcessMemory failed: %w", err)
	}
	if copied != size {
		return fmt.Errorf("short write: %d of %d bytes", copied, size)
	}
	return nil
}
