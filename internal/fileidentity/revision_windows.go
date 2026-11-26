//go:build windows

package fileidentity

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

var fingerprintGetFileInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")

func Open(path string) (*os.File, bool, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err == nil {
		// Denying write sharing makes the memo check coherent even when an
		// application's write timestamps would otherwise be deferred until close.
		// A live writer makes this open fail; we then use the original full-hash
		// path without memoization. Never change a writer's permissions or data.
		handle, openErr := syscall.CreateFile(name, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
		if openErr == nil {
			return os.NewFile(uintptr(handle), path), true, nil
		}
	}
	f, err := os.Open(path)
	return f, false, err
}

type basicFileInfo struct {
	Created, Accessed, Written, Changed int64
	Attributes, Padding                 uint32
}

func basicInfo(f *os.File) (basicFileInfo, bool) {
	defer runtime.KeepAlive(f)
	var basic basicFileInfo
	ok, _, _ := fingerprintGetFileInformation.Call(f.Fd(), 0, uintptr(unsafe.Pointer(&basic)), unsafe.Sizeof(basic))
	return basic, ok != 0 && basic.Changed > 0 && basic.Written > 0
}

// ChangeHint detects metadata changes even on volumes without a readable USN.
// It is not sufficient to key the content digest memo.
func ChangeHint(f *os.File, _ os.FileInfo) int64 {
	basic, _ := basicInfo(f)
	return basic.Changed
}

func Revision(f *os.File, info os.FileInfo) (Stamp, bool) {
	defer runtime.KeepAlive(f)
	basic, valid := basicInfo(f)
	if !valid {
		return Stamp{}, false
	}
	var id struct {
		Volume uint64
		ID     [16]byte
	}
	ok, _, _ := fingerprintGetFileInformation.Call(f.Fd(), 18, uintptr(unsafe.Pointer(&id)), unsafe.Sizeof(id))
	if ok == 0 || id.ID == [16]byte{} {
		return Stamp{}, false
	}
	revision, err := fingerprintFileRevision(f)
	if err != nil {
		return Stamp{}, false
	}
	return Stamp{Volume: id.Volume, ID: id.ID, Created: basic.Created, Modified: basic.Written, Changed: basic.Changed, Size: info.Size(), Revision: revision}, true
}

func fingerprintFileRevision(f *os.File) (int64, error) {
	defer runtime.KeepAlive(f)
	// NTFS change timestamps can repeat for fast same-size rewrites. A file
	// USN is required as a separate monotonic revision; timestamps alone are
	// insufficient evidence. Query only: never enable or change a journal.
	const fsctlReadFileUSNData = 0x000900eb
	var data [4096]byte
	var returned uint32
	if err := syscall.DeviceIoControl(syscall.Handle(f.Fd()), fsctlReadFileUSNData, nil, 0, &data[0], uint32(len(data)), &returned, nil); err != nil {
		return 0, err
	}
	if returned > uint32(len(data)) {
		return 0, fmt.Errorf("invalid file revision size %d", returned)
	}
	revision, valid := fingerprintUSN(data[:returned])
	if !valid {
		return 0, fmt.Errorf("unsupported file revision record (%d bytes, header %x)", returned, data[:min(returned, 8)])
	}
	return revision, nil
}

func fingerprintUSN(record []byte) (int64, bool) {
	if len(record) < 8 {
		return 0, false
	}
	length := int(binary.LittleEndian.Uint32(record))
	if length > len(record) {
		return 0, false
	}
	var offset, minimum int
	switch binary.LittleEndian.Uint16(record[4:]) {
	case 2:
		offset, minimum = 24, 60
	case 3:
		offset, minimum = 40, 76
	default:
		return 0, false
	}
	if length < minimum {
		return 0, false
	}
	usn := int64(binary.LittleEndian.Uint64(record[offset:]))
	return usn, usn > 0
}
