//go:build windows

// Package tray implements the Win32 notification-area UI with the Go standard
// library. The EXE has no CGO or external runtime dependencies.
package tray

import (
	"context"

	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unicode/utf16"
	"unsafe"

	"excellens/internal/branding"
	"excellens/internal/formats"
	"excellens/internal/i18n"
)

const (
	wmClose             = 0x0010
	wmDestroy           = 0x0002
	wmNull              = 0x0000
	wmLButtonUp         = 0x0202
	wmLButtonDblClk     = 0x0203
	wmRButtonUp         = 0x0205
	wmAppTray           = 0x8001
	wmAppActivateViewer = 0x8002
	csDblClks           = 0x0008
	wsPopup             = 0x80000000
	imageIcon           = 1
	lrLoadFromFile      = 0x10
	lrDefaultSize       = 0x40
	nifMessage          = 1
	nifIcon             = 2
	nifTip              = 4
	nimAdd              = 0
	nimDelete           = 2
	mfString            = 0
	mfChecked           = 8
	mfSeparator         = 0x800
	tpmRightButton      = 0x2
	tpmNonotify         = 0x80
	tpmReturnCmd        = 0x100
	swpNoSize           = 0x1
	swpNoZOrder         = 0x4
	swpNoActivate       = 0x10
	mbYesNo             = 0x4
	mbIconQuestion      = 0x20
	mbIconInfo          = 0x40
	mbIconError         = 0x10
	mbDefaultButton2    = 0x100
	idYes               = 6
	cfUnicodeText       = 13
	gmemMoveable        = 2
	ofnExplorer         = 0x00080000
	ofnPathMustExist    = 0x00000800
	ofnFileMustExist    = 0x00001000
	menuOpen            = 1001
	menuChoose          = 1002
	menuOpenCache       = 1003
	menuOpenTemp        = 1004
	menuOpenLog         = 1005
	menuCopyURL         = 1006
	menuClearCache      = 1007
	menuClearTemp       = 1008
	menuAbout           = 1009
	menuQuit            = 1010
	menuOpenExports     = 1011
	menuOpenNotes       = 1012
	menuInstallShell    = 1013
	menuRemoveShell     = 1014
	menuOpenLayouts     = 1015
	menuRibbonLayout    = 1016
	menuOpenProgram     = 1017
	menuClearAllCaches  = 1018
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	shell32              = syscall.NewLazyDLL("shell32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	comdlg32             = syscall.NewLazyDLL("comdlg32.dll")
	pGetModuleHandle     = kernel32.NewProc("GetModuleHandleW")
	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pDestroyWindow       = user32.NewProc("DestroyWindow")
	pPostMessage         = user32.NewProc("PostMessageW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pGetMessage          = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pLoadImage           = user32.NewProc("LoadImageW")
	pDestroyIcon         = user32.NewProc("DestroyIcon")
	pCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	pAppendMenu          = user32.NewProc("AppendMenuW")
	pDestroyMenu         = user32.NewProc("DestroyMenu")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
	pSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	pSetWindowPos        = user32.NewProc("SetWindowPos")
	pTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	pMessageBox          = user32.NewProc("MessageBoxW")
	pGetOpenFileName     = comdlg32.NewProc("GetOpenFileNameW")
	pCommDlgError        = comdlg32.NewProc("CommDlgExtendedError")
	pShellNotifyIcon     = shell32.NewProc("Shell_NotifyIconW")
	pOpenClipboard       = user32.NewProc("OpenClipboard")
	pEmptyClipboard      = user32.NewProc("EmptyClipboard")
	pSetClipboardData    = user32.NewProc("SetClipboardData")
	pCloseClipboard      = user32.NewProc("CloseClipboard")
	pGlobalAlloc         = kernel32.NewProc("GlobalAlloc")
	pGlobalLock          = kernel32.NewProc("GlobalLock")
	pGlobalUnlock        = kernel32.NewProc("GlobalUnlock")
	pGlobalFree          = kernel32.NewProc("GlobalFree")
	windowHandle         atomic.Uintptr
	activeTray           *trayState
)

type trayState struct {
	hwnd    uintptr
	icon    uintptr
	data    notifyIconData
	options Options
}

type windowClass struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	SmallIcon  uintptr
}
type notifyIconData struct {
	Size             uint32
	Hwnd             uintptr
	ID               uint32
	Flags            uint32
	CallbackMessage  uint32
	Icon             uintptr
	Tip              [128]uint16
	State            uint32
	StateMask        uint32
	Info             [256]uint16
	TimeoutOrVersion uint32
	InfoTitle        [64]uint16
	InfoFlags        uint32
	GUID             [16]byte
	BalloonIcon      uintptr
}
type point struct{ X, Y int32 }
type message struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}
type openFileName struct {
	Size            uint32
	Owner           uintptr
	Instance        uintptr
	Filter          *uint16
	CustomFilter    *uint16
	MaxCustomFilter uint32
	FilterIndex     uint32
	File            *uint16
	MaxFile         uint32
	FileTitle       *uint16
	MaxFileTitle    uint32
	InitialDir      *uint16
	Title           *uint16
	Flags           uint32
	FileOffset      uint16
	FileExtension   uint16
	DefaultExt      *uint16
	CustomData      uintptr
	Hook            uintptr
	TemplateName    *uint16
	Reserved        uintptr
	ReservedDWORD   uint32
	FlagsEx         uint32
}

