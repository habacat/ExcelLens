//go:build !windows

package workbook

import "os"

func openFingerprintSource(path string) (*os.File, bool, error) {
	f, err := os.Open(path)
	return f, false, err
}
func fingerprintSourceStamp(_ *os.File, _ os.FileInfo) (externalFileStamp, bool) {
	return externalFileStamp{}, false
}
