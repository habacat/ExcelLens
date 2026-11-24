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
		if !a.storageView.TryRLock() {
			writeError(w, 409, "数据传输进行中，可查看任务进度")
			return
		}
		defer a.storageView.RUnlock()
		writeJSON(w, map[string]any{"roots": roots, "groups": roots.Inventory(r.Context())})
		return
	}
	var req struct {
		Action    string `json:"action"`
		Directory string `json:"directory"`
		Password  string `json:"password,omitempty"`
		Stage     string `json:"stage,omitempty"`
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
	a.storageView.Lock()
	defer a.storageView.Unlock()
	if a.restoreState.pending() {
		writeError(w, 409, "请先完成恢复事务")
		return
	}
	var message string
	switch req.Action {
	case "list-stages":
		stages, e := roots.BackupStages(req.Directory)
		if e != nil {
			writeError(w, 409, e.Error())
			return
		}
		writeJSON(w, map[string]any{"stages": stages})
		return
	case "clean-stage":
		err = roots.CleanBackupStage(req.Directory, req.Stage)
		message = "已清理未完成备份"
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
	a.restoreActive.Store(true)
	defer a.restoreActive.Store(false)
	if err := a.restoreBlockers(); err != nil {
		return err
	}
	a.toolbar.mu.Lock()
	defer a.toolbar.notify()
	defer a.toolbar.mu.Unlock()
	defer func() { a.toolbar.loaded = false }()
	err := roots.RestoreWith(ctx, folder, options)
	if err != nil && roots.Pending() {
		a.restoreState.set(err)
	}
	return err
}
