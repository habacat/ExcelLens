//go:build windows

package tray

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"excellens/internal/buildinfo"
)

func TestMenuOwnerUsesPhysicalCoordinatesWithoutShowingOrActivating(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setContext := user32.NewProc("SetThreadDpiAwarenessContext")
	if err := setContext.Find(); err != nil {
		t.Skip("per-monitor DPI contexts are unavailable on this Windows version")
	}
	// Test only an invisible, test-owned window; no tray icon or user UI opens.
	const perMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
	previous, _, err := setContext.Call(perMonitorV2)
	if previous == 0 {
		t.Fatalf("SetThreadDpiAwarenessContext: %v", err)
	}
	defer setContext.Call(previous)
	instance, _, _ := pGetModuleHandle.Call(0)
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(utf16ptr("STATIC"))),
		uintptr(unsafe.Pointer(utf16ptr("ExcelLens DPI regression"))), wsPopup, 0, 0, 1, 1, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Fatalf("create hidden test window: %v", err)
	}
	defer pDestroyWindow.Call(hwnd)
	foreground := user32.NewProc("GetForegroundWindow")
	before, _, _ := foreground.Call()
	for _, cursor := range []point{{200, 150}, {-1800, 120}, {2500, -600}} {
		positionMenuOwner(hwnd, cursor)
		var rect struct{ Left, Top, Right, Bottom int32 }
		if ok, _, err := user32.NewProc("GetWindowRect").Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
			t.Fatalf("GetWindowRect: %v", err)
		}
		if rect.Left != cursor.X || rect.Top != cursor.Y || rect.Right-rect.Left != 1 || rect.Bottom-rect.Top != 1 {
			t.Fatalf("hidden owner changed coordinates or size: cursor=%+v rect=%+v", cursor, rect)
		}
		if visible, _, _ := user32.NewProc("IsWindowVisible").Call(hwnd); visible != 0 {
			t.Fatal("positioning made the hidden owner visible")
		}
		if current, _, _ := foreground.Call(); current != before {
			t.Fatal("positioning activated a window")
		}
	}
}

func TestWindowsAcceptsManifestAndResolvesNativeAboutDialog(t *testing.T) {
	manifest, err := os.ReadFile("../../cmd/excellens/app.manifest")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "app.manifest")
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(manifest), "__APP_VERSION__", buildinfo.Version)), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	type actctx struct {
		Size, Flags        uint32
		Source             *uint16
		Arch, Language     uint16
		Assembly, Resource *uint16
		Application        *uint16
		Module             uintptr
	}
	context := actctx{Size: uint32(unsafe.Sizeof(actctx{})), Source: utf16ptr(path)}
	handle, _, err := kernel32.NewProc("CreateActCtxW").Call(uintptr(unsafe.Pointer(&context)))
	runtime.KeepAlive(context)
	if handle == ^uintptr(0) {
		t.Fatalf("Windows rejected the application manifest: %v", err)
	}
	defer kernel32.NewProc("ReleaseActCtx").Call(handle)
	var cookie uintptr
	if ok, _, err := kernel32.NewProc("ActivateActCtx").Call(handle, uintptr(unsafe.Pointer(&cookie))); ok == 0 {
		t.Fatalf("ActivateActCtx: %v", err)
	}
	defer kernel32.NewProc("DeactivateActCtx").Call(0, cookie)
	dll, err := syscall.LoadDLL("comctl32.dll")
	if err != nil {
		t.Fatal(err)
	}
	defer dll.Release()
	if _, err := dll.FindProc("TaskDialog"); err != nil {
		t.Fatalf("manifest did not activate native TaskDialog support: %v", err)
	}
}
