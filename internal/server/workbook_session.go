package server

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"excellens/internal/archives"
	"excellens/internal/workbook"
)

// The session lock publishes a new workbook and its task registries together.
// Source file removal remains an explicit cache/upload maintenance operation.
type workbookSession struct {
	mu           sync.RWMutex
	wb           *workbook.Workbook
	startupError string
	launchPath   string // native open reserved for this page's first request
	maintenance  bool
}

func (s *workbookSession) currentWorkbook() *workbook.Workbook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wb
}

func (a *App) OpenPath(filePath string) error {
	if a.rootApp().restoreState.pending() {
		return errors.New("请先完成恢复事务")
	}
	return a.openPathAs(filePath, "")
}

func (a *App) openPathAs(filePath, displayName string) error {
	if archives.IsPath(filePath) {
		return a.openArchive(context.Background(), filePath, filepath.Base(filePath), 0)
	}
	a.closeArchive()
	wb, release, err := a.openPinnedWorkbook(filePath)
	if err != nil {
		a.mu.Lock()
		a.startupError = err.Error()
		a.mu.Unlock()
		return err
	}
	defer release()
	if displayName != "" {
		wb.SetDisplayName(displayName)
	}
	a.replaceWorkbook(wb)
	a.recordRecent(wb)
	return nil
}

func (a *App) replaceWorkbook(wb *workbook.Workbook) {
	a.installWorkbook(wb, nil, false)
}

// installWorkbook can atomically reject a stale source-opening intent before
// cancelling jobs or replacing any current view. Ordinary opens use no guard.
func (a *App) installWorkbook(wb, expected *workbook.Workbook, guarded bool) bool {
	// Analysis registration checks the current workbook under analysisMu.
	// Take the locks in that same order and release both before any wait.
	a.analysisMu.Lock()
	a.mu.Lock()
	if guarded && (a.wb != expected || a.maintenance) {
		a.mu.Unlock()
		a.analysisMu.Unlock()
		return false
	}
	waitAnalysis := a.detachAnalysisTasksLocked()
	waitReconcile := a.detachReconciliations()
	waitRecipes := a.detachRecipeCaptures()
	oldCatalogues := a.clearCatalogueTasksLocked()
	oldDataRules := a.clearDataRuleTasksLocked()
	oldPrints := clearMetadataTasks(a.printJobs)
	oldClusters := clearMetadataTasks(a.clusterJobs)
	oldCompositions, oldCompositionResources := a.clearCompositionTasksLocked()
	oldRelations, oldRelationResources := a.clearRelationTasksLocked()
	oldQueries := a.queryController.detachLocked()
	oldExports := a.exports.cancelAllLocked()
	a.edits.releaseBinding()
	a.wb = wb
	a.startupError = ""
	a.exports = make(exportController)
	a.mu.Unlock()
	a.analysisMu.Unlock()
	waitCatalogueTasks(oldCatalogues)
	waitMetadataTasks(oldDataRules)
	waitMetadataTasks(oldPrints)
	waitMetadataTasks(oldClusters)
	waitCompositionTasks(oldCompositions, oldCompositionResources)
	waitRelationTasks(oldRelations, oldRelationResources)
	waitReconcile()
	waitRecipes()
	waitAnalysis()
	oldQueries.waitAndRelease()
	waitExportJobs(oldExports)
	return true
}
