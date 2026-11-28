package server

import (
	"encoding/json"

	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"excellens/internal/archives"
	"excellens/internal/buildinfo"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/ui"
	"excellens/internal/workbook"
)

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	root := a.rootApp()
	locale := root.pageLocale(r)
	w.Header().Set("Content-Language", string(locale))
	w.Header().Add("Vary", "Accept-Language")
	if root.restoreState.pending() {
		page, err := ui.RecoveryIndex(root.token, locale)
		if err != nil {
			http.Error(w, locale.Text("service.language.recoveryUnavailable"), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
		return
	}
	ribbon, _ := root.RibbonLayout() // keep the page usable if a preference is unreadable
	b, err := ui.IndexLocalized(a.token, ribbon, root.cacheEpoch.Load(), locale)
	if err != nil {
		http.Error(w, locale.Text("service.language.interfaceUnavailable"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (a *App) handleAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/assets/")
	b, contentType, etag, err := ui.ReadAssetWithETag(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Embedded assets are immutable for the lifetime of this ephemeral-port process.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("ETag", etag)
	if r.URL.Query().Get("v") == strings.Trim(etag, `"`) {
		w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(b)
}

func etagMatches(header, etag string) bool {
	if header == "" || etag == "" {
		return false
	}
	for _, candidate := range strings.Split(header, ",") {
		tag := strings.TrimSpace(candidate)
		if tag == "*" || tag == etag || strings.TrimPrefix(tag, "W/") == etag {
			return true
		}
	}
	return false
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		a.mu.RLock()
		startupError := a.startupError
		a.mu.RUnlock()
		writeJSON(w, ClosedWorkbookResponse{Version: buildinfo.Version, Error: responseLocale(w).ErrorText(startupError), Archive: a.archiveSummary()})
		return
	}
	a.writeWorkbookStatus(w, wb)
}

func (a *App) writeWorkbookStatus(w http.ResponseWriter, wb *workbook.Workbook) {
	st, _ := os.Stat(wb.Path)
	var size int64
	if st != nil {
		size = st.Size()
	}
	if wb.Format == "db-online" {
		size = wb.DatabaseSnapshotSize()
	}
	status := openedWorkbook(wb, workbookDisplayName(wb), size)
	status.Version = buildinfo.Version
	status.Archive = a.archiveSummary()
	writeJSON(w, status)
}

// handleStyles sends the resolved, compact visual-style palette once per workbook.
// /api/sheet continues transmitting only the original style ID per cell.
func (a *App) handleStyles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.handlers.noWorkbookOpen")
		return
	}
	writeJSON(w, StylesResponse{Styles: wb.VisualStyles, RichTextNotice: wb.RichTextNotice()})
}

func workbookDisplayName(wb *workbook.Workbook) string {
	if wb.DisplayName != "" {
		return wb.DisplayName
	}
	return filepath.Base(wb.Path)
}

func (a *App) handleOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	const maxFileSize = formats.MaxUploadBytes
	r.Body = http.MaxBytesReader(w, r.Body, maxFileSize+(64<<20))
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeMessage(w, http.StatusBadRequest, "service.handlers.fileReadingFailed", i18n.Args{"detail": err})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		writeMessage(w, http.StatusBadRequest, "service.handlers.selectExcelCsvTxtOr")
		return
	}
	defer f.Close()
	if h.Size <= 0 {
		writeMessage(w, http.StatusBadRequest, "service.handlers.fileEmpty")
		return
	}
	if h.Size > maxFileSize {
		writeMessage(w, http.StatusRequestEntityTooLarge, "service.handlers.fileExceedsTibUploadLimit")
		return
	}
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if !workbook.SupportedExtensions(ext) {
		writeMessage(w, http.StatusBadRequest, "service.handlers.selectSupportedDataFileOr")
		return
	}
	a.cacheMu.RLock()
	tmpPath, err := persistUploadedFile(f, ext, h.Size)
	if err != nil {
		a.cacheMu.RUnlock()
		writeMessage(w, http.StatusInternalServerError, "service.handlers.failedSaveLocalTemporaryFiles", i18n.Args{"detail": err})
		return
	}
	releaseUpload := a.pinUploadPath(tmpPath)
	a.cacheMu.RUnlock()
	defer releaseUpload()
	if r.FormValue("reconcileSource") == "1" {
		a.handleReconcileUploaded(w, r, tmpPath, filepath.Base(h.Filename))
		return
	}
	if archives.IsPath(h.Filename) {
		if err := a.openArchive(r.Context(), tmpPath, filepath.Base(h.Filename), 0); err != nil {
			_ = os.Remove(tmpPath)
			writeMessage(w, 400, "service.handlers.unableReadCompressedFiles", i18n.Args{"detail": err})
			return
		}
		writeJSON(w, ClosedWorkbookResponse{ArchiveOnly: true, Archive: a.archiveSummary()})
		return
	}
	a.closeArchive()
	wb, releaseBook, err := a.openPinnedWorkbook(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		writeMessage(w, http.StatusBadRequest, "service.handlers.unableOpenWorkbook", i18n.Args{"detail": err})
		return
	}
	defer releaseBook()
	wb.SetDisplayName(filepath.Base(h.Filename))
	a.replaceWorkbook(wb)
	a.recordRecent(wb)
	writeJSON(w, openedWorkbook(wb, filepath.Base(h.Filename), h.Size))
}

func persistUploadedFile(src interface {
	Read([]byte) (int, error)
	Close() error
}, ext string, expected int64) (string, error) {
	if diskFile, ok := src.(*os.File); ok {
		sourcePath := diskFile.Name()
		_ = diskFile.Close()
		if err := os.MkdirAll(workbook.UploadRoot(), 0700); err != nil {
			return "", err
		}
		targetPath := filepath.Join(workbook.UploadRoot(), "ExcelLens-upload-"+randomID()+ext)
		if err := os.Rename(sourcePath, targetPath); err == nil {
			return targetPath, nil
		}
		reopened, err := os.Open(sourcePath)
		if err != nil {
			return "", err
		}
		src = reopened
	}
	defer src.Close()
	if err := os.MkdirAll(workbook.UploadRoot(), 0700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(workbook.UploadRoot(), "ExcelLens-upload-*"+ext)
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	buffer := make([]byte, 1<<20)
	copied, copyErr := io.CopyBuffer(tmp, src, buffer)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil || copied != expected {
		_ = os.Remove(tmpPath)
		switch {
		case copyErr != nil:
			return "", copyErr
		case syncErr != nil:
			return "", syncErr
		case closeErr != nil:
			return "", closeErr
		default:
			return "", i18n.Error("service.handlers.fileCopyLengthInconsistentExpected", i18n.Args{"expected": expected, "copied": copied})
		}
	}
	return tmpPath, nil
}

func (a *App) handleCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeJSON(w, map[string]any{"opened": false, "size": 0})
		return
	}
	a.cacheMu.RLock()
	size, files := directorySize(wb.CacheDir)
	a.cacheMu.RUnlock()
	writeJSON(w, map[string]any{"opened": true, "size": size, "files": files})
}

