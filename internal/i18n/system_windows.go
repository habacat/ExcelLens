package i18n

import (
	"slices"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getUserPreferredUILanguages = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetUserPreferredUILanguages")

func systemLanguages() []string {
	const languageName = 0x8 // MUI_LANGUAGE_NAME
	var count, length uint32
	ok, _, _ := getUserPreferredUILanguages.Call(languageName, uintptr(unsafe.Pointer(&count)), 0, uintptr(unsafe.Pointer(&length)))
	if ok == 0 || length == 0 || length > 8192 {
		return nil
	}
	buffer := make([]uint16, length)
	ok, _, _ = getUserPreferredUILanguages.Call(
		languageName, uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&length)),
	)
	if ok == 0 {
		return nil
	}
	var tags []string
	for len(buffer) > 0 && buffer[0] != 0 {
		end := slices.Index(buffer, 0)
		if end < 0 {
			break
		}
		tags = append(tags, syscall.UTF16ToString(buffer[:end]))
		buffer = buffer[end+1:]
	}
	return tags
}
