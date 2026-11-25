package server

import (
	"context"
	"errors"
	"net/http"
	"os"

	"excellens/internal/workbook"
)

func (a *App) handleDatabaseSnapshots(w http.ResponseWriter, r *http.Request) {
	root := a.rootApp()
	if r.Method == http.MethodGet {
		if !root.storageView.TryRLock() {
			writeError(w, 409, "数据传输进行中，请稍后再打开快照列表")
			return
		}
		defer root.storageView.RUnlock()
		items, err := workbook.ListDatabaseSnapshots(r.Context())
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]any{"items": items})
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Action string `json:"action"`
		Key    string `json:"key"`
	}
	if !decodeDatabaseRequest(w, r, &req) {
		return
	}
	path := workbook.DatabaseSnapshotPath(req.Key)
	if path == "" {
		writeError(w, 400, "数据库快照标识无效")
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
			writeError(w, 409, err.Error())
			return
		}
		a.writeWorkbookStatus(w, wb)
	case "verify":
		wb, err := workbook.OpenDatabaseSnapshot(ctx, path)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]any{"key": wb.Fingerprint, "verified": true, "evidence": wb.DatabaseSnapshotEvidence()})
	case "retain":
		if !root.storageMu.TryLock() {
			writeError(w, 409, "另一项数据管理操作正在进行")
			return
		}
		defer root.storageMu.Unlock()
		old, err := workbook.OpenDatabaseSnapshot(ctx, workbook.LegacyDatabaseSnapshotPath(req.Key))
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		wb, err := old.RetainLegacyDatabaseSnapshot(ctx)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]string{"key": wb.Fingerprint, "name": wb.DisplayName})
	default:
		writeError(w, 400, "请选择数据库快照操作")
	}
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
		return nil, errors.New("数据库快照身份与选择不一致")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.archiveMu.Lock()
	if a.archiveView != archive || !a.installWorkbook(wb, expected, true) {
		a.archiveMu.Unlock()
		return nil, errors.New("工作簿已切换")
	}
	view, job := a.detachArchiveLocked()
	a.archiveMu.Unlock()
	finishClosedArchive(view, job)
	a.recordRecent(wb)
	return wb, nil
}
