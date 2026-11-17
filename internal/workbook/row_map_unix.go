//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package workbook

import (
	"errors"
	"os"
	"syscall"
)

func mapIndexFile(f *os.File, size int64) ([]byte, error) {
	if size <= 0 || uint64(size) > uint64(^uint(0)>>1) {
		return nil, errors.New("行索引超过当前平台地址空间")
	}
	return syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
}

func unmapIndexFile(data []byte) error { return syscall.Munmap(data) }
