package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"excellens/internal/archives"
	"excellens/internal/workbook"
)

// ClearAllCaches is only offered by the native tray after confirmation. Stop
// workbook users before removing their files. Keep the listener and the user's
// uploads, preferences, edit documents and exports available for reopening.
func (a *App) ClearAllCaches() (string, error) {
	root := a.rootApp()
	root.cacheResetMu.Lock()
	defer root.cacheResetMu.Unlock()
	dir := workbook.CacheRoot()
	for _, path := range []string{workbook.DataRoot(), dir} {
		st, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err == nil && !st.IsDir() {
			return "", fmt.Errorf("缓存目录不是普通目录：%s", path)
		}
	}
	sources, err := editBasePaths()
	if err != nil {
		return "", fmt.Errorf("无法核对已保存的编辑来源：%w", err)
	}
	for _, wb := range root.referencedWorkbooks() {
		path := wb.Path
		if wb.ArchiveOrigin != nil {
			path = wb.ArchiveOrigin.Path
		}
		sources = append(sources, path)
	}
	for _, tab := range root.activeBrowserWorkbooks() {
		tab.archiveMu.Lock()
		if view := tab.archiveView; view != nil {
			sources = append(sources, view.rootPath)
		}
		tab.archiveMu.Unlock()
	}
	keep := protectedCacheSources(dir, sources)
	// Closing sessions joins index, query, edit and archive workers and releases
	// their leases. Do not hold the cache or archive lock while waiting for them.
	root.closeBrowserSessions()
	root.closeArchive()
	root.replaceWorkbook(nil)
	root.edits.revision.Store(nil)
	root.cacheEpoch.Add(1)
	unlock := archives.LockCacheMaintenance()
	defer unlock()
	root.cacheMu.Lock()
	defer root.cacheMu.Unlock()
	cache, err := os.OpenRoot(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "缓存目录为空，已返回首页。", nil
	}
	if err != nil {
		return "已关闭当前工作簿。", err
	}
	defer cache.Close()
	// Large immutable indices and shared strings release their OS mappings
	// through finalizers only after the last snapshot is unreachable.
	runtime.GC()
	for attempt := 0; ; attempt++ {
		err = clearCacheEntries(cache, ".", keep)
		if err == nil {
			return "已清除全部工作簿缓存，已返回首页。", nil
		}
		if runtime.GOOS != "windows" || attempt == 4 {
			return "部分缓存未能清理，已返回首页。", err
		}
		runtime.GC()
		time.Sleep(time.Duration(attempt+1) * 75 * time.Millisecond)
	}
}

// A local file can itself have been opened from below the cache directory.
// Protect its source bytes (and SQLite sidecars) even in that unusual case.
func protectedCacheSources(dir string, paths []string) map[string]bool {
	keep := make(map[string]bool)
	for _, path := range paths {
		for _, name := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
			if real, err := filepath.EvalSymlinks(name); err == nil {
				name = real
			}
			rel, err := filepath.Rel(dir, name)
			if err != nil || rel == "." || !filepath.IsLocal(rel) {
				continue
			}
			rel = cachePathKey(rel)
			keep[rel] = true
			for parent := filepath.Dir(rel); parent != "."; parent = filepath.Dir(parent) {
				if !keep[parent] {
					keep[parent] = false
				}
			}
		}
	}
	return keep
}

func cachePathKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func clearCacheEntries(root *os.Root, dir string, keep map[string]bool) error {
	f, err := root.Open(dir)
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	_ = f.Close()
	if err != nil {
		return err
	}
	var failed []error
	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())
		absolute := filepath.Join(root.Name(), name)
		protected, ancestor := keep[cachePathKey(name)]
		switch {
		case protected:
			continue
		case dir == "." && (archives.CachePinned(absolute) || workbook.QueryFilesInUse(absolute)):
			err = fmt.Errorf("缓存仍被使用：%s", name)
		case ancestor && entry.IsDir():
			err = clearCacheEntries(root, name, keep)
		default:
			err = root.RemoveAll(name)
		}
		if err != nil {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}
