package tray

import (
	"unsafe"

	"excellens/internal/i18n"
)

const menuLanguageFirst = 1100
const mfPopup = 0x10

var pCheckMenuRadioItem = user32.NewProc("CheckMenuRadioItem")

func appendLanguageMenu(menu uintptr, options Options, locale i18n.Locale) uintptr {
	if options.SetLanguageChoice == nil || options.LanguageChoice == nil {
		return 0
	}
	submenu, _, _ := pCreatePopupMenu.Call()
	if submenu == 0 {
		return 0
	}
	choices := i18n.Choices()
	appendMenu(submenu, menuLanguageFirst, locale.Text("native.languageMenu.automatic"))
	selected := uintptr(menuLanguageFirst)
	setting := options.LanguageChoice()
	for index, choice := range choices {
		command := uintptr(menuLanguageFirst + index + 1)
		appendMenu(submenu, uint32(command), choice.Name)
		if choice.Locale == setting {
			selected = command
		}
	}
	pCheckMenuRadioItem.Call(submenu, menuLanguageFirst, uintptr(menuLanguageFirst+len(choices)), selected, 0)
	title := utf16ptr(locale.Text("native.languageMenu.title"))
	if ok, _, _ := pAppendMenu.Call(menu, mfPopup, submenu, uintptr(unsafe.Pointer(title))); ok == 0 {
		pDestroyMenu.Call(submenu)
		return 0
	}
	return submenu
}

func languageCommand(command uintptr) (i18n.Locale, bool) {
	if command == menuLanguageFirst {
		return "", true
	}
	index := command - menuLanguageFirst - 1
	choices := i18n.Choices()
	if index >= uintptr(len(choices)) {
		return "", false
	}
	return choices[index].Locale, true
}
