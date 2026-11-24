//go:build netbsd

package localstore

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func availableSpace(path string) (volumeSpace, error) {
	// NetBSD statvfs1(2) begins with four unsigned longs followed by
	// 64-bit block counters on all Go architectures. The complete record
	// (mount paths and filesystem names included) fits in a 4 KiB buffer.
	var stat struct {
		Flags, Block, Fragment, IOSize uintptr
		Blocks, Free, Available        uint64
		tail                           [4096]byte
	}
	p, err := syscall.BytePtrFromString(path)
	if err != nil {
		return volumeSpace{}, err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_STATVFS1, uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&stat)), 1)
	if errno != 0 {
		return volumeSpace{}, errno
	}
	st, err := os.Stat(path)
	if err != nil {
		return volumeSpace{}, err
	}
	return volumeSpace{fmt.Sprint(st.Sys().(*syscall.Stat_t).Dev), stat.Available * uint64(stat.Fragment), uint64(stat.Fragment)}, nil
}
