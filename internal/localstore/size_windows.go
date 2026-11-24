//go:build windows

package localstore

import (
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

var standardInfo = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")

func allocatedSize(root *os.Root, name string, _ fs.FileInfo) (int64, error) {
	f, err := root.Open(name)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	// FILE_STANDARD_INFO starts with allocation size and end-of-file.
	var info struct {
		Allocation, EOF          int64
		Links                    uint32
		DeletePending, Directory byte
		Padding                  [2]byte
	}
	ok, _, cause := standardInfo.Call(f.Fd(), 1, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		return 0, cause
	}
	return info.Allocation, nil
}
