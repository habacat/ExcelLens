package conditional

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// BudgetPolicy configures cross-workbook cumulative disk budget and active protection.
type BudgetPolicy struct {
	Dir         string
	BudgetBytes int64
	Protected   func(name string) bool
}

// EvictionReport summarizes LRU eviction performed to enforce the global budget.
type EvictionReport struct {
	EvictedFiles   int
	FreedBytes     int64
	RemainingBytes int64
	RemainingFiles int
}

// ClearReport summarizes explicit manual cache cleanup.
type ClearReport struct {
	RemovedFiles int
	FreedBytes   int64
	KeptFiles    int
	KeptBytes    int64
}

// DirStats reports current conditional statistics disk usage and active protection counts.
type DirStats struct {
	Files          int
	Bytes          int64
	ProtectedFiles int
	ProtectedBytes int64
}

var (
	cacheMu      sync.Mutex
	activeRefs   = map[string]int{}
	activeBuilds = map[string]int{}
	lastAccessed = map[string]time.Time{}
)

func cacheFileKey(path string) string {
	cleaned := filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(cleaned)
	}
	return cleaned
}

func pinReader(path string) {
	key := cacheFileKey(path)
	cacheMu.Lock()
	activeRefs[key]++
	cacheMu.Unlock()
}

func unpinReader(path string) {
	key := cacheFileKey(path)
	cacheMu.Lock()
	if n := activeRefs[key]; n <= 1 {
		delete(activeRefs, key)
	} else {
		activeRefs[key] = n - 1
	}
	cacheMu.Unlock()
}

func pinBuild(path string) {
	key := cacheFileKey(path)
	cacheMu.Lock()
	activeBuilds[key]++
	cacheMu.Unlock()
}

func unpinBuild(path string) {
	key := cacheFileKey(path)
	cacheMu.Lock()
	if n := activeBuilds[key]; n <= 1 {
		delete(activeBuilds, key)
	} else {
		activeBuilds[key] = n - 1
	}
	cacheMu.Unlock()
}

func isInUseLocked(path string) bool {
	key := cacheFileKey(path)
	return activeRefs[key] > 0 || activeBuilds[key] > 0
}

// IsFileInUse reports whether any reader or builder currently holds path.
func IsFileInUse(path string) bool {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return isInUseLocked(path)
}

func touchAccess(path string, st os.FileInfo) {
	now := time.Now()
	key := cacheFileKey(path)
	cacheMu.Lock()
	lastAccessed[key] = now
	cacheMu.Unlock()
	// Best-effort mtime refresh throttled to 1 minute so LRU order persists
	// across restarts without extra filesystem writes during fast scrolling.
	if st != nil && now.Sub(st.ModTime()) > time.Minute {
		_ = os.Chtimes(path, now, now)
	}
}