func utf16ptr(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }
func messageBox(hwnd uintptr, title, body string, flags uintptr) uintptr {
	result, _, _ := pMessageBox.Call(hwnd, uintptr(unsafe.Pointer(utf16ptr(body))), uintptr(unsafe.Pointer(utf16ptr(title))), flags)
	return result
}
func alertErr(locale i18n.Locale, err error) {
	if err != nil {
		messageBox(0, "ExcelLens", locale.ErrorText(err), mbIconError)
	}
}
func callAction(locale i18n.Locale, fn func() error) {
	if fn == nil {
		return
	}
	go func() { alertErr(locale, fn()) }()
}
func cleanAction(hwnd uintptr, locale i18n.Locale, prompt string, fn func() (string, error)) {
	if fn == nil {
		return
	}
	if messageBox(hwnd, locale.Text("native.cleanup.title"), prompt, mbYesNo|mbIconQuestion|mbDefaultButton2) != idYes {
		return
	}
	go func() {
		info, err := fn()
		if err != nil {
			messageBox(0, "ExcelLens", strings.TrimSpace(info+"\n"+locale.ErrorText(err)), mbIconError)
			return
		}
		messageBox(0, "ExcelLens", info, mbIconInfo)
	}()
}

func chooseWorkbook(locale i18n.Locale) (string, error) {
	var filename [32768]uint16
	// Windows common-dialog filter strings are pairs separated by NUL and
	// terminated by two NULs, not a single ordinary UTF-16 C string.
	filter := utf16.Encode([]rune(formats.DialogFilter(locale.Text("native.dialog.supportedFiles"), locale.Text("native.dialog.allFiles"))))
	ofn := openFileName{
		Size: uint32(unsafe.Sizeof(openFileName{})), Filter: &filter[0], FilterIndex: 1,
		File: &filename[0], MaxFile: uint32(len(filename)), Title: utf16ptr(locale.Text("native.dialog.openDataFile")),
		Flags: ofnExplorer | ofnPathMustExist | ofnFileMustExist,
	}
	ok, _, err := pGetOpenFileName.Call(uintptr(unsafe.Pointer(&ofn)))
	runtime.KeepAlive(filter)
	if ok != 0 {
		return syscall.UTF16ToString(filename[:]), nil
	}
	code, _, _ := pCommDlgError.Call()
	if code == 0 {
		return "", nil
	} // user pressed Cancel
	return "", i18n.Error("diagnostic.tray.trayWindows.windowsFileSelectorFailedOpen", i18n.Args{"code": fmt.Sprintf("%x", code), "detail": err})
}

