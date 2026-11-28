package tray

import (
	"syscall"
	"testing"
	"unsafe"

	"excellens/internal/i18n"
)

func TestLanguageSubmenuUsesNamesAndSavedChoice(t *testing.T) {
	menu, _, err := pCreatePopupMenu.Call()
	if menu == 0 {
		t.Fatal(err)
	}
	defer pDestroyMenu.Call(menu)
	submenu := appendLanguageMenu(menu, Options{
		LanguageChoice:    func() i18n.Locale { return "ja" },
		SetLanguageChoice: func(i18n.Locale) error { return nil },
	}, "en")
	if submenu == 0 {
		t.Fatal("language submenu was not added")
	}
	getName := user32.NewProc("GetMenuStringW")
	getState := user32.NewProc("GetMenuState")
	for i, choice := range i18n.Choices() {
		id := uintptr(menuLanguageFirst + i + 1)
		var buf [96]uint16
		n, _, _ := getName.Call(submenu, id, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		state, _, _ := getState.Call(submenu, id, 0)
		locale, ok := languageCommand(id)
		if n == 0 || syscall.UTF16ToString(buf[:]) != choice.Name || state == 0xffffffff ||
			(state&mfChecked != 0) != (choice.Locale == "ja") || !ok || locale != choice.Locale {
			t.Fatalf("incorrect submenu item for %s", choice.Locale)
		}
	}
	if locale, ok := languageCommand(menuLanguageFirst); !ok || locale != "" {
		t.Fatal("automatic language must clear the preference")
	}
	if _, ok := languageCommand(menuLanguageFirst - 1); ok {
		t.Fatal("an unrelated tray command was accepted as a language")
	}
}
