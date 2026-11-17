//go:build windows

// Package shellmenu registers an opt-in, per-user Explorer verb for supported files.
// It does not replace the user's default Excel/WPS application.
package shellmenu

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"excellens/internal/formats"
)

const (
	hkcu              = 0x80000001
	regSZ             = 1
	keyRead           = 0x20019
	keyWrite          = 0x20006
	shcneAssocChanged = 0x08000000
	shcnfIDList       = 0
)

var (
	advapi32        = syscall.NewLazyDLL("advapi32.dll")
	shell32         = syscall.NewLazyDLL("shell32.dll")
	regCreateKeyEx  = advapi32.NewProc("RegCreateKeyExW")
	regOpenKeyEx    = advapi32.NewProc("RegOpenKeyExW")
	regSetValueEx   = advapi32.NewProc("RegSetValueExW")
	regQueryValueEx = advapi32.NewProc("RegQueryValueExW")
	regDeleteTree   = advapi32.NewProc("RegDeleteTreeW")
	regCloseKey     = advapi32.NewProc("RegCloseKey")
	shChangeNotify  = shell32.NewProc("SHChangeNotify")
)

func keyPath(ext string) string {
	return `Software\Classes\SystemFileAssociations\` + ext + `\shell\ExcelLens`
}
func utf16(s string) (*uint16, error) { return syscall.UTF16PtrFromString(s) }
func createKey(path string) (uintptr, error) {
	p, err := utf16(path)
	if err != nil {
		return 0, err
	}
	var handle uintptr
	r, _, _ := regCreateKeyEx.Call(hkcu, uintptr(unsafe.Pointer(p)), 0, 0, 0, keyWrite, 0, uintptr(unsafe.Pointer(&handle)), 0)
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return handle, nil
}
func openKey(path string) (uintptr, error) {
	p, err := utf16(path)
	if err != nil {
		return 0, err
	}
	var handle uintptr
	r, _, _ := regOpenKeyEx.Call(hkcu, uintptr(unsafe.Pointer(p)), 0, keyRead, uintptr(unsafe.Pointer(&handle)))
	if r != 0 {
		return 0, syscall.Errno(r)
	}
	return handle, nil
}
func setString(key uintptr, name, value string) error {
	n, err := utf16(name)
	if err != nil {
		return err
	}
	data, err := syscall.UTF16FromString(value)
	if err != nil {
		return err
	}
	r, _, _ := regSetValueEx.Call(key, uintptr(unsafe.Pointer(n)), 0, regSZ,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}
func readString(key uintptr, name string) (string, error) {
	n, err := utf16(name)
	if err != nil {
		return "", err
	}
	var typ uint32
	var size uint32
	r, _, _ := regQueryValueEx.Call(key, uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", syscall.Errno(r)
	}
	if typ != regSZ || size > 65536 || size < 2 {
		return "", fmt.Errorf("无效注册表字符串")
	}
	data := make([]uint16, (size+1)/2)
	r, _, _ = regQueryValueEx.Call(key, uintptr(unsafe.Pointer(n)), 0, uintptr(unsafe.Pointer(&typ)), uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&size)))
	if r != 0 {
		return "", syscall.Errno(r)
	}
	return syscall.UTF16ToString(data), nil
}
func currentCommand() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Explorer's %1 is quoted to preserve spaces and Chinese path names.
	return `"` + exe + `" "%1"`, nil
}
func IsInstalled() bool {
	want, err := currentCommand()
	if err != nil {
		return false
	}
	for _, ext := range formats.Extensions() {
		key, err := openKey(keyPath(ext) + `\command`)
		if err != nil {
			return false
		}
		actual, e := readString(key, "")
		regCloseKey.Call(key)
		if e != nil || !strings.EqualFold(actual, want) {
			return false
		}
	}
	return true
}
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	command, err := currentCommand()
	if err != nil {
		return err
	}
	for _, ext := range formats.Extensions() {
		root, err := createKey(keyPath(ext))
		if err != nil {
			return fmt.Errorf("注册 %s 菜单失败: %w", ext, err)
		}
		e := setString(root, "MUIVerb", "使用 ExcelLens 打开")
		if e == nil {
			e = setString(root, "Icon", `"`+exe+`",0`)
		}
		regCloseKey.Call(root)
		if e != nil {
			return e
		}
		key, err := createKey(keyPath(ext) + `\command`)
		if err != nil {
			return err
		}
		err = setString(key, "", command)
		regCloseKey.Call(key)
		if err != nil {
			return err
		}
	}
	shChangeNotify.Call(shcneAssocChanged, shcnfIDList, 0, 0)
	return nil
}
func Uninstall() error {
	for _, ext := range formats.Extensions() {
		key, err := utf16(keyPath(ext))
		if err != nil {
			return err
		}
		r, _, _ := regDeleteTree.Call(hkcu, uintptr(unsafe.Pointer(key)))
		if r != 0 && r != uintptr(syscall.ERROR_FILE_NOT_FOUND) {
			return fmt.Errorf("移除 %s 菜单失败: %w", ext, syscall.Errno(r))
		}
	}
	shChangeNotify.Call(shcneAssocChanged, shcnfIDList, 0, 0)
	return nil
}
