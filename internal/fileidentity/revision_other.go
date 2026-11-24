//go:build !windows

package fileidentity

import "os"

func Open(path string) (*os.File, bool, error) {
	f, err := os.Open(path)
	return f, false, err
}
func Revision(_ *os.File, _ os.FileInfo) (Stamp, bool) {
	return Stamp{}, false
}
