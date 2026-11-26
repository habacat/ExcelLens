package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type conditionalStatsPolicy struct {
	Version     int   `json:"version"`
	BudgetBytes int64 `json:"budgetBytes"`
}

func conditionalStatsPolicyPath(config string) string {
	return filepath.Join(config, "settings", "conditional-stats.json")
}

func validConditionalStatsBudget(bytes int64) bool {
	return bytes >= formats.ConditionalStatsMinBudgetBytes && bytes <= formats.ConditionalStatsMaxBudgetBytes
}

func loadConditionalStatsBudget() int64 {
	roots, err := dataRoots()
	if err != nil {
		return formats.ConditionalStatsDefaultBudgetBytes
	}
	path := conditionalStatsPolicyPath(roots.Config)
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	var policy conditionalStatsPolicy
	found, err := readSavedConfig(path, 4096, &policy)
	if err != nil || !found || policy.Version != 1 || !validConditionalStatsBudget(policy.BudgetBytes) {
		return formats.ConditionalStatsDefaultBudgetBytes
	}
	return policy.BudgetBytes
}

func saveConditionalStatsBudget(budgetBytes int64) error {
	if !validConditionalStatsBudget(budgetBytes) {
		return fmt.Errorf("条件统计缓存上限必须在 %s 到 %s 之间", formatReadableBytes(formats.ConditionalStatsMinBudgetBytes), formatReadableBytes(formats.ConditionalStatsMaxBudgetBytes))
	}
	roots, err := dataRoots()
	if err != nil {
		return err
	}
	path := conditionalStatsPolicyPath(roots.Config)
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	policy := conditionalStatsPolicy{Version: 1, BudgetBytes: budgetBytes}
	return writeSavedConfig(path, 4096, policy, durableConfigOps)
}

func formatReadableBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func (a *App) conditionalStatsProtector() func(name string) bool {
	root := a.rootApp()
	refs := root.referencedWorkbooks()
	if len(refs) == 0 {
		return nil
	}
	prefixes := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, wb := range refs {
		if wb == nil || wb.Fingerprint == "" {
			continue
		}
		p := workbook.ConditionalStatsFilePrefix(wb.Fingerprint)
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			prefixes = append(prefixes, p)
		}
	}
	if len(prefixes) == 0 {
		return nil
	}
	return func(name string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
}

func (a *App) conditionalStatsSheetStatus(sheetPath string) string {
	a.mu.RLock()
	id := a.conditionalStatsBySheet[sheetPath]
	job := a.conditionalStatsJobs[id]
	a.mu.RUnlock()
	if job == nil {
		return ""
	}
	job.mu.RLock()
	status := job.Status
	if job.ctx.Err() != nil && (status == "queued" || status == "running") {
		status = "cancelled"
	}
	job.mu.RUnlock()
	return status
}