func copyToClipboard(hwnd uintptr, value string) error {
	data := syscall.StringToUTF16(value)
	if len(data) < 2 {
		return i18n.Error("diagnostic.tray.trayWindows.accessAddressEmpty")
	}
	ok, _, err := pOpenClipboard.Call(hwnd)
	if ok == 0 {
		return i18n.Wrap("diagnostic.tray.trayWindows.unableOpenClipboard", err)
	}
	defer pCloseClipboard.Call()
	if result, _, err := pEmptyClipboard.Call(); result == 0 {
		return i18n.Wrap("diagnostic.tray.trayWindows.unableClearClipboard", err)
	}
	bytes := uintptr(len(data) * 2)
	mem, _, err := pGlobalAlloc.Call(gmemMoveable, bytes)
	if mem == 0 {
		return i18n.Wrap("diagnostic.tray.trayWindows.failedAllocateClipboardBuffer", err)
	}
	ptr, _, err := pGlobalLock.Call(mem)
	if ptr == 0 {
		pGlobalFree.Call(mem)
		return i18n.Wrap("diagnostic.tray.trayWindows.failedLockClipboardBuffer", err)
	}
	// GlobalLock returns an address as uintptr. Do not reinterpret it as
	// a Go pointer; copy the UTF-16 bytes through a checked Win32 API.
	if err := writeLocalMemory(ptr, unsafe.Pointer(&data[0]), bytes); err != nil {
		pGlobalUnlock.Call(mem)
		pGlobalFree.Call(mem)
		return i18n.Wrap("diagnostic.tray.trayWindows.failedWriteClipboardBuffer", err)
	}
	pGlobalUnlock.Call(mem)
	if ok, _, err := pSetClipboardData.Call(cfUnicodeText, mem); ok == 0 {
		pGlobalFree.Call(mem)
		return i18n.Wrap("diagnostic.tray.trayWindows.failedCopyAccessAddress", err)
	}
	return nil // Windows owns the memory now
}
func appendMenu(menu uintptr, id uint32, label string) {
	pAppendMenu.Call(menu, mfString, uintptr(id), uintptr(unsafe.Pointer(utf16ptr(label))))
}
func appendSeparator(menu uintptr) { pAppendMenu.Call(menu, mfSeparator, 0, 0) }

func positionMenuOwner(hwnd uintptr, cursor point) {
	// A tray owner is hidden at (0,0). Move it to the menu's monitor so native
	// menus and dialogs inherit that monitor's DPI, including mixed-DPI setups.
	pSetWindowPos.Call(hwnd, 0, uintptr(int64(cursor.X)), uintptr(int64(cursor.Y)), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate)
}

func appendTrayCommands(menu uintptr, options Options) bool {
	locale := options.language()
	appendMenu(menu, menuOpen, locale.Text("native.tray.open"))
	appendMenu(menu, menuChoose, locale.Text("native.tray.openFile"))
	appendLanguageMenu(menu, options, locale)
	var ribbon bool
	if options.RibbonLayout != nil {
		var err error
		ribbon, err = options.RibbonLayout()
		flags := uintptr(mfString)
		if ribbon {
			flags |= mfChecked
		}
		if err != nil {
			log.Printf("读取工具栏布局失败：%v", err)
		}
		pAppendMenu.Call(menu, flags, menuRibbonLayout, uintptr(unsafe.Pointer(utf16ptr(locale.Text("native.tray.ribbonLayout")))))
	}
	appendSeparator(menu)
	appendMenu(menu, menuOpenProgram, locale.Text("native.tray.programFolder"))
	appendMenu(menu, menuOpenCache, locale.Text("native.tray.cacheFolder"))
	appendMenu(menu, menuOpenTemp, locale.Text("native.tray.tempFolder"))
	appendMenu(menu, menuOpenLog, locale.Text("native.tray.logFolder"))
	appendMenu(menu, menuOpenExports, locale.Text("native.tray.exportsFolder"))
	appendMenu(menu, menuOpenNotes, locale.Text("native.tray.annotationsFolder"))
	appendMenu(menu, menuOpenLayouts, locale.Text("native.tray.layoutFolder"))
	appendMenu(menu, menuCopyURL, locale.Text("native.tray.copyAddress"))
	appendSeparator(menu)
	if options.ContextMenuInstalled != nil && options.ContextMenuInstalled() {
		appendMenu(menu, menuRemoveShell, locale.Text("native.tray.removeShell"))
	} else {
		appendMenu(menu, menuInstallShell, locale.Text("native.tray.addShell"))
	}
	appendSeparator(menu)
	appendMenu(menu, menuClearCache, locale.Text("native.tray.clearUnusedCaches"))
	appendMenu(menu, menuClearAllCaches, locale.Text("native.tray.clearAllCaches"))
	appendMenu(menu, menuClearTemp, locale.Text("native.tray.clearTemporaryFiles"))
	appendSeparator(menu)
	appendMenu(menu, menuAbout, locale.Text("native.tray.about"))
	appendMenu(menu, menuQuit, locale.Text("native.tray.exit"))
	return ribbon
}

