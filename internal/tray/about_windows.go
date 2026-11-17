//go:build windows

package tray

import (
	"fmt"
	"syscall"
	"unsafe"

	"excellens/internal/buildinfo"
)

var pTaskDialog = syscall.NewLazyDLL("comctl32.dll").NewProc("TaskDialog")

const (
	tdcbfOKButton             = 0x1
	applicationIconResourceID = 1
)

func showAbout(hwnd uintptr) {
	content := fmt.Sprintf("版本 %s（64 位）\n\n只读数据查看与分析工具", buildinfo.Version)
	if pTaskDialog.Find() == nil {
		instance, _, _ := pGetModuleHandle.Call(0)
		// RT_GROUP_ICON #1 is the application icon embedded by tools/icon.
		result, _, _ := pTaskDialog.Call(hwnd, instance,
			uintptr(unsafe.Pointer(utf16ptr("关于 ExcelLens"))),
			uintptr(unsafe.Pointer(utf16ptr("ExcelLens"))),
			uintptr(unsafe.Pointer(utf16ptr(content))), tdcbfOKButton, applicationIconResourceID, 0)
		if result == 0 {
			return
		}
	}
	messageBox(hwnd, "关于 ExcelLens", "ExcelLens\n"+content, mbIconInfo)
}
