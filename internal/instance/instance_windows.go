//go:build windows

// Package instance owns the Windows startup mutex. It is acquired BEFORE the
// HTTP server or tray is created, so two near-simultaneous double-clicks cannot
// create competing listeners/tray icons. A second process must forward to the
// first one; it never assumes that the tray window already exists.
package instance

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32    = syscall.NewLazyDLL("kernel32.dll")
	createMutex = kernel32.NewProc("CreateMutexW")
	closeHandle = kernel32.NewProc("CloseHandle")
)

// Acquire returns owner=false when this logon session has a running instance.
// The mutex name includes a hash of the user's profile path, preventing an
// unrelated user in a shared Windows session from colliding accidentally.
// We do NOT take initial ownership: the handle's lifetime is the process lock.
func Acquire() (owner bool, release func(), err error) {
	home, e := os.UserConfigDir()
	if e != nil || home == "" {
		return false, nil, fmt.Errorf("无法定位用户目录以创建单实例锁：%w", e)
	}
	digest := sha256.Sum256([]byte(strings.ToLower(home)))
	name := fmt.Sprintf("Local\\ExcelLens.SingleInstance.v1.%x", digest[:12])
	p, e := syscall.UTF16PtrFromString(name)
	if e != nil {
		return false, nil, e
	}
	h, _, callErr := createMutex.Call(0, 0, uintptr(unsafe.Pointer(p)))
	if h == 0 {
		if callErr == syscall.Errno(0) {
			callErr = errors.New("CreateMutexW returned NULL")
		}
		return false, nil, fmt.Errorf("建立单实例锁失败：%w", callErr)
	}
	if callErr == syscall.ERROR_ALREADY_EXISTS {
		closeHandle.Call(h)
		return false, nil, nil
	}
	return true, func() { closeHandle.Call(h) }, nil
}
