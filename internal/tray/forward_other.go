//go:build !windows

package tray

func ForwardWorkbook(path string) bool { return false }
func ForwardOpen() bool                { return false }
