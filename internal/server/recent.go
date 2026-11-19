package server

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"excellens/internal/workbook"
)

// Recent files are a local user preference, independent of disposable indexes.
// Browser uploads are kept in ExcelLens's temporary-file directory until the
// user explicitly cleans that directory. No source workbooks are altered.
type recentEntry struct {
	ArchivePath    string       `json:"archivePath,omitempty"`
	ArchiveName    string       `json:"archiveName,omitempty"`
	ArchiveEntry   int          `json:"archiveEntry,omitempty"`
	ArchiveChain   []archiveHop `json:"archiveChain,omitempty"`
	ArchiveMember  string       `json:"archiveMember,omitempty"`
	ArchivePartial bool         `json:"archivePartial,omitempty"`
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Path           string       `json:"path"`
	Fingerprint    string       `json:"fingerprint"`
	FileSize       int64        `json:"fileSize"`
	SheetCount     int          `json:"sheetCount"`
	OpenedAt       time.Time    `json:"openedAt"`
	Pinned         bool         `json:"pinned"`
	Available      bool         `json:"available"`
}

type recentFile struct {
	Version int           `json:"version"`
	Items   []recentEntry `json:"items"`
}

var recentStoreMu sync.Mutex

func recentPath() string { return filepath.Join(workbook.DataRoot(), "recent.json") }

func readRecents() recentFile {
	result := recentFile{Version: 1, Items: []recentEntry{}}
	f, err := os.Open(recentPath())
	if err != nil {
		return result
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 256<<10+1))
	if err != nil || len(b) > 256<<10 {
		return result
	}
	var saved recentFile
	if json.Unmarshal(b, &saved) != nil || saved.Version != 1 || len(saved.Items) > 256 {
		return result
	}
	seen := make(map[string]struct{})
	for _, item := range saved.Items {
		if item.ID != recentID(item.Path) || item.Path == "" || len(item.Name) > 512 {
			continue
		}
		if _, found := seen[item.ID]; found {
			continue
		}
		seen[item.ID] = struct{}{}
		result.Items = append(result.Items, item)
		if len(result.Items) >= 40 {
			break
		}
	}
	return result
}

func recentID(p string) string {
	canonical, err := filepath.Abs(p)
	if err != nil {
		canonical = p
	}
	if os.PathSeparator == '\\' {
		canonical = strings.ToLower(canonical)
	}
	sum := sha1.Sum([]byte(canonical))
	return hex.EncodeToString(sum[:12])
}

func writeRecents(r recentFile) error {
	dir := filepath.Dir(recentPath())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".recent-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(r); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), recentPath()); err != nil {
		// Windows does not always allow Rename over an existing file.
		if rmErr := os.Remove(recentPath()); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return err
		}
		return os.Rename(f.Name(), recentPath())
	}
	return nil
}

func sortedRecents(items []recentEntry) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Pinned != items[j].Pinned {
			return items[i].Pinned
		}
		return items[i].OpenedAt.After(items[j].OpenedAt)
	})
}

func (a *App) recordRecent(wb *workbook.Workbook) {
	if wb == nil {
		return
	}
	p, _ := filepath.Abs(wb.Path)
	if p == "" {
		p = wb.Path
	}
	id := recentID(p)
	st, err := os.Stat(p)
	if err != nil {
		return
	}
	item := recentEntry{ID: id, Name: workbookDisplayName(wb), Path: p, Fingerprint: wb.Fingerprint, FileSize: st.Size(), SheetCount: len(wb.Sheets), OpenedAt: time.Now(), Available: true}
	a.archiveRecent(wb, &item)
	recentStoreMu.Lock()
	defer recentStoreMu.Unlock()
	r := readRecents()
	for _, existing := range r.Items {
		if existing.ID == id {
			item.Pinned = existing.Pinned
			break
		}
	}
	filtered := make([]recentEntry, 0, 32)
	filtered = append(filtered, item)
	for _, existing := range r.Items {
		if existing.ID != id {
			filtered = append(filtered, existing)
		}
	}
	sortedRecents(filtered)
	if len(filtered) > 30 {
		filtered = filtered[:30]
	}
	r.Items = filtered
	_ = writeRecents(r)
}

