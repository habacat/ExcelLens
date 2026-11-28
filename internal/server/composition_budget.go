package server

import (
	"excellens/internal/i18n"
)

var errCompositionBudgetFull = i18n.Error("service.compositionBudget.multipleTablesOccupyingSourceSpace")

// Reservations follow their actual owner. Source descriptors/scratch strings
// stay charged until the final independent source lease closes; response bytes
// stay charged while a task retains them. This is shared by all browser tabs.
func (a *App) reserveCompositionBytes(bytes int64) (func(), error) {
	return a.rootApp().compositionBudget.Reserve(bytes, errCompositionBudgetFull)
}
