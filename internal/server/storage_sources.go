package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"

	"excellens/internal/i18n"
	"excellens/internal/localstore"
)

func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	roots, err := dataRoots()
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	var req struct {
		Book, Previous, Path string
		Confirm              bool
	}
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 96<<10))
		if err != nil || decodeSavedConfig(data, &req) != nil || !validExportDigest(req.Book) || !filepath.IsAbs(req.Path) {
			writeMessage(w, 400, "service.storageSources.invalidSourceLocationParameter")
			return
		}
	}
	if r.Method == http.MethodGet {
		if !a.storageView.TryRLock() {
			writeMessage(w, 409, "service.storageSources.dataTransferProgressCanView")
			return
		}
		defer a.storageView.RUnlock()
		if a.restoreState.pending() {
			writeMessage(w, 409, "service.storageSources.completeRestorationProcessFirst")
			return
		}
		items, err := relocationSources(r.Context())
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		writeJSON(w, map[string]any{"items": items})
		return
	}
	if !a.storageMu.TryLock() {
		writeMessage(w, 409, "service.storageSources.anotherDataManagementOperationProgress")
		return
	}
	if a.restoreState.pending() {
		a.storageMu.Unlock()
		writeMessage(w, 409, "service.storageSources.completeRestorationProcessFirst")
		return
	}
	action := "verify-source"
	if req.Confirm {
		action = "relocate-source"
	}
	job, err := a.submitStorage(roots, action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
		options.Report(localstore.ProgressForStage("stage.task.verificationOfSourceFingerprint"))
		item, err := a.verifyLocationWith(ctx, req.Book, req.Previous, req.Path, req.Confirm, options.BeginCommit)
		if err != nil {
			return transferResult{}, err
		}
		notice := i18n.Message{Key: "service.transfer.sourceVerified"}
		if req.Confirm {
			notice.Key = "service.transfer.sourceRelocated"
		}
		return transferResult{Notice: notice, Candidate: item.Candidate}, nil
	})
	if err != nil {
		a.storageMu.Unlock()
		writeFailure(w, 409, err)
		return
	}
	writeJSONStatus(w, 202, job.Localized(responseLocale(w)))
}
