package server

import (
	"context"
	"errors"

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
	roots, err := dataRoots()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		if !a.storageMu.TryLock() {
			writeError(w, 409, "数据传输进行中，可查看任务进度")
			return
		}
		defer a.storageMu.Unlock()
		writeJSON(w, map[string]any{"roots": roots, "groups": roots.Inventory(r.Context())})
		return
	}
	var req struct {
		Action    string `json:"action"`
		Directory string `json:"directory"`
		Password  string `json:"password,omitempty"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeError(w, 400, "数据管理参数无效")
		return
	}
	if !a.storageMu.TryLock() {
		writeError(w, 409, "另一项数据管理操作正在进行")
		return
	}
	if req.Action == "backup" || req.Action == "restore" || req.Action == "migrate" {
		job, err := a.startStorage(roots, req.Action, req.Directory, req.Password)
		if err != nil {
			a.storageMu.Unlock()
			writeError(w, 409, err.Error())
			return
		}
		writeJSONStatus(w, 202, job)
		return
	}
	defer a.storageMu.Unlock()
	if a.restorePending.Load() {
		writeError(w, 409, "请先完成恢复事务")
		return
	}
	var message string
	switch req.Action {
	case "clear-cache":
		message, err = a.ClearUnusedCaches()
	case "clear-uploads":
		message, err = a.ClearUnusedUploads()
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

func (a *App) restoreStorage(ctx context.Context, roots localstore.Roots, folder string, options localstore.TransferOptions) error {
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
	err := roots.RestoreWith(ctx, folder, options)
	a.restorePending.Store(err != nil && roots.Pending())
	return err
}