func (a *App) handleConditionalStatsStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Sheet string `json:"sheet"`
	}
	if err := decodeControlRequest(w, r, 8<<10, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
		return
	}
	a.mu.RLock()
	maintenance := a.maintenance
	a.mu.RUnlock()
	if maintenance {
		writeError(w, http.StatusConflict, "正在维护本机缓存，请稍后重试")
		return
	}
	a.cacheMu.RLock()
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		a.cacheMu.RUnlock()
		writeError(w, http.StatusConflict, "工作表索引尚未建立")
		return
	}
	info, err := wb.ConditionalStatisticsState(r.Context(), meta, idx)
	a.cacheMu.RUnlock()
	if err != nil {
		writeJSON(w, map[string]any{"sheet": meta.Name, "needed": true, "ready": false, "status": "error", "error": err.Error()})
		return
	}
	if info.State == workbook.ConditionalStatsNotNeeded {
		writeJSON(w, map[string]any{"sheet": meta.Name, "needed": false, "ready": true, "status": "not_needed"})
		return
	}
	if info.State == workbook.ConditionalStatsReady {
		writeJSON(w, map[string]any{"sheet": meta.Name, "needed": true, "ready": true, "status": "ready"})
		return
	}
	damaged := info.State == workbook.ConditionalStatsDamaged

	a.mu.Lock()
	if a.maintenance {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "正在维护本机缓存，请稍后重试")
		return
	}
	if a.wb != wb {
		a.mu.Unlock()
		writeError(w, http.StatusConflict, "工作簿已切换，请重新选择工作表")
		return
	}
	if existingID := a.conditionalStatsBySheet[meta.Path]; existingID != "" {
		if job := a.conditionalStatsJobs[existingID]; job != nil {
			job.mu.RLock()
			active := (job.Status == "queued" || job.Status == "running") && job.ctx.Err() == nil
			status, isDamaged := job.Status, job.Damaged
			job.mu.RUnlock()
			if active {
				a.mu.Unlock()
				writeJSON(w, map[string]any{"sheet": meta.Name, "needed": true, "ready": false, "status": status, "jobId": existingID, "damaged": isDamaged})
				return
			}
		}
		delete(a.conditionalStatsBySheet, meta.Path)
	}
	if !pruneQueryHistory(a.conditionalStatsJobs, formats.ConditionalStatsTaskHistory, func(j *ConditionalStatsJob) queryTaskLifetime {
		return queryTaskLifetime{Started: j.Started, Done: j.done, Cancel: j.cancel}
	}) {
		a.mu.Unlock()
		writeError(w, http.StatusTooManyRequests, "条件统计任务已满，请稍后重试")
		return
	}
	for path, id := range a.conditionalStatsBySheet {
		if a.conditionalStatsJobs[id] == nil {
			delete(a.conditionalStatsBySheet, path)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	stage := "等待建立条件统计索引"
	if damaged {
		stage = "等待重建条件统计索引"
	}
	job := &ConditionalStatsJob{
		ID:      randomID(),
		Sheet:   meta.Name,
		Path:    meta.Path,
		Status:  "queued",
		Stage:   stage,
		Damaged: damaged,
		Started: time.Now(),
		cancel:  cancel,
		ctx:     ctx,
		done:    make(chan struct{}),
	}
	a.conditionalStatsJobs[job.ID] = job
	a.conditionalStatsBySheet[meta.Path] = job.ID
	a.mu.Unlock()

	go a.runConditionalStats(wb, meta, idx, job)
	writeJSON(w, map[string]any{"sheet": meta.Name, "needed": true, "ready": false, "status": "queued", "jobId": job.ID, "damaged": damaged})
}

func (a *App) runConditionalStats(wb *workbook.Workbook, meta workbook.SheetMeta, idx *workbook.SheetIndex, job *ConditionalStatsJob) {
	defer close(job.done)
	releaseSlot, admissionErr := a.backgroundIndex(job.ctx)
	if admissionErr != nil {
		job.mu.Lock()
		job.Status = "cancelled"
		job.Stage = "已取消"
		job.mu.Unlock()
		return
	}
	defer releaseSlot()

	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()

	runningStage := "正在建立条件统计索引"
	if job.Damaged {
		runningStage = "正在重建条件统计索引"
	}
	job.mu.Lock()
	job.Status = "running"
	job.Stage = runningStage
	job.mu.Unlock()

	budget := loadConditionalStatsBudget()
	isProtected := a.conditionalStatsProtector()
	err := wb.PrepareConditionalStatisticsWithPolicy(job.ctx, meta, idx, budget, isProtected, func(done, total int) {
		job.mu.Lock()
		defer job.mu.Unlock()
		job.Stage = runningStage
		job.DoneRows, job.TotalRows = done, total
		if total > 0 {
			job.Progress = float64(done) * 100 / float64(total)
		}
	})
	job.mu.Lock()
	defer job.mu.Unlock()
	if err != nil {
		if errors.Is(err, context.Canceled) || job.ctx.Err() != nil {
			job.Status = "cancelled"
			job.Stage = "已取消"
		} else {
			job.Status = "error"
			job.Stage = "条件统计失败"
			job.Error = err.Error()
		}
		return
	}
	job.Progress = 100
	job.DoneRows = job.TotalRows
	job.Status = "done"
	job.Stage = "条件统计完成"
}

func (a *App) handleConditionalStatsStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id := r.URL.Query().Get("id")
	var job *ConditionalStatsJob
	a.mu.RLock()
	if id != "" {
		job = a.conditionalStatsJobs[id]
	} else if sheet := r.URL.Query().Get("sheet"); sheet != "" && a.wb != nil {
		if meta, ok := a.wb.FindSheet(sheet); ok {
			if sid := a.conditionalStatsBySheet[meta.Path]; sid != "" {
				job = a.conditionalStatsJobs[sid]
			}
		}
	}
	a.mu.RUnlock()
	if job == nil {
		writeError(w, http.StatusNotFound, "条件统计任务不存在或已失效")
		return
	}
	job.mu.RLock()
	out := map[string]any{
		"id":        job.ID,
		"sheet":     job.Sheet,
		"status":    job.Status,
		"stage":     job.Stage,
		"progress":  job.Progress,
		"doneRows":  job.DoneRows,
		"totalRows": job.TotalRows,
		"damaged":   job.Damaged,
		"error":     job.Error,
	}
	job.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) handleConditionalStatsCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID    string `json:"id"`
		Sheet string `json:"sheet"`
	}
	if err := decodeControlRequest(w, r, 4096, &req); err != nil {
		writeError(w, http.StatusBadRequest, "请求内容无效")
		return
	}
	a.mu.RLock()
	job := a.conditionalStatsJobs[req.ID]
	if req.ID == "" && req.Sheet != "" && a.wb != nil {
		if meta, ok := a.wb.FindSheet(req.Sheet); ok {
			if sid := a.conditionalStatsBySheet[meta.Path]; sid != "" {
				job = a.conditionalStatsJobs[sid]
			}
		}
	}
	a.mu.RUnlock()
	if job == nil {
		writeJSON(w, map[string]any{"cancelled": false})
		return
	}
	job.cancel()
	writeJSON(w, map[string]any{"cancelled": true})
}

