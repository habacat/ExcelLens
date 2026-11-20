package workbook

import (
	"strings"
	"syscall"
	"unsafe"
)

// Excel's built-in short date follows the user's regional settings. Read the
// numeric pattern once, so formatting a column does not cross the OS boundary
// for every cell. Explicit workbook format codes take precedence.
var localShortDate = func() string {
	const localeShortDate = 0x1f
	var buf [128]uint16
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetLocaleInfoEx")
	n, _, _ := proc.Call(0, localeShortDate, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n < 2 || n > uintptr(len(buf)) {
		return "yyyy-mm-dd"
	}
	code := strings.ToLower(syscall.UTF16ToString(buf[:n]))
	// Only numeric dates have a locale-independent rendering of all fields.
	if !strings.Contains(code, "y") || !strings.Contains(code, "m") || !strings.Contains(code, "d") || strings.Contains(code, "ddd") || strings.Contains(code, "mmm") {
		return "yyyy-mm-dd"
	}
	for _, r := range code {
		if !strings.ContainsRune("ymd/-. 年月日", r) {
			return "yyyy-mm-dd"
		}
	}
	year := code[strings.IndexByte(code, 'y'):]
	yearDigits := len(year) - len(strings.TrimLeft(year, "y"))
	if yearDigits != 2 && yearDigits != 4 {
		return "yyyy-mm-dd"
	}
	return code
}()
