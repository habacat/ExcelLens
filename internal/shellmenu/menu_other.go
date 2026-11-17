//go:build !windows

package shellmenu

import "errors"

func IsInstalled() bool { return false }
func Install() error    { return errors.New("仅 Windows 支持右键菜单集成") }
func Uninstall() error  { return errors.New("仅 Windows 支持右键菜单集成") }