func showMenu(hwnd uintptr) {
	if activeTray == nil {
		return
	}
	var cursor point
	if ok, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor))); ok == 0 {
		return
	}
	positionMenuOwner(hwnd, cursor)
	menu, _, _ := pCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer pDestroyMenu.Call(menu)
	options := activeTray.options
	locale := options.language()
	ribbon := appendTrayCommands(menu, options)
	pSetForegroundWindow.Call(hwnd)
	command, _, _ := pTrackPopupMenu.Call(menu, tpmRightButton|tpmNonotify|tpmReturnCmd,
		uintptr(int64(cursor.X)), uintptr(int64(cursor.Y)), 0, hwnd, 0)
	if choice, ok := languageCommand(command); ok && options.SetLanguageChoice != nil {
		alertErr(locale, options.SetLanguageChoice(choice))
		pPostMessage.Call(hwnd, wmNull, 0, 0)
		return
	}
	switch command {
	case menuOpen:
		if options.OnOpen != nil {
			options.OnOpen()
		}
	case menuChoose:
		path, err := chooseWorkbook(locale)
		if err != nil {
			alertErr(locale, err)
		} else if path != "" && options.OnChooseFile != nil {
			go func() { alertErr(locale, options.OnChooseFile(path)) }()
		}
	case menuOpenCache:
		callAction(locale, options.OnOpenCache)
	case menuOpenProgram:
		callAction(locale, options.OnOpenProgram)
	case menuRibbonLayout:
		if options.SetRibbonLayout != nil {
			alertErr(locale, options.SetRibbonLayout(!ribbon))
		}
	case menuOpenTemp:
		callAction(locale, options.OnOpenTemp)
	case menuOpenLog:
		callAction(locale, options.OnOpenLog)
	case menuOpenExports:
		callAction(locale, options.OnOpenExports)
	case menuOpenNotes:
		callAction(locale, options.OnOpenNotes)
	case menuOpenLayouts:
		callAction(locale, options.OnOpenLayouts)
	case menuCopyURL:
		if err := copyToClipboard(hwnd, options.Address); err != nil {
			alertErr(locale, err)
		}
	case menuInstallShell:
		callAction(locale, func() error {
			if options.OnInstallContextMenu == nil {
				return nil
			}
			if err := options.OnInstallContextMenu(); err != nil {
				return err
			}
			messageBox(0, "ExcelLens", locale.Text("native.shell.added"), mbIconInfo)
			return nil
		})
	case menuRemoveShell:
		if messageBox(hwnd, "ExcelLens", locale.Text("native.shell.removePrompt"), mbYesNo|mbIconQuestion|mbDefaultButton2) == idYes {
			callAction(locale, func() error {
				if options.OnRemoveContextMenu == nil {
					return nil
				}
				if err := options.OnRemoveContextMenu(); err != nil {
					return err
				}
				messageBox(0, "ExcelLens", locale.Text("native.shell.removed"), mbIconInfo)
				return nil
			})
		}
	case menuClearCache:
		cleanAction(hwnd, locale, locale.Text("native.cleanup.unusedCachesPrompt"), options.OnClearCache)
	case menuClearTemp:
		cleanAction(hwnd, locale, locale.Text("native.cleanup.temporaryFilesPrompt"), options.OnClearTemp)
	case menuClearAllCaches:
		cleanAction(hwnd, locale, locale.Text("native.cleanup.allCachesPrompt"), options.OnClearAllCaches)
	case menuAbout:
		showAbout(hwnd, locale)
	case menuQuit:
		RequestQuit()
	}
	pPostMessage.Call(hwnd, wmNull, 0, 0)
}
func trayWindowProc(hwnd uintptr, msg uint32, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmAppActivateViewer:
		if activeTray == nil || activeTray.options.OnOpen == nil {
			return 0
		}
		go activeTray.options.OnOpen()
		return 1
	case wmCopyData:
		if receiveWorkbookCopyData(lparam) {
			return 1
		}
		return 0
	case wmAppTray:
		switch uint32(lparam) {
		case wmLButtonUp, wmLButtonDblClk:
			if activeTray != nil && activeTray.options.OnOpen != nil {
				activeTray.options.OnOpen()
			}
		case wmRButtonUp:
			showMenu(hwnd)
		}
		return 0
	case wmClose:
		pDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := pDefWindowProc.Call(hwnd, uintptr(msg), wparam, lparam)
	return ret
}
func RequestQuit() {
	if hwnd := windowHandle.Load(); hwnd != 0 {
		pPostMessage.Call(hwnd, wmClose, 0, 0)
	}
}