func (a *App) ClearUnusedConditionalStats() (string, error) {
	root := a.rootApp()
	root.cacheResetMu.RLock()
	defer root.cacheResetMu.RUnlock()
	if root.restoreState.pending() {
		return "", errors.New("请先完成恢复事务")
	}
	root.cacheMu.Lock()
	defer root.cacheMu.Unlock()
	report, err := workbook.ClearUnusedConditionalStats(root.conditionalStatsProtector())
	msg := fmt.Sprintf("已清理 %d 个条件统计索引（释放 %s）；保留正在使用的 %d 个。", report.RemovedFiles, formatReadableBytes(report.FreedBytes), report.KeptFiles)
	return msg, err
}

func (a *App) UpdateConditionalStatsBudget(budgetBytes int64) (string, error) {
	root := a.rootApp()
	root.cacheResetMu.RLock()
	defer root.cacheResetMu.RUnlock()
	if root.restoreState.pending() {
		return "", errors.New("请先完成恢复事务")
	}
	if err := saveConditionalStatsBudget(budgetBytes); err != nil {
		return "", err
	}
	root.cacheMu.Lock()
	evicted, err := workbook.EnforceConditionalStatsBudget(budgetBytes, root.conditionalStatsProtector())
	root.cacheMu.Unlock()
	msg := fmt.Sprintf("条件统计缓存上限已设为 %s。", formatReadableBytes(budgetBytes))
	if evicted.EvictedFiles > 0 {
		msg += fmt.Sprintf("已自动淘汰 %d 个闲置索引（释放 %s）。", evicted.EvictedFiles, formatReadableBytes(evicted.FreedBytes))
	}
	if err != nil {
		return msg, err
	}
	return msg, nil
}
