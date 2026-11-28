package server

import (
	"context"
	"errors"

	"excellens/internal/i18n"
	"excellens/internal/localstore"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"excellens/internal/archives"
	"excellens/internal/workbook"
)

// CacheDir and TempDir are also used by the Windows tray's explorer actions.
func CacheDir() string { return workbook.CacheRoot() }
func TempDir() string  { return workbook.UploadRoot() }
func LogDir() string {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base = workbook.DataRoot()
	}
	return filepath.Join(base, "ExcelLens", "logs")
}

// ClearUnusedCaches intentionally preserves the currently open workbook so a
// tray action cannot invalidate mapped row indexes while a browser reads them.
func (a *App) ClearUnusedCaches() (string, error) {
	return a.clearUnusedCaches(a.DisplayLocale())
}

func (a *App) clearUnusedCaches(locale i18n.Locale) (string, error) {
	a.rootApp().cacheResetMu.RLock()
	defer a.rootApp().cacheResetMu.RUnlock()
	if a.rootApp().restoreState.pending() {
		return "", i18n.Error("service.maintenance.completeRestorationProcessFirst")
	}
	releaseArchiveMaintenance := archives.LockCacheMaintenance()
	defer releaseArchiveMaintenance()
	rootApp := a.rootApp()
	rootApp.cacheMu.Lock()
	defer rootApp.cacheMu.Unlock()
	active := make(map[string]struct{})
	for _, wb := range rootApp.referencedWorkbooks() {
		active[filepath.Clean(wb.CacheDir)] = struct{}{}
		if group := archives.CacheGroup(wb.Path); group != "" {
			active[group] = struct{}{}
		}
	}
	root := workbook.CacheRoot()
	protected, err := rootApp.cacheSourceProtection(root)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return locale.Text("native.cleanup.emptyCache"), nil
	}
	if err != nil {
		return "", err
	}
	count := 0
	var failed []string
	for _, entry := range entries {
		_, holdsSource := protected[cachePathKey(entry.Name())]
		if !entry.IsDir() || holdsSource || !disposableCacheGroup(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if archives.CachePinned(dir) {
			continue
		}
		if _, inUse := active[filepath.Clean(dir)]; inUse {
			continue
		}
		if workbook.QueryFilesInUse(dir) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			failed = append(failed, entry.Name())
			continue
		}
		count++
	}
	statsReport, statsErr := workbook.ClearUnusedConditionalStats(rootApp.conditionalStatsProtector())
	info := locale.Text("native.cleanup.unusedCachesCleared", i18n.Args{"count": count})
	if statsReport.RemovedFiles > 0 {
		info = locale.Text("native.cleanup.unusedCachesAndStatisticsCleared", i18n.Args{"count": count, "indexes": statsReport.RemovedFiles, "space": formatReadableBytes(statsReport.FreedBytes)})
	}
	if len(failed) > 0 {
		return info, errors.Join(i18n.Error("service.maintenance.groupCacheCleanupFailed", i18n.Args{"len": len(failed), "join": strings.Join(failed, ", ")}), statsErr)
	}
	return info, statsErr
}

// ClearUnusedUploads deletes only files created by ExcelLens browser uploads,
// never the original workbook and never the one currently open in the viewer.
// No file is ever deleted implicitly on switching workbooks or quitting.
func (a *App) ClearUnusedUploads() (string, error) {
	return a.clearUnusedUploads(a.DisplayLocale())
}

func (a *App) clearUnusedUploads(locale i18n.Locale) (string, error) {
	a.rootApp().cacheResetMu.RLock()
	defer a.rootApp().cacheResetMu.RUnlock()
	if a.rootApp().restoreState.pending() {
		return "", i18n.Error("service.maintenance.completeRestorationProcessFirst")
	}
	rootApp := a.rootApp()
	rootApp.cacheMu.Lock()
	defer rootApp.cacheMu.Unlock()
	active := make(map[string]struct{})
	for _, path := range rootApp.sourceUploads.Snapshot() {
		active[filepath.Clean(path)] = struct{}{}
	}
	for _, viewer := range rootApp.activeBrowserWorkbooks() {
		viewer.archiveMu.Lock()
		if view := viewer.archiveView; view != nil {
			active[filepath.Clean(view.file.Path)] = struct{}{}
		}
		viewer.archiveMu.Unlock()
	}
	for _, wb := range rootApp.referencedWorkbooks() {
		active[filepath.Clean(wb.Path)] = struct{}{}
	}
	root := workbook.UploadRoot()
	keep, err := rootApp.cacheSourceProtection(root)
	if err != nil {
		return "", err
	}
	pinned, err := protectedCacheSources(root, slices.Collect(maps.Keys(active)))
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return locale.Text("native.cleanup.noUnusedTemporaryFiles"), nil
	}
	if err != nil {
		return "", err
	}
	count, skipped, failed := 0, 0, 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "ExcelLens-upload-") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		key := cachePathKey(entry.Name())
		if keep[key] || pinned[key] {
			skipped++
			continue
		}
		if err := os.Remove(path); err != nil {
			failed++
			continue
		}
		count++
	}
	info := locale.Text("native.cleanup.temporaryFilesCleared", i18n.Args{"count": count, "kept": skipped})
	if failed > 0 {
		return info, i18n.Error("service.maintenance.additionallyFilesCannotBeDeleted", i18n.Args{"failed": failed})
	}
	return info, nil
}

// Confirmed task sources have to be reopenable even after their tab closes.
func savedTaskSources() ([]string, error) {
	root, err := localstore.ConfigDir()
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(filepath.Join(root, "task-checkpoints")); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	store, err := openTaskCheckpoints()
	if err != nil {
		return nil, err
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	items, err := listTaskCheckpoints(ctx, store)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, item := range items {
		if item.Manifest == nil {
			return nil, i18n.Error("service.maintenance.sourceTaskRecoveryRecordsCannot")
		}
		for _, s := range item.Manifest.Definition.Sources {
			source, err := locatedSource(s)
			if err != nil {
				return nil, err
			}
			paths = append(paths, s.Path, source.Path, restoredUploadCandidate(s.Path))
		}
	}
	return paths, nil
}
