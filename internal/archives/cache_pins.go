package archives

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"

	"excellens/internal/workbook"
)

var archiveCacheAccess sync.RWMutex
var pinnedCaches = struct {
	sync.Mutex
	refs map[string]int
}{refs: make(map[string]int)}

func CacheGroup(path string) string {
	root := filepath.Clean(workbook.CacheRoot())
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	first := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	if !strings.HasPrefix(first, "archive-") || len(first) != 72 {
		return ""
	}
	if _, err := hex.DecodeString(first[8:]); err != nil {
		return ""
	}
	return filepath.Join(root, first)
}

func pinDirectory(dir string) func() {
	if dir == "" {
		return func() {}
	}
	pinnedCaches.Lock()
	pinnedCaches.refs[dir]++
	pinnedCaches.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			pinnedCaches.Lock()
			defer pinnedCaches.Unlock()
			pinnedCaches.refs[dir]--
			if pinnedCaches.refs[dir] <= 0 {
				delete(pinnedCaches.refs, dir)
			}
		})
	}
}

func pinSource(path string) func() {
	archiveCacheAccess.RLock()
	defer archiveCacheAccess.RUnlock()
	return pinDirectory(CacheGroup(path))
}

func CachePinned(dir string) bool {
	pinnedCaches.Lock()
	defer pinnedCaches.Unlock()
	return pinnedCaches.refs[filepath.Clean(dir)] > 0
}

// Cache creation and pin registration are atomic with respect to maintenance.
// The caller may only remove inactive groups below workbook.CacheRoot().
func LockCacheMaintenance() func() { archiveCacheAccess.Lock(); return archiveCacheAccess.Unlock }
