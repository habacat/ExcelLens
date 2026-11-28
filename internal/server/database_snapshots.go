package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (a *App) handleDatabaseSnapshots(w http.ResponseWriter, r *http.Request) {
	root := a.rootApp()
	if r.Method == http.MethodGet {
		if !root.storageView.TryRLock() {
			writeMessage(w, 409, "service.databaseSnapshots.dataTransferProgressOpenSnapshot")
			return
		}
		defer root.storageView.RUnlock()
		items, err := workbook.ListDatabaseSnapshots(r.Context())
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		roots, err := dataRoots()
		if err != nil {
			writeFailure(w, 500, err)
			return
		}
		stages, err := roots.SnapshotStages(r.Context(), false)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		policy, err := loadSnapshotPolicy(roots.Config)
		response := map[string]any{"items": items, "stages": stages, "stageRoot": workbook.DatabaseSnapshotRoot(), "policy": policy}
		if err != nil {
			response["policyError"] = err.Error()
		}
		writeJSON(w, response)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Action string          `json:"action"`
		Key    string          `json:"key"`
		Policy *snapshotPolicy `json:"policy,omitempty"`
	}
	if !decodeDatabaseRequest(w, r, &req) {
		return
	}
	if req.Action == "policy" {
		if req.Policy == nil || !req.Policy.valid() {
			writeMessage(w, 400, "service.databaseSnapshots.capacityReviewDaysInvalid")
			return
		}
		if !root.storageMu.TryLock() {
			writeMessage(w, 409, "service.databaseSnapshots.anotherDataManagementOperationProgress")
			return
		}
		defer root.storageMu.Unlock()
		roots, err := dataRoots()
		if err == nil {
			if _, err = loadSnapshotPolicy(roots.Config); err == nil {
				err = writeSavedConfig(snapshotPolicyPath(roots.Config), 4096, req.Policy, durableConfigOps)
			}
		}
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		writeJSON(w, req.Policy)
		return
	}
	path := workbook.DatabaseSnapshotPath(req.Key)
	if path == "" {
		writeMessage(w, 400, "service.databaseSnapshots.databaseSnapshotIdentifierInvalid")
		return
	}
	ctx := r.Context()
	switch req.Action {
	case "open":
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			path = workbook.LegacyDatabaseSnapshotPath(req.Key)
		}
		wb, err := a.openDatabaseSnapshot(ctx, path, req.Key)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		a.writeWorkbookStatus(w, wb)
	case "verify", "export":
		evidence, err := workbook.VerifiedDatabaseEvidence(ctx, req.Key)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		if req.Action == "export" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-snapshot-`+req.Key+`.json"`)
			_, _ = w.Write(evidence)
			return
		}
		writeJSON(w, map[string]any{"key": req.Key, "verified": true, "verifiedAt": time.Now().UTC(), "evidence": evidence})
	case "retain":
		if !root.storageMu.TryLock() {
			writeMessage(w, 409, "service.databaseSnapshots.anotherDataManagementOperationProgress")
			return
		}
		defer root.storageMu.Unlock()
		old, err := workbook.OpenDatabaseSnapshot(ctx, workbook.LegacyDatabaseSnapshotPath(req.Key))
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		wb, err := old.RetainLegacyDatabaseSnapshot(ctx)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		writeJSON(w, map[string]string{"key": wb.Fingerprint, "name": wb.DisplayName})
	default:
		writeMessage(w, 400, "service.databaseSnapshots.selectDatabaseSnapshotOperation")
	}
}

type snapshotPolicy struct {
	BudgetBytes int64 `json:"budgetBytes"`
	ReviewDays  int   `json:"reviewDays"`
}

func (p snapshotPolicy) valid() bool {
	return p.BudgetBytes >= 0 && p.BudgetBytes < 1<<53 && p.ReviewDays >= 0 && p.ReviewDays <= 365000
}
func snapshotPolicyPath(config string) string {
	return filepath.Join(config, "settings", "database-snapshots.json")
}
func loadSnapshotPolicy(config string) (snapshotPolicy, error) {
	var p snapshotPolicy
	_, err := readSavedConfig(snapshotPolicyPath(config), 4096, &p)
	if err == nil && !p.valid() {
		err = i18n.Error("service.databaseSnapshots.snapshotCapacityOrReviewReminder")
	}
	return p, err
}

func (a *App) openDatabaseSnapshot(ctx context.Context, path, key string) (*workbook.Workbook, error) {
	expected := a.currentWorkbook()
	a.archiveMu.Lock()
	archive := a.archiveView
	a.archiveMu.Unlock()
	// Inactive legacy directories are protected as original data, including
	// while a verified open switches away from the current worksheet.
	wb, err := workbook.OpenDatabaseSnapshot(ctx, path)
	if err != nil {
		return nil, err
	}
	if wb.Fingerprint != key {
		return nil, i18n.Error("service.databaseSnapshots.identityDatabaseSnapshotDoesNot")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.archiveMu.Lock()
	if a.archiveView != archive || !a.installWorkbook(wb, expected, true) {
		a.archiveMu.Unlock()
		return nil, i18n.Error("service.databaseSnapshots.workbookSwitched")
	}
	view, job := a.detachArchiveLocked()
	a.archiveMu.Unlock()
	finishClosedArchive(view, job)
	a.recordRecent(wb)
	return wb, nil
}