func (a *App) handleCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.handlers.noWorkbookOpen")
		return
	}
	if wb.HasMappedIndexes() || workbook.QueryFilesInUse(wb.CacheDir) {
		writeMessage(w, http.StatusConflict, "service.handlers.currentWorkbookStillUseOr")
		return
	}
	if a.otherTabUsesCache(wb.CacheDir) {
		writeMessage(w, http.StatusConflict, "service.handlers.otherPagesVisibleRowViews")
		return
	}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		writeMessage(w, http.StatusConflict, "service.handlers.cacheMaintenanceProgress")
		return
	}
	a.maintenance = true
	done := a.cancelIndexSearchLocked()
	a.mu.Unlock()
	for _, task := range done {
		<-task
	}
	a.cacheMu.Lock()
	var clearErr error
	conflict := false
	if a.currentWorkbook() != wb {
		clearErr, conflict = i18n.Error("service.handlers.workbookSwitchedCacheCleanupCanceled"), true
	} else if wb.HasMappedIndexes() || workbook.QueryFilesInUse(wb.CacheDir) || a.otherTabUsesCache(wb.CacheDir) {
		clearErr, conflict = i18n.Error("service.handlers.cacheStillContainsUsedIndexes"), true
	} else {
		roots, err := dataRoots()
		clearErr = err
		if clearErr == nil {
			_, clearErr = roots.Adopt(r.Context(), "frozen-panes", wb.Fingerprint+".json")
		}
		if clearErr == nil {
			clearErr = os.RemoveAll(wb.CacheDir)
		}
		if clearErr == nil {
			clearErr = os.MkdirAll(wb.CacheDir, 0700)
		}
		if clearErr == nil {
			clearErr = workbook.ClearWorkbookConditionalStats(wb.Fingerprint)
		}
	}
	if clearErr == nil {
		wb.ClearIndexes()
	}
	a.cacheMu.Unlock()
	a.mu.Lock()
	if clearErr == nil && a.wb == wb {
		for _, job := range a.searchJobs {
			job.Results.Close()
		}
		a.indexJobs = make(map[string]*IndexJob)
		a.indexBySheet = make(map[string]string)
		a.conditionalStatsJobs = make(map[string]*ConditionalStatsJob)
		a.conditionalStatsBySheet = make(map[string]string)
		a.searchJobs = make(map[string]*SearchJob)
	}
	a.maintenance = false
	a.mu.Unlock()
	if clearErr != nil {
		if conflict {
			writeFailure(w, http.StatusConflict, clearErr)
			return
		}
		writeMessage(w, http.StatusInternalServerError, "service.handlers.cacheCleanupFailed", i18n.Args{"detail": clearErr})
		return
	}
	writeJSON(w, map[string]any{"cleared": true})
}

func (a *App) handleExit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, map[string]any{"exiting": true})
	go func() {
		time.Sleep(120 * time.Millisecond)
		a.rootApp().Shutdown()
	}()
}

func directorySize(root string) (int64, int) {
	var size int64
	files := 0
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err == nil {
			size += info.Size()
			files++
		}
		return nil
	})
	return size, files
}

func methodNotAllowed(w http.ResponseWriter) {
	writeMessage(w, http.StatusMethodNotAllowed, "service.handlers.requestMethodNotAllowed")
}

// Control requests are small. Validate the entire body before changing task state.
func decodeControlRequest(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	body := http.MaxBytesReader(w, r.Body, limit)
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func parseIntDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
