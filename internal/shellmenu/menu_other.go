//go:build !windows

package shellmenu

import "excellens/internal/i18n"

func IsInstalled() bool         { return false }
func Install(i18n.Locale) error { return i18n.Error("native.shell.windowsOnly") }
func Uninstall() error          { return i18n.Error("native.shell.windowsOnly") }
