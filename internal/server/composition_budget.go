package server

import (
	"errors"
	"sync"

	"excellens/internal/formats"
)

// Reservations follow their actual owner. Source descriptors/scratch strings
// stay charged until the final independent source lease closes; response bytes
// stay charged while a task retains them. This is shared by all browser tabs.
func (a *App) reserveCompositionBytes(bytes int64) (func(), error) {
	root := a.rootApp()
	root.sourceMu.Lock()
	defer root.sourceMu.Unlock()
	if bytes < 0 || bytes > formats.CompositionRetainedBytes-root.compositionBytes {
		return nil, errors.New("多表来源占用已满，请关闭不用的结果或分析任务后重试")
	}
	root.compositionBytes += bytes
	var once sync.Once
	return func() {
		once.Do(func() {
			root.sourceMu.Lock()
			root.compositionBytes -= bytes
			root.sourceMu.Unlock()
		})
	}, nil
}
