//go:build windows

package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

func shortCacheTestPath(t *testing.T, path string) string {
	t.Helper()
	input, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, 32768)
	n, _, callErr := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW").Call(uintptr(unsafe.Pointer(input)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 || n >= uintptr(len(buf)) {
		t.Fatal(callErr)
	}
	return syscall.UTF16ToString(buf[:n])
}

func TestFullCacheResetProtectsWindowsShortAndLongNames(t *testing.T) {
	isolateTestStorage(t)
	dir := filepath.Join(t.TempDir(), "Long directory name for ExcelLens cache")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	long, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	short := shortCacheTestPath(t, long)
	if strings.EqualFold(short, long) {
		t.Skip("NTFS did not allocate an 8.3 alias for this temporary directory")
	}
	group := filepath.Join(long, "ExcelLens", "cache", strings.Repeat("b", 64))
	if err = os.MkdirAll(group, 0700); err != nil {
		t.Fatal(err)
	}
	// Use an extension that retains its format in an 8.3 filename.
	source := filepath.Join(group, "Long source workbook name.csv")
	original := []byte("id,value\n001,42\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ home, source string }{{short, source}, {long, shortCacheTestPath(t, source)}} {
		t.Setenv("LOCALAPPDATA", tc.home)
		app, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if err = app.OpenPath(tc.source); err != nil {
			t.Fatal(err)
		}
		wb := app.currentWorkbook()
		if _, err = wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
			t.Fatal(err)
		}
		_, err = app.ClearAllCaches()
		app.Close()
		if err != nil {
			t.Fatal(err)
		}
		if data, err := os.ReadFile(source); err != nil || !bytes.Equal(original, data) {
			t.Fatal("cache cleanup changed source bytes", err)
		}
	}
}
