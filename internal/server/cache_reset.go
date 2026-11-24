package server

import (
	"context"
	"encoding/hex"
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
	keep, err := root.cacheSourceProtection(dir)
	if err != nil {
		return "", fmt.Errorf("无法核对缓存目录中的源文件：%w", err)
	}
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

func (root *App) cacheSourceProtection(dir string) (map[string]bool, error) {
	for _, path := range []string{workbook.DataRoot(), dir} {
		st, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if err == nil && !st.IsDir() {
			return nil, fmt.Errorf("缓存目录不是普通目录：%s", path)
		}
	}
	if dir == workbook.CacheRoot() {
		roots, err := dataRoots()
		if err != nil {
			return nil, err
		}
		if err := roots.AdoptFrozen(context.Background()); err != nil {
			return nil, err
		}
	}
	sources, err := editBasePaths()
	if err != nil {
		return nil, fmt.Errorf("无法核对已保存的编辑来源：%w", err)
	}
	taskPaths, err := savedTaskSources()
	if err != nil {
		return nil, err
	}
	sources = append(sources, taskPaths...)
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
	return protectedCacheSources(dir, sources)
}

// A local file can itself have been opened from below the cache directory.
// Protect its source bytes (and SQLite sidecars) even in that unusual case.
func protectedCacheSources(dir string, paths []string) (map[string]bool, error) {
	keep := make(map[string]bool)
	realDir, err := filepath.EvalSymlinks(dir)
	if errors.Is(err, os.ErrNotExist) {
		return keep, nil
	}
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Stat(realDir)
	if err != nil {
		return nil, err
	}
	remember := func(base, name string) {
		rel, err := filepath.Rel(base, name)
		if err != nil || rel == "." || !filepath.IsLocal(rel) {
			return
		}
		rel = cachePathKey(rel)
		keep[rel] = true
		for parent := filepath.Dir(rel); parent != "."; parent = filepath.Dir(parent) {
			if _, seen := keep[parent]; !seen {
				keep[parent] = false
			}
		}
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		// Keep both the name used to open the source and the actual directory
		// entries. Windows expands 8.3 names when resolving either operand.
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			name := path + suffix
			remember(dir, name)
			real, err := filepath.EvalSymlinks(name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			remember(realDir, real)
			// A drive alias or UNC spelling can still refer to the same
			// directory. Anchor the resolved filename to the actual root ID.
			for parent := filepath.Dir(real); ; parent = filepath.Dir(parent) {
				st, err := os.Stat(parent)
				if err != nil {
					return nil, err
				}
				if os.SameFile(st, rootInfo) {
					remember(parent, real)
					break
				}
				if parent == filepath.Dir(parent) {
					break
				}
			}
		}
	}
	return keep, nil
}

// Workbook fingerprints name the only workbook cache groups. Archive member
// caches use an archive SHA-256, and queries can have their own scratch group.
// Other entries may be original files the user stored beside these caches.
func disposableCacheGroup(name string) bool {
	if name == "queries" {
		return true
	}
	digest, archive := strings.CutPrefix(name, "archive-")
	if len(digest) != 64 && (archive || len(digest) != 40) {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
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
		if dir == "." && (!entry.IsDir() || !disposableCacheGroup(entry.Name())) {
			continue
		}
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
