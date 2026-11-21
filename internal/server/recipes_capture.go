package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/recipe"
	"excellens/internal/workbook"
)

const maxRecipeCaptures = 4

type recipeCaptureTask struct {
	mu                   sync.RWMutex
	ID                   string
	Workbook             *workbook.Workbook
	Query                analysis.Query
	SourceStamp          string
	Format               string
	Status, Stage, Error string
	Progress, Total      int64
	Draft                recipe.Recipe
	Created              time.Time
	ctx                  context.Context
	cancel               context.CancelFunc
	done                 chan struct{}
	release              func()
	SourceRows           *analysisSourceSnapshot
}

func (a *App) getRecipeCapture(id string) *recipeCaptureTask {
	a.recipeMu.Lock()
	defer a.recipeMu.Unlock()
	return a.recipeCaptures[id]
}

func (a *App) handleRecipeCaptureStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID       string `json:"jobId"`
		Book        string `json:"book"`
		WorkbookKey string `json:"workbookKey"`
		Format      string `json:"format"`
	}
	if decodeAnalysis(r, &req) != nil || (req.Format != "csv" && req.Format != "xlsx") {
		writeError(w, 400, "分析方案捕获参数无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	wb := a.currentWorkbook()
	if wb == nil || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，请重新选择分析结果")
		return
	}
	original := a.getAnalysisJob(req.JobID)
	if original == nil {
		writeError(w, 409, "分析结果已过期，请先重新运行分析")
		return
	}
	original.mu.RLock()
	if original.Status != "done" || original.Workbook != wb || original.SourceStamp == "" {
		original.mu.RUnlock()
		writeError(w, 409, "分析结果未完成或缺少来源快照")
		return
	}
	if original.Query.Composition != nil {
		original.mu.RUnlock()
		writeError(w, 409, "联合来源请保存报表；分析方案捕获尚未接入")
		return
	}
	encoded, _ := json.Marshal(original.Query)
	stamp := original.SourceStamp
	var captured *analysisSourceSnapshot
	if original.Query.SourceScope != nil {
		var err error
		captured, err = original.SourceRows.retain()
		if err != nil {
			original.mu.RUnlock()
			writeError(w, 409, err.Error())
			return
		}
	}
	original.mu.RUnlock()
	defer captured.close()
	var query analysis.Query
	if json.Unmarshal(encoded, &query) != nil {
		writeError(w, 400, "分析配置无法捕获")
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || a.maintenance {
		writeError(w, 409, "工作簿已切换或正在维护，未捕获分析方案")
		return
	}
	a.recipeMu.Lock()
	defer a.recipeMu.Unlock()
	for _, old := range a.recipeCaptures {
		select {
		case <-old.done:
		default:
			writeError(w, 429, "已有分析方案捕获任务，请先完成或取消")
			return
		}
	}
	for len(a.recipeCaptures) >= maxRecipeCaptures {
		var oldest *recipeCaptureTask
		for _, old := range a.recipeCaptures {
			if oldest == nil || old.Created.Before(oldest.Created) {
				oldest = old
			}
		}
		delete(a.recipeCaptures, oldest.ID)
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &recipeCaptureTask{ID: randomID(), Workbook: wb, Query: query, SourceStamp: stamp, Format: req.Format, Status: "queued", Stage: "等待字段类型捕获", Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), release: a.pinWorkbooks(wb)}
	if captured != nil {
		job.SourceRows, _ = captured.retain()
	}
	a.recipeCaptures[job.ID] = job
	go a.runRecipeCapture(job)
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (job *recipeCaptureTask) fail(err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Status, job.Stage, job.Error = "error", "分析方案捕获失败", err.Error()
	if errors.Is(err, context.Canceled) {
		job.Status, job.Stage, job.Error = "cancelled", "分析方案捕获已取消", ""
	}
}

func (a *App) runRecipeCapture(job *recipeCaptureTask) {
	defer close(job.done)
	defer job.release()
	defer job.SourceRows.close()
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		job.fail(job.ctx.Err())
		return
	}
	defer releaseScan()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	wb, q := job.Workbook, job.Query
	job.mu.Lock()
	job.Status, job.Stage = "running", "核对已完成分析的来源"
	job.mu.Unlock()
	ref := reconcileSourceRef{Book: sourceBookID(wb), Source: q.Source, HeaderRow: q.HeaderRow}
	if strings.HasPrefix(q.Source, "scratch:") {
		ref.Stamp = job.SourceStamp
	}
	meta, err := a.buildReconcileSource(job.ctx, wb, ref, nil)
	if err != nil {
		job.fail(err)
		return
	}
	needed := analysis.RequiredFields(q)
	if len(needed) > analysis.MaxSourceFields {
		job.fail(errors.New("分析方案引用字段超过当前分析容量"))
		return
	}
	fields := make([]recipe.Field, 0, len(needed))
	sourceFields := recipeSourceFields(meta.Info)
	byColumn := make(map[int]int, len(needed))
	for _, id := range needed {
		column, e := strconv.Atoi(strings.TrimPrefix(id, "C"))
		if e != nil || column < 1 || column > analysis.MaxSourceColumns || column > len(meta.Info.Fields) {
			job.fail(fmt.Errorf("分析方案字段%s不存在或超出当前分析范围", id))
			return
		}
		f := sourceFields[column-1]
		byColumn[column] = len(fields)
		fields = append(fields, f)
	}
	format := wb.Format
	if strings.HasPrefix(q.Source, "scratch:") {
		format = "scratch"
	}
	src, err := a.sourceQuery(job.ctx, wb, q, func(row int, cell workbook.CellData, numeric bool) error {
		if slot, ok := byColumn[cell.Col]; ok {
			fields[slot].Mask |= uint16(recipeCellMask(format, cell, numeric))
		}
		return job.ctx.Err()
	}, job.SourceRows)
	if err != nil {
		job.fail(err)
		return
	}
	defer src.close()
	if src.stamp() != job.SourceStamp {
		job.fail(errors.New("分析来源已修改，请重新运行分析后保存分析方案"))
		return
	}
	if err := analysis.Validate(q, src.fields); err != nil {
		job.fail(err)
		return
	}
	job.mu.Lock()
	job.Stage, job.Total = "逐行捕获实际读取类型", int64(src.total)
	job.mu.Unlock()
	if len(needed) > 0 {
		var scanned int64
		last := time.Now()
		err = src.scan(job.ctx, needed, func(analysis.Record) error {
			scanned++
			if scanned == int64(src.total) || scanned&4095 == 0 && time.Since(last) > 200*time.Millisecond {
				job.mu.Lock()
				job.Progress = scanned
				job.mu.Unlock()
				last = time.Now()
			}
			return job.ctx.Err()
		})
		if err == nil && scanned != int64(src.total) {
			err = errors.New("字段类型捕获未完整扫描来源")
		}
		if err != nil {
			job.fail(err)
			return
		}
	}
	for i := range fields {
		mask := recipeTypeMask(fields[i].Mask)
		fields[i].Uncertain = mask.uncertain()
		if mask == 0 || fields[i].Uncertain {
			job.fail(fmt.Errorf("字段%s的读取类型为%s，请先在普通分析中核对；未保存分析方案", fields[i].Name, mask.label()))
			return
		}
	}
	now := job.Created.UTC().Format(time.RFC3339Nano)
	draft := recipe.Recipe{Version: recipe.VersionForQuery(q), ID: randomID(), Name: "待保存分析方案", Origin: recipe.Origin{Name: workbookDisplayName(wb), Source: q.Source, Format: format, Fingerprint: wb.Fingerprint, HeaderRow: q.HeaderRow, SourceScope: q.SourceScope}, Fields: fields, Steps: recipe.StepsFromQuery(q, job.Format), Created: now, Updated: now}
	if err := validateSavedRecipe(draft); err != nil {
		job.fail(err)
		return
	}
	job.mu.Lock()
	if job.ctx.Err() != nil {
		job.mu.Unlock()
		job.fail(job.ctx.Err())
		return
	}
	job.Draft, job.Status, job.Stage, job.Progress = draft, "done", "字段类型捕获完成，尚未保存", int64(src.total)
	job.mu.Unlock()
}

func (a *App) handleRecipeCaptureStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getRecipeCapture(r.URL.Query().Get("id"))
	if job == nil || a.currentWorkbook() != job.Workbook {
		writeError(w, 404, "分析方案捕获已过期或工作簿已切换")
		return
	}
	job.mu.RLock()
	out := map[string]any{"status": job.Status, "stage": job.Stage, "error": job.Error, "progress": job.Progress, "total": job.Total}
	if job.Status == "done" {
		out["result"] = map[string]any{"captureId": job.ID, "fields": job.Draft.Fields, "steps": len(job.Draft.Steps), "origin": job.Draft.Origin}
	}
	job.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) handleRecipeCaptureCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeError(w, 400, "捕获任务参数无效")
		return
	}
	if job := a.getRecipeCapture(req.ID); job != nil {
		job.cancel()
	}
	writeJSON(w, map[string]bool{"cancelled": true})
}

