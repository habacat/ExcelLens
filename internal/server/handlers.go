package server

import (
	"encoding/json"
	"errors"
	"fmt"
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
	"excellens/internal/ui"
	"excellens/internal/workbook"
)

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	root := a.rootApp()
	if root.restoreState.pending() {
		page, err := ui.RecoveryIndex(root.token)
		if err != nil {
			http.Error(w, "恢复界面不可用", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
		return
	}
	ribbon, _ := root.RibbonLayout() // keep the page usable if a preference is unreadable
	b, err := ui.IndexWithLayout(a.token, ribbon, root.cacheEpoch.Load())
	if err != nil {
		http.Error(w, "界面资源不可用", http.StatusInternalServerError)
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
		writeJSON(w, map[string]any{"version": buildinfo.Version, "opened": false, "error": startupError, "archive": a.archiveSummary()})
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
	writeJSON(w, map[string]any{
		"version":     buildinfo.Version,
		"opened":      true,
		"workbookKey": wb.Fingerprint,
		"book":        sourceBookID(wb),
		"fileName":    workbookDisplayName(wb),
		"fileSize":    size,
		"sheetCount":  len(wb.Sheets),
		"activeTab":   wb.ActiveTab,
		"sheets":      wb.Sheets,
		"source":      wb.SourceInfo,
		"archive":     a.archiveSummary(),
	})
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
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	writeJSON(w, map[string]any{"styles": wb.VisualStyles, "richTextNotice": wb.RichTextNotice()})
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
		writeError(w, http.StatusBadRequest, "读取文件失败："+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	f, h, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请先选择 Excel、CSV、TXT 或 SQLite 数据文件")
		return
	}
	defer f.Close()
	if h.Size <= 0 {
		writeError(w, http.StatusBadRequest, "文件为空")
		return
	}
	if h.Size > maxFileSize {
		writeError(w, http.StatusRequestEntityTooLarge, "文件超过 1 TiB 上传限制；超大本机文件可直接通过右键菜单或命令行打开")
		return
	}
	ext := strings.ToLower(filepath.Ext(h.Filename))
	if !workbook.SupportedExtensions(ext) {
		writeError(w, http.StatusBadRequest, "请选择受支持的数据文件或压缩包")
		return
	}
	a.cacheMu.RLock()
	tmpPath, err := persistUploadedFile(f, ext, h.Size)
	if err != nil {
		a.cacheMu.RUnlock()
		writeError(w, http.StatusInternalServerError, "保存本机临时文件失败："+err.Error())
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
			writeError(w, 400, "无法读取压缩包："+err.Error())
			return
		}
		writeJSON(w, map[string]any{"archiveOnly": true, "opened": false, "archive": a.archiveSummary()})
		return
	}
	a.closeArchive()
	wb, releaseBook, err := a.openPinnedWorkbook(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		writeError(w, http.StatusBadRequest, "无法打开工作簿："+err.Error())
		return
	}
	defer releaseBook()
	wb.SetDisplayName(filepath.Base(h.Filename))
	a.replaceWorkbook(wb)
	a.recordRecent(wb)
	writeJSON(w, map[string]any{
		"opened":      true,
		"workbookKey": wb.Fingerprint,
		"book":        sourceBookID(wb),
		"fileName":    filepath.Base(h.Filename),
		"fileSize":    h.Size,
		"sheetCount":  len(wb.Sheets),
		"activeTab":   wb.ActiveTab,
		"sheets":      wb.Sheets,
		"source":      wb.SourceInfo,
	})
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
			return "", fmt.Errorf("文件复制长度不一致：期望 %d，实际 %d", expected, copied)
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
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	if wb.HasMappedIndexes() || workbook.QueryFilesInUse(wb.CacheDir) {
		writeError(w, http.StatusConflict, "当前工作簿仍有在用索引或查询文件，请退出程序后清理本机缓存；在用数据不会被删除")
		return
	}
	if a.otherTabUsesCache(wb.CacheDir) {
		writeError(w, http.StatusConflict, "其他页面、可见行视图或核对任务仍在使用此来源，请先关闭或清除相应结果")
		return
	}
	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "缓存维护正在进行")
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
		clearErr, conflict = errors.New("工作簿已切换，缓存清理已取消"), true
	} else if wb.HasMappedIndexes() || workbook.QueryFilesInUse(wb.CacheDir) || a.otherTabUsesCache(wb.CacheDir) {
		clearErr, conflict = errors.New("缓存仍有在用索引或查询结果，未执行清理"), true
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
		a.searchJobs = make(map[string]*SearchJob)
	}
	a.maintenance = false
	a.mu.Unlock()
	if clearErr != nil {
		if conflict {
			writeError(w, http.StatusConflict, clearErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "清理缓存失败："+clearErr.Error())
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
	writeError(w, http.StatusMethodNotAllowed, "请求方法不允许")
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

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]any{"error": message})
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
