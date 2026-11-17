package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type dataRuleTask = sourceMetadataTask[workbook.SourceDataRuleIndex]

type dataRuleStartRequest struct {
	WorkbookKey string `json:"workbookKey"`
	Book        string `json:"book"`
	Sheet       string `json:"sheet"`
}

func (a *App) handleDataRuleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req dataRuleStartRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.DataRuleRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.Sheet == "" {
		writeError(w, 400, "数据规则读取参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "数据规则来源已切换，请重新读取")
		return
	}
	source, supported := formats.Lookup(wb.Format)
	if !supported || !source.DataRuleMetadata {
		writeError(w, 422, "此来源格式没有可读取的Excel数据规则")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	meta, found := wb.FindSheet(req.Sheet)
	if !found {
		writeError(w, 404, "数据规则所在工作表不存在")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, 409, "请先打开并完成该工作表索引，再读取数据规则")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &dataRuleTask{ID: randomID(), Mode: "read", Status: "queued", Stage: "等待读取数据规则", Workbook: wb, Meta: meta, Index: idx, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	if activeMetadataTasks(a.dataRuleJobs) >= formats.DataRuleActiveTasks {
		a.mu.Unlock()
		cancel()
		writeError(w, 429, "数据规则正在读取，请等待或取消后重试")
		return
	}
	a.pruneDataRulesLocked("", formats.DataRuleTaskHistory-1)
	unpin := a.pinWorkbooks(wb)
	a.dataRuleJobs[job.ID] = job
	a.mu.Unlock()
	go runSourceMetadataTask(a, job, unpin, "数据规则", func() bool {
		return a.wb == wb && a.dataRuleJobs[job.ID] == job && wb.GetCachedIndex(meta.Path) == idx
	}, func() { a.pruneDataRulesLocked(job.ID, formats.DataRuleTaskHistory) }, func(progress func(int64, int64, string)) (*workbook.SourceDataRuleIndex, error) {
		value, err := wb.SourceDataRules(job.ctx, meta, idx, progress)
		if err == nil && (value == nil || value.BudgetBytes() > formats.DataRuleSnapshotBytes) {
			return nil, errors.New("数据规则超过本次快照容量，未发布部分结果")
		}
		return value, err
	})
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "sheet": meta.Name})
}

func (a *App) pruneDataRulesLocked(keep string, maximum int) {
	pruneMetadataTasks(a.dataRuleJobs, keep, metadataTaskLimits{maximum, formats.DataRuleSnapshots, formats.DataRuleSnapshotBytes}, (*workbook.SourceDataRuleIndex).BudgetBytes)
}

func (a *App) clearDataRuleTasksLocked() []*dataRuleTask {
	return clearMetadataTasks(a.dataRuleJobs)
}

func (a *App) handleDataRuleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.dataRuleJobs[r.URL.Query().Get("id")]
	if job == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		writeError(w, 404, "数据规则任务已失效，请重新读取")
		return
	}
	payload := metadataTaskPayload(job)
	a.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) dataRuleAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.DataRuleRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	changed := metadataTaskAction(a.dataRuleJobs, id, clear)
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"changed": changed})
}

func (a *App) handleDataRuleCancel(w http.ResponseWriter, r *http.Request) {
	a.dataRuleAction(w, r, false)
}

func (a *App) handleDataRuleClear(w http.ResponseWriter, r *http.Request) {
	a.dataRuleAction(w, r, true)
}

