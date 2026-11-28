package server

import (
	"context"

	"io"
	"net/http"
	"path/filepath"

	"excellens/internal/i18n"
	"excellens/internal/localstore"
	"excellens/internal/transferjobs"
)

type transferResult = transferjobs.Result
type storageJob = transferjobs.Job

func (a *App) startStorage(roots localstore.Roots, action, dir, password string) (storageJob, error) {
	return a.submitStorage(roots, action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
		options.Password = password
		var notice i18n.Message
		var err error
		switch action {
		case "restore":
			err = a.restoreStorage(ctx, roots, dir, options)
			notice.Key = "service.transfer.restored"
		case "backup":
			a.cacheResetMu.RLock()
			var path string
			var missing int
			missing, err = a.prepareLegacyTasks(ctx)
			if err == nil {
				path, err = roots.BackupWith(ctx, dir, options)
			}
			a.cacheResetMu.RUnlock()
			notice = i18n.Message{Key: "service.transfer.backupVerified", Args: i18n.Args{"path": path}}
			if missing > 0 {
				notice.Key = "service.transfer.backupVerifiedWithMissingSources"
				notice.Args["count"] = missing
			}
		case "migrate":
			a.cacheResetMu.RLock()
			var report localstore.Migration
			report, err = roots.MigrateLegacyWith(ctx, options)
			a.cacheResetMu.RUnlock()
			notice = i18n.Message{Key: "service.transfer.migrated", Args: i18n.Args{
				"copied": report.Copied, "identical": report.Identical, "different": report.Different,
			}}
		}
		return transferResult{Notice: notice}, err
	})
}
func (a *App) submitStorage(roots localstore.Roots, action string, run func(context.Context, localstore.TransferOptions) (transferResult, error)) (storageJob, error) {
	if a.restoreState.pending() {
		return storageJob{}, i18n.Error("service.storageJobs.completeRestorationProcessFirst")
	}
	return a.transfers.Start(filepath.Join(roots.Config, "transfer-history.json"), randomID(), action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
		a.storageView.Lock()
		defer a.storageView.Unlock()
		return run(ctx, options)
	}, a.storageMu.Unlock)
}
func (a *App) handleStorageJobs(w http.ResponseWriter, r *http.Request) {
	roots, err := dataRoots()
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	file := filepath.Join(roots.Config, "transfer-history.json")
	id := r.URL.Query().Get("id")
	switch r.Method {
	case http.MethodGet:
		jobs, issue := a.transfers.List(file)
		for i := range jobs {
			jobs[i] = jobs[i].Localized(responseLocale(w))
		}
		if id == "" {
			writeJSON(w, map[string]any{"jobs": jobs, "historyIssue": issue})
			return
		}
		for _, job := range jobs {
			if job.ID == id {
				writeJSON(w, job)
				return
			}
		}
		writeMessage(w, 404, "service.storageJobs.transferTaskDoesNotExist")
	case http.MethodPost:
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256))
		var empty struct{}
		if err != nil || len(b) > 0 && decodeSavedConfig(b, &empty) != nil {
			writeMessage(w, 400, "service.storageJobs.invalidParameterCancellation")
			return
		}
		if err := a.transfers.Cancel(file, id); err != nil {
			writeFailure(w, 409, err)
			return
		}
		writeJSON(w, map[string]bool{"cancelled": true})
	default:
		methodNotAllowed(w)
	}
}
