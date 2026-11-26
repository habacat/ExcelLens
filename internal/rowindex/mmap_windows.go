//go:build windows

package rowindex

import (
	"errors"
	"os"
	"reflect"
	"syscall"
	"unsafe"
)

func MapFile(f *os.File, size int64) ([]byte, error) {
	if size <= 0 || uint64(size) > uint64(^uint(0)>>1) {
		return nil, errors.New("行索引超过当前平台地址空间")
	}
	handle, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil, syscall.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(handle)
	address, err := syscall.MapViewOfFile(handle, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		return nil, err
	}
	// MapViewOfFile returns a native address, not a Go allocation. Populate an
	// actual slice descriptor (never a detached SliceHeader) so vet/checkptr
	// do not treat the native handle as Go pointer arithmetic.
	var data []byte
	header := (*reflect.SliceHeader)(unsafe.Pointer(&data))
	header.Data = address
	header.Len = int(size)
	header.Cap = int(size)
	return data, nil
}

func UnmapFile(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&data[0])))
}
