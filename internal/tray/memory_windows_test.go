//go:build windows

package tray

import (
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestWin32MemoryCopyValidAndInvalidAddresses(t *testing.T) {
	original := copyDataStruct{data: openWorkbookDataTag, size: 32, ptr: 12345}
	var got copyDataStruct
	if !readLocalMemory(uintptr(unsafe.Pointer(&original)), unsafe.Pointer(&got), unsafe.Sizeof(got)) {
		t.Fatal("failed to copy a valid Win32 message structure")
	}
	runtime.KeepAlive(&original)
	if got != original {
		t.Fatalf("memory copy did not preserve the structure: got %#v; want %#v", got, original)
	}
	if readLocalMemory(1, unsafe.Pointer(&got), unsafe.Sizeof(got)) {
		t.Fatal("invalid Win32 address was accepted")
	}
	if readLocalMemory(0, unsafe.Pointer(&got), unsafe.Sizeof(got)) {
		t.Fatal("zero Win32 address was accepted")
	}
}

func TestWin32GlobalMemoryWriteAndRead(t *testing.T) {
	original := []uint16{'E', 'x', 'c', 'e', 'l', 'L', 'e', 'n', 's', 0}
	bytes := uintptr(len(original) * 2)
	handle, _, err := pGlobalAlloc.Call(gmemMoveable, bytes)
	if handle == 0 {
		t.Fatalf("GlobalAlloc: %v", err)
	}
	defer pGlobalFree.Call(handle)
	address, _, err := pGlobalLock.Call(handle)
	if address == 0 {
		t.Fatalf("GlobalLock: %v", err)
	}
	defer pGlobalUnlock.Call(handle)
	if err := writeLocalMemory(address, unsafe.Pointer(&original[0]), bytes); err != nil {
		t.Fatal(err)
	}
	actual := make([]uint16, len(original))
	if !readLocalMemory(address, unsafe.Pointer(&actual[0]), bytes) {
		t.Fatal("failed to read back a Win32-allocated UTF-16 buffer")
	}
	for i := range original {
		if actual[i] != original[i] {
			t.Fatalf("mismatched UTF-16 code unit %d: got %04x; want %04x", i, actual[i], original[i])
		}
	}
	if err := writeLocalMemory(1, unsafe.Pointer(&original[0]), bytes); err == nil {
		t.Fatal("invalid destination address was accepted")
	}
}

func TestReceiveWorkbookCopyDataDoesNotDereferenceUntrustedPointers(t *testing.T) {
	previous := activeTray
	t.Cleanup(func() { activeTray = previous })
	opened := make(chan string, 1)
	activeTray = &trayState{options: Options{OnChooseFile: func(path string) error {
		opened <- path
		return nil
	}}}
	if receiveWorkbookCopyData(1) {
		t.Fatal("accepted an invalid WM_COPYDATA lparam")
	}
	invalid := copyDataStruct{data: openWorkbookDataTag, size: 8, ptr: 1}
	if receiveWorkbookCopyData(uintptr(unsafe.Pointer(&invalid))) {
		t.Fatal("accepted an invalid WM_COPYDATA lpData")
	}
	runtime.KeepAlive(&invalid)

	expected := `C:\资料\样本.xlsx`
	characters, err := syscall.UTF16FromString(expected)
	if err != nil {
		t.Fatal(err)
	}
	packet := copyDataStruct{
		data: openWorkbookDataTag,
		size: uint32(len(characters) * 2),
		ptr:  uintptr(unsafe.Pointer(&characters[0])),
	}
	if !receiveWorkbookCopyData(uintptr(unsafe.Pointer(&packet))) {
		t.Fatal("valid WM_COPYDATA was rejected")
	}
	runtime.KeepAlive(characters)
	runtime.KeepAlive(&packet)
	select {
	case path := <-opened:
		if path != expected {
			t.Fatalf("got %q; want %q", path, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WM_COPYDATA receiver did not invoke OnChooseFile")
	}
}
