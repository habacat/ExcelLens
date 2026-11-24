package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"

	"excellens/internal/localstore"
)

func (a *App) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	roots, err := dataRoots()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var req struct {
		Book, Previous, Path string
		Confirm              bool
	}
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 96<<10))
		if err != nil || decodeSavedConfig(data, &req) != nil || !validExportDigest(req.Book) || !filepath.IsAbs(req.Path) {
			writeError(w, 400, "来源定位参数无效")
			return
		}
	}
	if r.Method == http.MethodGet {
		if !a.storageView.TryRLock() {
			writeError(w, 409, "数据传输进行中，可查看任务进度")
			return
		}
		defer a.storageView.RUnlock()
		if a.restoreState.pending() {
			writeError(w, 409, "请先完成恢复事务")
			return
		}
		items, err := relocationSources(r.Context())
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]any{"items": items})
		return
	}
	if !a.storageMu.TryLock() {
		writeError(w, 409, "另一项数据管理操作正在进行")
		return
	}
	if a.restoreState.pending() {
		a.storageMu.Unlock()
		writeError(w, 409, "请先完成恢复事务")
		return
	}
	action := "verify-source"
	if req.Confirm {
		action = "relocate-source"
	}
	job, err := a.submitStorage(roots, action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
		options.Report(localstore.Progress{Phase: "核验来源指纹"})
		item, err := a.verifyLocationWith(ctx, req.Book, req.Previous, req.Path, req.Confirm, options.BeginCommit)
		if err != nil {
			return transferResult{}, err
		}
		msg := "内容指纹相符"
		if req.Confirm {
			msg = "已更新来源位置"
		}
		return transferResult{Message: msg, Candidate: item.Candidate}, nil
	})
	if err != nil {
		a.storageMu.Unlock()
		writeError(w, 409, err.Error())
		return
	}
	writeJSONStatus(w, 202, job)
}
