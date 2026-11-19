package server

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"excellens/internal/workbook"
)

// Search caches contain only *completed* searches. They are tied to the
// workbook's content fingerprint (CacheDir) and invalidated when it changes.
// v4 uses the actual shifted whole-row/column references in shared formulas.
// Earlier completed searches are recomputed on demand from the original index.
const searchCacheSchema = 4
const maxCachedSearches = 32
const maxSearchCacheBytes = 3 << 20

type persistedSearch struct {
	Version   int                     `json:"version"`
	Sheet     string                  `json:"sheet"`
	Query     string                  `json:"query"`
	Mode      string                  `json:"mode"`
	Scope     string                  `json:"scope"`
	Matches   []workbook.SearchResult `json:"matches"`
	Truncated bool                    `json:"truncated"`
}

func searchCacheKey(sheet, query, mode, scope string) string {
	if scope == "all" {
		sheet = ""
	}
	query = strings.TrimSpace(query)
	if mode != "advanced" {
		query = strings.ToLower(query)
	}
	return strings.Join([]string{sheet, query, mode, scope}, "\x00")
}

func searchCachePath(wb *workbook.Workbook, sheet, query, mode, scope string) string {
	h := sha1.Sum([]byte(searchCacheKey(sheet, query, mode, scope)))
	return filepath.Join(wb.CacheDir, "search", hex.EncodeToString(h[:])+".json")
}

func loadSearchCache(wb *workbook.Workbook, sheet, query, mode, scope string) (*persistedSearch, error) {
	path := searchCachePath(wb, sheet, query, mode, scope)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var c persistedSearch
	dec := json.NewDecoder(io.LimitReader(f, maxSearchCacheBytes+1))
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if c.Version != searchCacheSchema || searchCacheKey(c.Sheet, c.Query, c.Mode, c.Scope) != searchCacheKey(sheet, query, mode, scope) || len(c.Matches) > 500 {
		return nil, fmt.Errorf("搜索缓存版本或检索条件不匹配")
	}
	return &c, nil
}

func saveSearchCache(wb *workbook.Workbook, job *SearchJob) error {
	job.mu.RLock()
	if job.Status != "done" {
		job.mu.RUnlock()
		return nil
	}
	data := persistedSearch{Version: searchCacheSchema, Sheet: job.Sheet, Query: job.Query,
		Mode: job.Mode, Scope: job.Scope, Matches: append([]workbook.SearchResult(nil), job.Matches...), Truncated: job.Truncated}
	job.mu.RUnlock()
	dir := filepath.Join(wb.CacheDir, "search")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	target := searchCachePath(wb, data.Sheet, data.Query, data.Mode, data.Scope)
	tmp, err := os.CreateTemp(dir, ".search-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := json.NewEncoder(tmp).Encode(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		// Windows rename fails when target exists; remove the cache copy only.
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return err
		}
		if err = os.Rename(tmp.Name(), target); err != nil {
			return err
		}
	}
	pruneSearchCache(dir, maxCachedSearches)
	return nil
}

func pruneSearchCache(dir string, keep int) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileAge struct {
		path string
		mod  time.Time
	}
	var old []fileAge
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		info, err := file.Info()
		if err != nil {
			continue
		}
		old = append(old, fileAge{filepath.Join(dir, file.Name()), info.ModTime()})
	}
	for len(old) > keep {
		oldest := 0
		for i := 1; i < len(old); i++ {
			if old[i].mod.Before(old[oldest].mod) {
				oldest = i
			}
		}
		_ = os.Remove(old[oldest].path)
		old = append(old[:oldest], old[oldest+1:]...)
	}
}
