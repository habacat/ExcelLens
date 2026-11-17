//go:build windows

package tray

import (
	"excellens/internal/workbook"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	wmCopyData          = 0x004A
	smtoAbortIfHung     = 0x0002
	openWorkbookDataTag = 0x584C454E
)

type copyDataStruct struct {
	data uintptr
	size uint32
	ptr  uintptr
}

var pFindWindow = user32.NewProc("FindWindowW")
var pSendMessageTimeout = user32.NewProc("SendMessageTimeoutW")

// ForwardWorkbook opens an Explorer-context-menu file in the existing tray
// process instead of creating a second server and second tray icon.
func ForwardWorkbook(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if !workbook.SupportedExtensions(ext) {
		return false
	}
	class := utf16ptr("ExcelLensHiddenTrayWindow")
	hwnd, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(class)), 0)
	if hwnd == 0 {
		return false
	}
	u, err := syscall.UTF16FromString(path)
	if err != nil {
		return false
	}
	// The receiver limits IPC payloads to 64 KiB. Reject oversized paths
	// before narrowing the byte count to the DWORD field in COPYDATASTRUCT.
	if len(u) < 2 || len(u) > 32768 {
		return false
	}
	message := copyDataStruct{data: openWorkbookDataTag, size: uint32(len(u) * 2), ptr: uintptr(unsafe.Pointer(&u[0]))}
	var result uintptr
	success, _, _ := pSendMessageTimeout.Call(hwnd, wmCopyData, 0, uintptr(unsafe.Pointer(&message)), smtoAbortIfHung, 3000, uintptr(unsafe.Pointer(&result)))
	// Both Go-owned buffers are still used by the synchronous Windows call.
	runtime.KeepAlive(u)
	runtime.KeepAlive(&message)
	return success != 0 && result == 1
}
func receiveWorkbookCopyData(lparam uintptr) bool {
	if activeTray == nil || activeTray.options.OnChooseFile == nil || lparam == 0 {
		return false
	}
	// WM_COPYDATA's LPARAM and lpData are Win32-owned addresses, not Go
	// pointers. ReadProcessMemory validates both ranges before copying
	// them into Go-owned memory. This also prevents malformed messages
	// from crashing the process on an invalid address.
	var packet copyDataStruct
	if !readLocalMemory(lparam, unsafe.Pointer(&packet), unsafe.Sizeof(packet)) {
		return false
	}
	if packet.data != openWorkbookDataTag || packet.ptr == 0 || packet.size < 4 || packet.size > 65536 || packet.size%2 != 0 {
		return false
	}
	copied := make([]uint16, int(packet.size/2))
	if !readLocalMemory(packet.ptr, unsafe.Pointer(&copied[0]), uintptr(packet.size)) {
		return false
	}
	path := syscall.UTF16ToString(copied)
	ext := strings.ToLower(filepath.Ext(path))
	if !workbook.SupportedExtensions(ext) {
		return false
	}
	callback := activeTray.options.OnChooseFile
	go func() { alertErr(callback(path)) }()
	return true
}

// ForwardOpen activates the existing viewer for a plain EXE double-click.
// Posting a Win32 message instead of opening a fixed port preserves the
// existing random loopback port and bearer token, and creates no second tray.
func ForwardOpen() bool {
	class := utf16ptr("ExcelLensHiddenTrayWindow")
	hwnd, _, _ := pFindWindow.Call(uintptr(unsafe.Pointer(class)), 0)
	if hwnd == 0 {
		return false
	}
	var result uintptr
	success, _, _ := pSendMessageTimeout.Call(hwnd, wmAppActivateViewer, 0, 0,
		smtoAbortIfHung, 3000, uintptr(unsafe.Pointer(&result)))
	return success != 0 && result == 1
}
