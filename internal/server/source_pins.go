package server

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"excellens/internal/workbook"
)

func (a *App) openPinnedWorkbook(path string) (*workbook.Workbook, func(), error) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		return nil, nil, err
	}
	return wb, a.pinWorkbooks(wb), nil
}

func (a *App) pinUploadPath(path string) func() {
	return a.rootApp().sourceUploads.Pin(path)
}

// Register while holding the shared cache read lock, before exposing a source
// snapshot. Pin operations themselves never acquire App/cache/session locks.
func (a *App) pinWorkbooks(books ...*workbook.Workbook) func() {
	return a.rootApp().sourcePins.Pin(books...)
}

func (a *App) referencedWorkbooks() []*workbook.Workbook {
	root := a.rootApp()
	seen := make(map[*workbook.Workbook]bool)
	for _, viewer := range root.activeBrowserWorkbooks() {
		if wb := viewer.currentWorkbook(); wb != nil {
			seen[wb] = true
		}
	}
	for _, wb := range root.sourcePins.Snapshot() {
		seen[wb] = true
	}
	books := make([]*workbook.Workbook, 0, len(seen))
	for wb := range seen {
		books = append(books, wb)
	}
	return books
}

// Content identity remains visible separately. Two uploaded copies with equal
// bytes may have different names/paths and must not exchange their provenance.
func sourceBookID(wb *workbook.Workbook) string {
	if wb == nil {
		return ""
	}
	return bookIDAt(wb.Fingerprint, wb.Path)
}

func bookIDAt(fingerprint, file string) string {
	path := filepath.Clean(file)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	digest := sha256.Sum256([]byte(fingerprint + "\x00" + path))
	return fmt.Sprintf("%x", digest[:])
}

func (a *App) resolveReconcileBook(id string) *workbook.Workbook {
	if wb := a.currentWorkbook(); wb != nil && sourceBookID(wb) == id {
		return wb
	}
	for _, wb := range a.referencedWorkbooks() {
		if sourceBookID(wb) == id {
			return wb
		}
	}
	return nil
}
