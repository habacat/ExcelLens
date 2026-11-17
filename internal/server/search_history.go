package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SearchHistoryItem is intentionally metadata-only. The cached snippets stay
// in the search JSON file until a user re-opens that search.
type SearchHistoryItem struct {
	Sheet     string    `json:"sheet"`
	Query     string    `json:"query"`
	Mode      string    `json:"mode"`
	Scope     string    `json:"scope"`
	Hits      int       `json:"hits"`
	Truncated bool      `json:"truncated"`
	Updated   time.Time `json:"updated"`
}

func (a *App) handleSearchHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeJSON(w, map[string]any{"items": []SearchHistoryItem{}})
		return
	}
	dir := filepath.Join(wb.CacheDir, "search")
	files, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		writeJSON(w, map[string]any{"items": []SearchHistoryItem{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type pathWithTime struct {
		path string
		mod  time.Time
	}
	found := make([]pathWithTime, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		stat, err := file.Info()
		if err != nil || stat.Size() > maxSearchCacheBytes {
			continue
		}
		found = append(found, pathWithTime{filepath.Join(dir, file.Name()), stat.ModTime()})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	if len(found) > maxCachedSearches {
		found = found[:maxCachedSearches]
	}
	out := make([]SearchHistoryItem, 0, len(found))
	for _, entry := range found {
		f, err := os.Open(entry.path)
		if err != nil {
			continue
		}
		var c persistedSearch
		decodeErr := json.NewDecoder(io.LimitReader(f, maxSearchCacheBytes+1)).Decode(&c)
		f.Close()
		if decodeErr != nil || c.Version != searchCacheSchema {
			continue
		}
		out = append(out, SearchHistoryItem{Sheet: c.Sheet, Query: c.Query, Mode: c.Mode, Scope: c.Scope, Hits: len(c.Matches), Truncated: c.Truncated, Updated: entry.mod})
	}
	writeJSON(w, map[string]any{"items": out})
}
