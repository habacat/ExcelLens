//go:build windows

package localstore

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getDiskFreeSpace = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetDiskFreeSpaceW")

func availableSpace(path string) (volumeSpace, error) {
	var v volumeSpace
	p, err := widePath(path)
	if err != nil {
		return v, err
	}
	if err = windows.GetDiskFreeSpaceEx(p, &v.free, nil, nil); err != nil {
		return v, err
	}
	mount := make([]uint16, 32768)
	if err = windows.GetVolumePathName(p, &mount[0], uint32(len(mount))); err != nil {
		return v, err
	}
	guid := make([]uint16, 128)
	if err = windows.GetVolumeNameForVolumeMountPoint(&mount[0], &guid[0], uint32(len(guid))); err == nil {
		v.id = strings.ToLower(windows.UTF16ToString(guid))
	} else {
		v.id = strings.ToLower(windows.UTF16ToString(mount))
	}
	var sectors, bytes, free, total uint32
	ok, _, err := getDiskFreeSpace.Call(uintptr(unsafe.Pointer(&mount[0])), uintptr(unsafe.Pointer(&sectors)), uintptr(unsafe.Pointer(&bytes)), uintptr(unsafe.Pointer(&free)), uintptr(unsafe.Pointer(&total)))
	if ok == 0 {
		if err == nil {
			err = syscall.EINVAL
		}
		return v, err
	}
	v.block = uint64(sectors) * uint64(bytes)
	return v, nil
}
