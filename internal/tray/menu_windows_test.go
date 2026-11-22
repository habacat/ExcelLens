//go:build windows

package tray

import (
	"syscall"
	"testing"
	"unsafe"
)

func TestNativeMenuReflectsToolbarChoiceAndCacheActions(t *testing.T) {
	getState := user32.NewProc("GetMenuState")
	getName := user32.NewProc("GetMenuStringW")
	for _, ribbon := range []bool{false, true} {
		menu, _, err := pCreatePopupMenu.Call()
		if menu == 0 {
			t.Fatal(err)
		}
		func() {
			defer pDestroyMenu.Call(menu)
			shown := appendTrayCommands(menu, Options{RibbonLayout: func() (bool, error) { return ribbon, nil }})
			state, _, _ := getState.Call(menu, menuRibbonLayout, 0)
			if shown != ribbon || (state&mfChecked != 0) != ribbon {
				t.Fatalf("incorrect layout check for %t: %x", ribbon, state)
			}
			for id, want := range map[uintptr]string{menuRibbonLayout: "选项卡布局", menuOpenProgram: "打开程序所在目录", menuClearAllCaches: "清除所有缓存（含当前）...", menuClearCache: "清理历史缓存（保留当前）..."} {
				var buf [96]uint16
				length, _, _ := getName.Call(menu, id, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
				if length == 0 || syscall.UTF16ToString(buf[:]) != want {
					t.Fatalf("menu %d label %q, want %q", id, syscall.UTF16ToString(buf[:]), want)
				}
			}
		}()
	}
}
