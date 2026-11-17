//go:build !windows

package workbook

import (
	"errors"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// The distributed Windows build uses the operating system's winsqlite3.dll,
// keeping ExcelLens a single EXE without cgo or additional installers.
func sqliteDiscover(path string) ([]sqliteTable, error) {
	return nil, errors.New("SQLite 查看功能目前仅支持 Windows 10/11（系统 winsqlite3.dll）")
}
func sqliteStreamRows(path, table string, emit func([]string) error) error {
	return errors.New("SQLite 查看功能目前仅支持 Windows 10/11")
}
func decodeGB18030(data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	if len(data) > maxTextLine {
		return "", errors.New("单行超过 64MB 解码上限")
	}
	// Match the decoder already used for streaming GB18030 text. Fragment
	// detection also needs this path before the streaming reader is created.
	decoded, err := simplifiedchinese.GB18030.NewDecoder().Bytes(data)
	return string(decoded), err
}
