package server

import "errors"

var errCompositionBudgetFull = errors.New("多表来源占用已满，请关闭不用的结果或分析任务后重试")

// Reservations follow their actual owner. Source descriptors/scratch strings
// stay charged until the final independent source lease closes; response bytes
// stay charged while a task retains them. This is shared by all browser tabs.
func (a *App) reserveCompositionBytes(bytes int64) (func(), error) {
	return a.rootApp().compositionBudget.Reserve(bytes, errCompositionBudgetFull)
}