func isConditionalStatsFile(name string) bool {
	stem, ok := strings.CutSuffix(name, ".db")
	fingerprint, signature, version := strings.Cut(stem, "-v1-")
	if !ok || !version || fingerprint == "" || len(signature) != 64 {
		return false
	}
	for _, c := range signature {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func isConditionalTempFile(name string) bool {
	return strings.HasPrefix(name, ".conditional-") && strings.HasSuffix(name, ".tmp")
}

// InspectDir returns total and protected counts/bytes for conditional stats files in dir.
func InspectDir(dir string, isProtected func(name string) bool) (DirStats, error) {
	var stats DirStats
	if dir == "" {
		return stats, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for _, entry := range entries {
		if entry.IsDir() || !isConditionalStatsFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		size := info.Size()
		stats.Files++
		stats.Bytes += size
		if isInUseLocked(full) || (isProtected != nil && isProtected(entry.Name())) {
			stats.ProtectedFiles++
			stats.ProtectedBytes += size
		}
	}
	return stats, nil
}

// EnforceBudget evicts least-recently-used unprotected conditional stats files
// in policy.Dir until total size + reserveBytes <= policy.BudgetBytes.
// excludeName (if non-empty) is excluded from existing totals because it is
// about to be atomically replaced by the reserved file.
func EnforceBudget(policy BudgetPolicy, reserveBytes int64, excludeName string) (EvictionReport, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return enforceBudgetLocked(policy, reserveBytes, excludeName)
}

func enforceBudgetLocked(policy BudgetPolicy, reserveBytes int64, excludeName string) (EvictionReport, error) {
	var report EvictionReport
	if policy.Dir == "" || policy.BudgetBytes <= 0 {
		return report, nil
	}
	if reserveBytes > policy.BudgetBytes {
		return report, ErrBudgetExceeded
	}
	entries, err := os.ReadDir(policy.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	excludeKey := ""
	if excludeName != "" {
		excludeKey = cacheFileKey(filepath.Join(policy.Dir, excludeName))
	}
	type candidate struct {
		name  string
		path  string
		size  int64
		atime time.Time
	}
	var total int64
	var files int
	var evictable []candidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		full := filepath.Join(policy.Dir, name)
		if isConditionalTempFile(name) {
			if !isInUseLocked(full) {
				if info, e := entry.Info(); e == nil && time.Since(info.ModTime()) > 5*time.Minute {
					_ = os.Remove(full)
				}
			}
			continue
		}
		if !isConditionalStatsFile(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		key := cacheFileKey(full)
		if excludeKey != "" && key == excludeKey {
			continue
		}
		size := info.Size()
		total += size
		files++
		if isInUseLocked(full) || (policy.Protected != nil && policy.Protected(name)) {
			continue
		}
		atime := info.ModTime()
		if mem, ok := lastAccessed[key]; ok && mem.After(atime) {
			atime = mem
		}
		evictable = append(evictable, candidate{name: name, path: full, size: size, atime: atime})
	}
	if total+reserveBytes > policy.BudgetBytes {
		sort.Slice(evictable, func(i, j int) bool {
			if !evictable[i].atime.Equal(evictable[j].atime) {
				return evictable[i].atime.Before(evictable[j].atime)
			}
			return evictable[i].name < evictable[j].name
		})
		for _, item := range evictable {
			if total+reserveBytes <= policy.BudgetBytes {
				break
			}
			if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				continue
			}
			delete(lastAccessed, cacheFileKey(item.path))
			total -= item.size
			files--
			report.EvictedFiles++
			report.FreedBytes += item.size
		}
	}
	report.RemainingBytes = total
	report.RemainingFiles = files
	if reserveBytes > 0 && total+reserveBytes > policy.BudgetBytes {
		return report, ErrBudgetExceeded
	}
	return report, nil
}

// ClearUnused deletes unprotected and unused conditional statistics files in dir.
func ClearUnused(dir string, isProtected func(name string) bool) (ClearReport, error) {
	var report ClearReport
	if dir == "" {
		return report, nil
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return report, nil
	}
	if err != nil {
		return report, err
	}
	var failed []error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		full := filepath.Join(dir, name)
		if isConditionalTempFile(name) {
			if !isInUseLocked(full) {
				_ = os.Remove(full)
			}
			continue
		}
		if !isConditionalStatsFile(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		size := info.Size()
		if isInUseLocked(full) || (isProtected != nil && isProtected(name)) {
			report.KeptFiles++
			report.KeptBytes += size
			continue
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, err)
			report.KeptFiles++
			report.KeptBytes += size
			continue
		}
		delete(lastAccessed, cacheFileKey(full))
		report.RemovedFiles++
		report.FreedBytes += size
	}
	return report, errors.Join(failed...)
}

// ClearPrefix removes unused conditional stats files matching prefix (for a single workbook).
func ClearPrefix(dir, prefix string) error {
	if dir == "" || prefix == "" {
		return nil
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failed []error
	for _, entry := range entries {
		if entry.IsDir() || !isConditionalStatsFile(entry.Name()) || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		full := filepath.Join(dir, entry.Name())
		if isInUseLocked(full) {
			failed = append(failed, errors.New("条件统计索引正在使用中"))
			continue
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, err)
			continue
		}
		delete(lastAccessed, cacheFileKey(full))
	}
	return errors.Join(failed...)
}
