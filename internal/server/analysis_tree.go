package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Child requests have a separate bounded history: expanding a tree must not
// evict its root result from the ordinary report-job pool. Sources are retained
// before queuing, independently of the original view, root and snapshot owners.
type analysisTreeTask struct {
	mu          sync.RWMutex
	ID, RootID  string
	Workbook    *workbook.Workbook
	Query       analysis.Query
	Path        []string
	SourceStamp string
	SourceRows  *analysisSourceSnapshot
	Index       *workbook.SheetIndex
	RecipePlan  *recipeRunPlan
	Status      string
	Error       string
	Progress    int64
	Total       int64
	Result      json.RawMessage
	Created     time.Time
	discarded   bool
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	release     func()
}

func (a *App) handleAnalysisTreeStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID       string   `json:"jobId"`
		WorkbookKey string   `json:"workbookKey"`
		Book        string   `json:"book"`
		Path        []string `json:"path"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.AnalysisTreeRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil {
		writeError(w, 400, "层级展开参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "层级透视来源已切换，请重新运行分析")
		return
	}
	job, err := a.startAnalysisTree(wb, req.JobID, req.Path)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (a *App) startAnalysisTree(wb *workbook.Workbook, rootID string, path []string) (*analysisTreeTask, error) {
	// The cache lock precedes analysisMu, as for ordinary analysis starts.
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	root := a.analysisJobs[rootID]
	if root == nil || a.currentWorkbook() != wb {
		return nil, errors.New("透视根结果已过期，请重新运行分析")
	}
	root.mu.RLock()
	defer root.mu.RUnlock()
	if root.Workbook != wb || root.Status != "done" || root.Result.Tree == nil || root.Query.Layout != "tree" {
		return nil, errors.New("请先完成层级透视分析，再展开分组")
	}
	if err := analysis.ValidateTreePath(root.Query, path); err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, errors.New("根层已在原结果中，请选择要展开的分组")
	}
	pathBytes := 0
	for _, key := range path {
		pathBytes += len(key)
		if pathBytes > formats.AnalysisTreePathBytes {
			return nil, errors.New("展开路径文本超出容量，请使用较短的分类字段")
		}
	}
	active := 0
	for _, old := range a.analysisTreeJobs {
		select {
		case <-old.done:
		default:
			active++
		}
	}
	if active >= formats.AnalysisTreeActiveTasks {
		return nil, errors.New("已有层级正在展开，请完成或取消后重试")
	}
	for len(a.analysisTreeJobs) >= formats.AnalysisTreeTasks {
		if !a.evictAnalysisTreeLocked("") {
			return nil, errors.New("层级任务已满，请稍后重试")
		}
	}
	var rows *analysisSourceSnapshot
	var idx *workbook.SheetIndex
	if root.SourceRows != nil {
		var err error
		rows, err = root.SourceRows.retain()
		if err != nil {
			return nil, err
		}
	} else if root.Query.SourceScope != nil || root.Query.Composition != nil {
		return nil, errors.New("根结果的来源范围已释放，请重新运行分析")
	}
	if strings.HasPrefix(root.Query.Source, "wb:") {
		meta, found := wb.FindSheet(strings.TrimPrefix(root.Query.Source, "wb:"))
		if found {
			idx = wb.GetCachedIndex(meta.Path)
		}
		if idx == nil {
			rows.close()
			return nil, errors.New("根结果的来源索引已失效，请重新运行分析")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &analysisTreeTask{ID: randomID(), RootID: rootID, Workbook: wb, Query: root.Query, Path: append([]string{}, path...), SourceStamp: root.SourceStamp, SourceRows: rows, Index: idx, RecipePlan: root.RecipePlan, Status: "queued", Total: root.Total, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), release: a.pinWorkbooks(wb)}
	if a.analysisTreeJobs == nil {
		a.analysisTreeJobs = make(map[string]*analysisTreeTask)
	}
	a.analysisTreeJobs[job.ID] = job
	go a.runAnalysisTree(job)
	return job, nil
}

func (a *App) evictAnalysisTreeLocked(except string) bool {
	var oldest *analysisTreeTask
	for id, job := range a.analysisTreeJobs {
		if id == except {
			continue
		}
		select {
		case <-job.done:
			if oldest == nil || job.Created.Before(oldest.Created) {
				oldest = job
			}
		default:
		}
	}
	if oldest == nil {
		return false
	}
	delete(a.analysisTreeJobs, oldest.ID)
	return true
}

func (a *App) runAnalysisTree(job *analysisTreeTask) {
	defer func() {
		job.SourceRows.close()
		job.release()
		// History owns only provenance and the bounded response, not closed
		// row-order/index graphs or the pin closure's captured workbooks.
		job.SourceRows, job.Index, job.RecipePlan, job.release = nil, nil, nil, nil
		close(job.done)
		a.analysisMu.Lock()
		if job.discarded && a.analysisTreeJobs[job.ID] == job {
			delete(a.analysisTreeJobs, job.ID)
		}
		a.analysisMu.Unlock()
	}()
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-job.ctx.Done():
		a.finishAnalysisTree(job, analysis.Result{}, job.ctx.Err())
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.mu.Unlock()
	release, err := a.acquireCompositionAnalysisSlot(job.ctx, job.Query.Source)
	if err != nil {
		a.finishAnalysisTree(job, analysis.Result{}, err)
		return
	}
	defer release()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if strings.HasPrefix(job.Query.Source, "wb:") {
		meta, found := job.Workbook.FindSheet(strings.TrimPrefix(job.Query.Source, "wb:"))
		if !found || job.Workbook.GetCachedIndex(meta.Path) != job.Index {
			a.finishAnalysisTree(job, analysis.Result{}, errors.New("来源索引已变化，未发布层级结果"))
			return
		}
		if err := job.Workbook.CheckSourceUnchanged(); err != nil {
			a.finishAnalysisTree(job, analysis.Result{}, err)
			return
		}
	}
	var observer sourceCellObserver
	if plan := job.RecipePlan; plan != nil {
		format := job.Workbook.Format
		if strings.HasPrefix(job.Query.Source, "scratch:") {
			format = "scratch"
		}
		observer = guardRecipeTypes(format, plan.Masks)
	}
	source, err := a.sourceQuery(job.ctx, job.Workbook, job.Query, observer, job.SourceRows)
	if err != nil {
		a.finishAnalysisTree(job, analysis.Result{}, err)
		return
	}
	defer source.close()
	if source.stamp() != job.SourceStamp {
		a.finishAnalysisTree(job, analysis.Result{}, errors.New("来源数据已变化，请重新运行层级透视"))
		return
	}
	scan := source.scan
	if job.RecipePlan != nil {
		scan = recipeScanInputs(scan, job.RecipePlan.Masks)
	}
	result, err := analysis.RunTreeLevel(job.ctx, job.Query, source.fields, job.Path, scan, func(scanned int64) { job.mu.Lock(); job.Progress = scanned; job.mu.Unlock() })
	a.finishAnalysisTree(job, result, err)
}

func (a *App) finishAnalysisTree(job *analysisTreeTask, result analysis.Result, err error) {
	var encoded []byte
	if err == nil {
		encoded, err = json.Marshal(result)
		if err == nil && cap(encoded) > formats.AnalysisTreeResultBytes {
			err = errors.New("此层级结果超过16 MiB保留预算，请缩小范围")
		}
	}
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.ctx.Err() != nil || job.discarded || a.currentWorkbook() != job.Workbook || a.analysisTreeJobs[job.ID] != job {
		job.Status = "cancelled"
		return
	}
	if err != nil {
		job.Status, job.Error = "error", err.Error()
		return
	}
	for {
		retained := cap(encoded)
		for _, old := range a.analysisTreeJobs {
			if old != job {
				old.mu.RLock()
				retained += cap(old.Result)
				old.mu.RUnlock()
			}
		}
		if retained <= formats.AnalysisTreeRetainedBytes {
			break
		}
		if !a.evictAnalysisTreeLocked(job.ID) {
			job.Status, job.Error = "error", "层级结果保留预算已满，请稍后重试"
			return
		}
	}
	job.Status, job.Result, job.Progress = "done", encoded, result.Scanned
}

func (a *App) handleAnalysisTreeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.analysisMu.Lock()
	job := a.analysisTreeJobs[r.URL.Query().Get("id")]
	if job != nil && !job.discarded && a.currentWorkbook() == job.Workbook {
		job.mu.RLock()
	} else {
		job = nil
	}
	a.analysisMu.Unlock()
	if job == nil {
		writeError(w, 404, "层级展开任务已过期或已清除")
		return
	}
	stage := "正在按路径聚合当前层"
	switch job.Status {
	case "queued":
		stage = "等待展开当前层"
	case "done":
		stage = "当前层已完成"
	case "error", "cancelled":
		stage = "当前层已停止"
	}
	payload := map[string]any{"status": job.Status, "error": job.Error, "stage": stage, "progress": job.Progress, "total": job.Total, "rootJobId": job.RootID, "path": job.Path, "result": job.Result}
	job.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) handleAnalysisTreeCancel(w http.ResponseWriter, r *http.Request) {
	a.changeAnalysisTree(w, r, false)
}

func (a *App) handleAnalysisTreeClear(w http.ResponseWriter, r *http.Request) {
	a.changeAnalysisTree(w, r, true)
}

func (a *App) changeAnalysisTree(w http.ResponseWriter, r *http.Request, discard bool) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.ID == "" {
		writeError(w, 400, "层级任务标识无效")
		return
	}
	a.analysisMu.Lock()
	job := a.analysisTreeJobs[req.ID]
	if job != nil {
		job.cancel()
		if discard {
			job.discarded = true // protected by analysisMu, including finish/defer
			select {
			case <-job.done:
				delete(a.analysisTreeJobs, req.ID)
			default:
			}
		}
	}
	a.analysisMu.Unlock()
	writeJSON(w, map[string]bool{"cancelled": job != nil, "cleared": discard && job != nil})
}
