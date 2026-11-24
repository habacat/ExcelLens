// Package filelock provides a non-blocking, process-owned file lock.
package filelock

import "errors"

var ErrBusy = errors.New("文件正在使用")