func Run(ctx context.Context, options Options) error {
	if ctx.Err() != nil {
		return nil
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := pGetModuleHandle.Call(0)
	className := utf16ptr("ExcelLensHiddenTrayWindow")
	class := windowClass{Size: uint32(unsafe.Sizeof(windowClass{})), Style: csDblClks, WndProc: syscall.NewCallback(trayWindowProc), Instance: instance, ClassName: className}
	if result, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); result == 0 {
		return fmt.Errorf("register tray window: %w", err)
	}
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16ptr("ExcelLens"))), wsPopup, 0, 0, 1, 1, 0, 0, instance, 0)
	if hwnd == 0 {
		return fmt.Errorf("create tray window: %w", err)
	}
	iconFile, err := os.CreateTemp("", "excellens-*.ico")
	if err != nil {
		pDestroyWindow.Call(hwnd)
		return err
	}
	iconPath := iconFile.Name()
	defer os.Remove(iconPath)
	if _, err = iconFile.Write(branding.IconICO()); err != nil {
		iconFile.Close()
		pDestroyWindow.Call(hwnd)
		return err
	}
	if err = iconFile.Close(); err != nil {
		pDestroyWindow.Call(hwnd)
		return err
	}
	icon, _, err := pLoadImage.Call(0, uintptr(unsafe.Pointer(utf16ptr(iconPath))), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize)
	if icon == 0 {
		pDestroyWindow.Call(hwnd)
		return fmt.Errorf("load tray icon: %w", err)
	}
	defer pDestroyIcon.Call(icon)
	state := &trayState{hwnd: hwnd, icon: icon, options: options}
	state.data = notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Hwnd: hwnd, ID: 1, Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmAppTray, Icon: icon}
	copy(state.data.Tip[:], syscall.StringToUTF16("ExcelLens"))
	if ok, _, err := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&state.data))); ok == 0 {
		pDestroyWindow.Call(hwnd)
		return fmt.Errorf("add notification icon: %w", err)
	}
	activeTray = state
	windowHandle.Store(hwnd)
	defer func() {
		windowHandle.Store(0)
		pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&state.data)))
		activeTray = nil
	}()
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			RequestQuit()
		case <-closed:
		}
	}()
	var msg message
	for {
		code, _, e := pGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if code == 0 {
			return nil
		}
		if code == ^uintptr(0) {
			return fmt.Errorf("tray message pump: %w", e)
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&msg)))
	}
}