// Checking a source path can block on external storage. Hold the shared
// history lock only while reading the JSON snapshot or committing a change.
func snapshotRecents() recentFile {
	recentStoreMu.Lock()
	defer recentStoreMu.Unlock()
	return readRecents()
}

func (a *App) handleRecent(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if req.Method == http.MethodGet {
		r := snapshotRecents()
		for i := range r.Items {
			if r.Items[i].ArchivePath != "" {
				st, err := os.Stat(r.Items[i].ArchivePath)
				r.Items[i].Available = err == nil && st.Mode().IsRegular()
				continue
			}
			st, err := os.Stat(r.Items[i].Path)
			r.Items[i].Available = err == nil && st.Mode().IsRegular() && strings.EqualFold(filepath.Ext(r.Items[i].Path), filepath.Ext(r.Items[i].Name))
		}
		sortedRecents(r.Items)
		writeJSON(w, map[string]any{"items": r.Items})
		return
	}
	var input struct {
		Action string `json:"action"`
		ID     string `json:"id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&input) != nil {
		writeError(w, 400, "历史记录操作参数无效")
		return
	}
	var r recentFile
	if input.Action == "open" {
		r = snapshotRecents()
	} else {
		recentStoreMu.Lock()
		defer recentStoreMu.Unlock()
		r = readRecents()
	}
	target := -1
	for i := range r.Items {
		if r.Items[i].ID == input.ID {
			target = i
			break
		}
	}
	if target < 0 {
		writeError(w, 404, "找不到这条最近打开记录")
		return
	}
	item := r.Items[target]
	switch input.Action {
	case "remove":
		r.Items = append(r.Items[:target], r.Items[target+1:]...)
		if err := writeRecents(r); err != nil {
			writeError(w, 500, "无法保存历史记录")
			return
		}
		writeJSON(w, map[string]any{"removed": true})
		return
	case "pin":
		pinned := !item.Pinned
		r.Items[target].Pinned = pinned
		sortedRecents(r.Items)
		if err := writeRecents(r); err != nil {
			writeError(w, 500, "无法保存置顶状态")
			return
		}
		writeJSON(w, map[string]any{"pinned": pinned})
		return
	case "open":
		if !workbook.SupportedExtensions(filepath.Ext(item.Path)) {
			writeError(w, 400, "记录中的文件类型不受支持")
			return
		}
		checkPath := item.Path
		if item.ArchivePath != "" {
			checkPath = item.ArchivePath
		}
		st, err := os.Stat(checkPath)
		if err != nil || !st.Mode().IsRegular() {
			writeError(w, 404, "文件已移动或不存在。可以从列表移除该记录，或重新选择文件。")
			return
		}
		if item.ArchivePath != "" {
			err = a.reopenArchiveRecent(req.Context(), item)
			if err == nil {
				a.recordRecent(a.currentWorkbook())
			}
		} else {
			err = a.openPathAs(item.Path, item.Name)
		}
		if err != nil {
			writeError(w, 400, fmt.Sprintf("无法打开最近文件：%v", err))
			return
		}
		wb := a.currentWorkbook()
		memberStat, statErr := os.Stat(wb.Path)
		if statErr != nil {
			writeError(w, 404, "刚打开的源文件已移动或删除，请重新选择")
			return
		}
		writeJSON(w, map[string]any{"opened": true, "workbookKey": wb.Fingerprint, "fileName": workbookDisplayName(wb), "fileSize": memberStat.Size(), "sheetCount": len(wb.Sheets), "activeTab": wb.ActiveTab, "sheets": wb.Sheets, "source": wb.SourceInfo, "archive": a.archiveSummary()})
		return
	default:
		writeError(w, 400, "不支持的历史记录操作")
	}
}
