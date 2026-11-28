package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"excellens/internal/formats"
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
		writeFailure(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		if !a.storageView.TryRLock() {
			writeMessage(w, 409, "service.storage.dataTransferProgressCanView")
			return
		}
		defer a.storageView.RUnlock()
		groups := roots.Inventory(r.Context(), responseLocale(w))
		usage, usageErr := workbook.InspectConditionalStatsCache(a.conditionalStatsProtector())
		condStats := map[string]any{
			"budgetBytes":    loadConditionalStatsBudget(),
			"minBudgetBytes": formats.ConditionalStatsMinBudgetBytes,
			"maxBudgetBytes": formats.ConditionalStatsMaxBudgetBytes,
			"usedBytes":      usage.Bytes,
			"files":          usage.Files,
			"protectedFiles": usage.ProtectedFiles,
			"protectedBytes": usage.ProtectedBytes,
		}
		if usageErr != nil {
			condStats["error"] = usageErr.Error()
		}
		writeJSON(w, map[string]any{"roots": roots, "groups": groups, "conditionalStats": condStats})
		return
	}
	var req struct {
		Action      string `json:"action"`
		Directory   string `json:"directory"`
		Password    string `json:"password,omitempty"`
		Stage       string `json:"stage,omitempty"`
		Revision    string `json:"revision,omitempty"`
		Confirm     bool   `json:"confirm,omitempty"`
		BudgetBytes int64  `json:"budgetBytes,omitempty"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeMessage(w, 400, "service.storage.dataManagementParametersInvalid")
		return
	}
	if !a.storageMu.TryLock() {
		writeMessage(w, 409, "service.storage.anotherDataManagementOperationProgress")
		return
	}
	if req.Action == "backup" || req.Action == "restore" || req.Action == "migrate" {
		job, err := a.startStorage(roots, req.Action, req.Directory, req.Password)
		if err != nil {
			a.storageMu.Unlock()
			writeFailure(w, 409, err)
			return
		}
		writeJSONStatus(w, 202, job.Localized(responseLocale(w)))
		return
	}
	defer a.storageMu.Unlock()
	a.storageView.Lock()
	defer a.storageView.Unlock()
	if a.restoreState.pending() {
		writeMessage(w, 409, "service.storage.completeRestorationProcessFirst")
		return
	}
	var message string
	switch req.Action {
	case "repair-history":
		if !req.Confirm || req.Revision == "" {
			writeMessage(w, 400, "service.storage.ensureThatDiagnosticCopyRetained")
			return
		}
		var evidence string
		evidence, err = a.transfers.Repair(filepath.Join(roots.Config, "transfer-history.json"), req.Revision)
		if evidence != "" {
			message = "原传输历史已保存在 " + evidence
		}
	case "list-stages":
		stages, e := roots.BackupStages(req.Directory)
		if e != nil {
			writeFailure(w, 409, e)
			return
		}
		writeJSON(w, map[string]any{"stages": stages})
		return
	case "clean-stage":
		err = roots.CleanBackupStage(req.Directory, req.Stage)
		message = "已清理未完成备份"
	case "clear-cache":
		message, err = a.clearUnusedCaches(responseLocale(w))
	case "clear-conditional-stats":
		message, err = a.ClearUnusedConditionalStats()
	case "set-conditional-stats-budget":
		message, err = a.UpdateConditionalStatsBudget(req.BudgetBytes)
	case "clear-uploads":
		message, err = a.clearUnusedUploads(responseLocale(w))
	default:
		writeMessage(w, 400, "service.storage.selectDataManagementOperation")
		return
	}
	if err != nil {
		if message != "" {
			err = errors.New(message + "。" + err.Error())
		}
		writeFailure(w, 409, err)
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
	defer a.locale.invalidate()
	err := roots.RestoreWith(ctx, folder, options)
	if err != nil && roots.Pending() {
		a.restoreState.set(err)
	}
	return err
}
