package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"

	"excellens/internal/localstore"
	"excellens/internal/transferjobs"
)

type transferResult = transferjobs.Result
type storageJob = transferjobs.Job

func (a *App) startStorage(roots localstore.Roots, action, dir, password string) (storageJob, error) {
	return a.submitStorage(roots, action, func(ctx context.Context, options localstore.TransferOptions) (transferResult, error) {
		options.Password = password
		var msg string
		var err error
		switch action {
		case "restore":
			err = a.restoreStorage(ctx, roots, dir, options)
			msg = "已核验并迁入备份，刷新页面可读取恢复记录"
		case "backup":
			a.cacheResetMu.RLock()
			var path string
			var missing int
			missing, err = a.prepareLegacyTasks(ctx)
			if err == nil {
				path, err = roots.BackupWith(ctx, dir, options)
			}
			a.cacheResetMu.RUnlock()
			msg = "备份已核验：" + path
			if missing > 0 {
				msg += fmt.Sprintf("（%d 项旧任务缺少可迁移的来源或索引证明）", missing)
			}
		case "migrate":
			a.cacheResetMu.RLock()
			var report localstore.Migration
			report, err = roots.MigrateLegacyWith(ctx, options)
			a.cacheResetMu.RUnlock()
			msg = fmt.Sprintf("已迁入 %d 个，相同 %d 个，内容不同且保留两份 %d 个", report.Copied, report.Identical, report.Different)
		}
		return transferResult{Message: msg}, err
	})
}
func (a *App) submitStorage(roots localstore.Roots, action string, run func(context.Context, localstore.TransferOptions) (transferResult, error)) (storageJob, error) {
	if a.restoreState.pending() {
		return storageJob{}, errors.New("请先完成恢复事务")
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
		writeError(w, 500, err.Error())
		return
	}
	file := filepath.Join(roots.Config, "transfer-history.json")
	id := r.URL.Query().Get("id")
	switch r.Method {
	case http.MethodGet:
		jobs, issue := a.transfers.List(file)
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
		writeError(w, 404, "传输任务不存在")
	case http.MethodPost:
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256))
		var empty struct{}
		if err != nil || len(b) > 0 && decodeSavedConfig(b, &empty) != nil {
			writeError(w, 400, "取消参数无效")
			return
		}
		if err := a.transfers.Cancel(file, id); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"cancelled": true})
	default:
		methodNotAllowed(w)
	}
}
