//go:build windows

package tray

import (
	"syscall"
	"unsafe"

	"excellens/internal/buildinfo"
	"excellens/internal/i18n"
)

var pTaskDialog = syscall.NewLazyDLL("comctl32.dll").NewProc("TaskDialog")

const (
	tdcbfOKButton             = 0x1
	applicationIconResourceID = 1
)

func showAbout(hwnd uintptr, locale i18n.Locale) {
	title := locale.Text("native.tray.about")
	content := locale.Text("native.about.version", i18n.Args{"version": buildinfo.Version})
	if pTaskDialog.Find() == nil {
		instance, _, _ := pGetModuleHandle.Call(0)
		// RT_GROUP_ICON #1 is the application icon embedded by tools/icon.
		result, _, _ := pTaskDialog.Call(hwnd, instance,
			uintptr(unsafe.Pointer(utf16ptr(title))),
			uintptr(unsafe.Pointer(utf16ptr("ExcelLens"))),
			uintptr(unsafe.Pointer(utf16ptr(content))), tdcbfOKButton, applicationIconResourceID, 0)
		if result == 0 {
			return
		}
	}
	messageBox(hwnd, title, "ExcelLens\n"+content, mbIconInfo)
}
