package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"excellens/internal/localstore"
	"excellens/internal/workbook"
)

func dataRoots() (localstore.Roots, error) {
	config, err := localstore.ConfigDir()
	return localstore.Roots{Config: config, Local: workbook.DataRoot()}, err
}

func lockWorkbookSetting(ctx context.Context, kind, fingerprint string) (string, func(), error) {
	roots, err := dataRoots()
	if err != nil {
		return "", nil, err
	}
	name := fingerprint + ".json"
	path := filepath.Join(roots.Config, kind, name)
	lock := persistentFileLock(path)
	lock.Lock()
	if _, err := roots.Adopt(ctx, kind, name); err != nil {
		lock.Unlock()
		return "", nil, err
	}
	return path, lock.Unlock, nil
}

// Storage is a process-wide operation. Dispatch it before acquiring the
// workbook request lock, since the maintenance methods take their own locks.
func (a *App) handleStorage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !a.storageMu.TryLock() {
		writeError(w, 409, "另一项数据管理操作正在进行")
		return
	}
	defer a.storageMu.Unlock()
	roots, err := dataRoots()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"roots": roots, "groups": roots.Inventory(r.Context())})
		return
	}
	var req struct {
		Action    string `json:"action"`
		Directory string `json:"directory"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeError(w, 400, "数据管理参数无效")
		return
	}
	var message string
	switch req.Action {
	case "clear-cache":
		message, err = a.ClearUnusedCaches()
	case "clear-uploads":
		message, err = a.ClearUnusedUploads()
	case "migrate":
		a.cacheResetMu.RLock()
		var report localstore.Migration
		report, err = roots.MigrateLegacy(r.Context())
		a.cacheResetMu.RUnlock()
		message = fmt.Sprintf("已迁入 %d 个，相同 %d 个，内容不同且保留两份 %d 个", report.Copied, report.Identical, report.Different)
	case "restore":
		err = a.restoreStorage(r.Context(), roots, req.Directory)
		if err == nil {
			message = "已核验并迁入备份，刷新页面可读取恢复记录"
		}
	case "backup":
		a.cacheResetMu.RLock()
		var folder string
		folder, err = roots.Backup(r.Context(), req.Directory)
		a.cacheResetMu.RUnlock()
		if err == nil {
			message = "备份已核验：" + folder
		}
	default:
		writeError(w, 400, "请选择数据管理操作")
		return
	}
	if err != nil {
		if message != "" {
			err = errors.New(message + "。" + err.Error())
		}
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]string{"message": message})
}

func (a *App) restoreStorage(ctx context.Context, roots localstore.Roots, folder string) error {
	a.cacheResetMu.Lock()
	defer a.cacheResetMu.Unlock()
	for _, app := range a.activeBrowserWorkbooks() {
		app.archiveMu.Lock()
		open := app.archiveView != nil || app.archiveTask != nil
		app.archiveMu.Unlock()
		if open || app.currentWorkbook() != nil {
			return errors.New("有工作簿正在使用。请关闭工作簿标签页，稍后从首页迁入")
		}
	}
	a.toolbar.mu.Lock()
	defer a.toolbar.mu.Unlock()
	defer func() { a.toolbar.loaded = false }()
	return roots.Restore(ctx, folder)
}