func (a *App) handleRecipeCaptureCommit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID               string `json:"id"`
		Name             string `json:"name"`
		Book             string `json:"book"`
		WorkbookKey      string `json:"workbookKey"`
		ExpectedRevision *int64 `json:"expectedRevision"`
	}
	if decodeAnalysis(r, &req) != nil || req.ExpectedRevision == nil || !recipe.ValidName(strings.TrimSpace(req.Name)) {
		writeError(w, 400, "分析方案名称或保存参数无效")
		return
	}
	job := a.getRecipeCapture(req.ID)
	if job == nil {
		writeError(w, 409, "分析方案捕获已过期，请重新捕获")
		return
	}
	job.mu.RLock()
	if job.Status != "done" || job.ctx.Err() != nil {
		job.mu.RUnlock()
		writeError(w, 409, "分析方案捕获未完成或已取消")
		return
	}
	value := job.Draft
	wb := job.Workbook
	job.mu.RUnlock()
	value.Name = strings.TrimSpace(req.Name)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || req.Book != sourceBookID(wb) || req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，分析方案未保存")
		return
	}
	revision, err := saveCapturedRecipe(value, *req.ExpectedRevision)
	if err != nil {
		code := 500
		if errors.Is(err, errRecipeRevision) || errors.Is(err, errRecipeName) {
			code = 409
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": value.ID, "revision": revision})
}

func (a *App) cancelRecipeCaptures() {
	a.recipeMu.Lock()
	defer a.recipeMu.Unlock()
	for _, job := range a.recipeCaptures {
		job.cancel()
	}
}
func (a *App) detachRecipeCaptures() func() {
	a.recipeMu.Lock()
	jobs := a.recipeCaptures
	a.recipeCaptures = make(map[string]*recipeCaptureTask)
	for _, job := range jobs {
		job.cancel()
	}
	a.recipeMu.Unlock()
	return func() {
		for _, job := range jobs {
			<-job.done
		}
	}
}
