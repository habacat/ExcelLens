package server

import (
	"context"
	"errors"
	"excellens/internal/localstore"
	"fmt"
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
	a.rootApp().cacheResetMu.RLock()
	defer a.rootApp().cacheResetMu.RUnlock()
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
		return "缓存目录为空", nil
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
	info := fmt.Sprintf("已清理 %d 组历史工作簿缓存。当前打开工作簿的缓存已保留。", count)
	if len(failed) > 0 {
		return info, fmt.Errorf("%d 组缓存清理失败：%s", len(failed), strings.Join(failed, ", "))
	}
	return info, nil
}

// ClearUnusedUploads deletes only files created by ExcelLens browser uploads,
// never the original workbook and never the one currently open in the viewer.
// No file is ever deleted implicitly on switching workbooks or quitting.
func (a *App) ClearUnusedUploads() (string, error) {
	a.rootApp().cacheResetMu.RLock()
	defer a.rootApp().cacheResetMu.RUnlock()
	rootApp := a.rootApp()
	rootApp.cacheMu.Lock()
	defer rootApp.cacheMu.Unlock()
	active := make(map[string]struct{})
	rootApp.sourceMu.Lock()
	for path := range rootApp.sourceUploads {
		active[filepath.Clean(path)] = struct{}{}
	}
	rootApp.sourceMu.Unlock()
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
		return "没有可清理的临时文件", nil
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
	info := fmt.Sprintf("已清理 %d 个临时文件；保留正在使用的 %d 个文件。", count, skipped)
	if failed > 0 {
		return info, fmt.Errorf("另外 %d 个文件无法删除（可能被其他程序占用）", failed)
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
			return nil, errors.New("任务恢复记录的来源无法核对，已保留文件")
		}
		for _, s := range item.Manifest.Definition.Sources {
			paths = append(paths, s.Path)
		}
	}
	return paths, nil
}