func (a *App) dataRulesSnapshot(r *http.Request) (*dataRuleTask, *workbook.SourceDataRuleIndex, error) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.mu.RLock()
	job := a.dataRuleJobs[r.URL.Query().Get("id")]
	if job == nil || job.Status != "done" || job.Snapshot == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		return nil, nil, errors.New("数据规则结果已失效，请重新读取")
	}
	wb, snapshot := job.Workbook, job.Snapshot
	a.mu.RUnlock()
	if err := wb.CheckSourceUnchanged(); err != nil {
		return nil, nil, err
	}
	if wb.GetCachedIndex(job.Meta.Path) != job.Index {
		return nil, nil, errors.New("工作表索引已更换，请重新读取数据规则")
	}
	a.mu.RLock()
	current := a.wb == wb && a.dataRuleJobs[job.ID] == job && !job.discarded
	a.mu.RUnlock()
	if !current {
		return nil, nil, errors.New("数据规则来源已切换，请重新读取")
	}
	return job, snapshot, nil
}

func dataRuleQueryInt(r *http.Request, key string) (int, error) {
	q := r.URL.Query()
	if !q.Has(key) {
		return 0, nil
	}
	value, err := strconv.Atoi(q.Get(key))
	if err != nil || value < 0 {
		return 0, errors.New("数据规则分页或条目标识无效")
	}
	return value, nil
}

func dataRuleEnvelope(job *dataRuleTask) map[string]any {
	return map[string]any{"workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "sheet": job.Meta.Name}
}

func (a *App) handleDataRuleItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	offset, err := dataRuleQueryInt(r, "offset")
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	col, row := 0, 0
	if ref != "" {
		col, row, err = workbook.ParseCellRef(ref)
		if err != nil || len(ref) > 12 || !strings.EqualFold(ref, workbook.ColumnName(col)+strconv.Itoa(row)) {
			writeError(w, 400, "请选择有效的数据规则源单元格")
			return
		}
		ref = workbook.ColumnName(col) + strconv.Itoa(row)
	}
	job, snapshot, err := a.dataRulesSnapshot(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	page, err := snapshot.List(r.Context(), workbook.DataRuleQuery{Row: row, Col: col, Offset: offset, Count: formats.DataRulePageRows, Type: q.Get("type"), Status: q.Get("status")})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	summary := snapshot.Summary()
	summary.Containers = nil // Raw container attributes do not belong in every list page.
	payload := dataRuleEnvelope(job)
	payload["page"], payload["summary"], payload["ref"] = page, summary, ref
	writeJSON(w, payload)
}

func (a *App) handleDataRuleDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id, err1 := dataRuleQueryInt(r, "item")
	rangeOffset, err2 := dataRuleQueryInt(r, "rangeOffset")
	listOffset, err3 := dataRuleQueryInt(r, "listOffset")
	if err1 != nil || err2 != nil || err3 != nil || id < 1 {
		writeError(w, 400, "数据规则详情参数无效")
		return
	}
	job, snapshot, err := a.dataRulesSnapshot(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if id > snapshot.Summary().Rules {
		writeError(w, 404, "数据规则条目不存在")
		return
	}
	detail, err := snapshot.Detail(id, rangeOffset, listOffset, formats.DataRulePageRows)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	payload := dataRuleEnvelope(job)
	payload["detail"] = detail
	writeJSON(w, payload)
}

func (a *App) handleDataRuleResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id, err1 := dataRuleQueryInt(r, "item")
	area, err2 := dataRuleQueryInt(r, "area")
	if err1 != nil || err2 != nil || id < 1 {
		writeError(w, 400, "请选择有效的数据规则源区域")
		return
	}
	job, snapshot, err := a.dataRulesSnapshot(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if r.URL.Query().Get("workbookKey") != job.Workbook.Fingerprint || r.URL.Query().Get("book") != sourceBookID(job.Workbook) {
		writeError(w, 409, "定位来源已切换，请重新打开数据规则")
		return
	}
	rect, err := snapshot.Area(id, area)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	payload := dataRuleEnvelope(job)
	payload["area"] = map[string]string{"sheet": job.Meta.Name, "start": workbook.ColumnName(rect.Col) + strconv.Itoa(rect.Row), "end": workbook.ColumnName(rect.EndCol) + strconv.Itoa(rect.EndRow)}
	writeJSON(w, payload)
}
