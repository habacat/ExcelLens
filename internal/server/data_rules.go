package server

import (
	"context"

	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"excellens/internal/formats"
	"excellens/internal/i18n"
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
		writeMessage(w, 400, "service.dataRules.dataRuleReadingParametersInvalid")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.dataRules.sourceDataRulesChangedReload")
		return
	}
	source, supported := formats.Lookup(wb.Format)
	if !supported || !source.DataRuleMetadata {
		writeMessage(w, 422, "service.dataRules.thisSourceFormatDoesNot")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	meta, found := wb.FindSheet(req.Sheet)
	if !found {
		writeMessage(w, 404, "service.dataRules.worksheetContainingDataRulesDoes")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, 409, "service.dataRules.openCompleteWorksheetIndexBefore")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &dataRuleTask{ID: randomID(), Mode: "read", Status: "queued", Stage: "stage.task.waitingToReadDataRules", Workbook: wb, Meta: meta, Index: idx, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeMessage(w, 409, "service.dataRules.workbookSwitchedOrCurrentlyUnder")
		return
	}
	if activeMetadataTasks(a.dataRuleJobs) >= formats.DataRuleActiveTasks {
		a.mu.Unlock()
		cancel()
		writeMessage(w, 429, "service.dataRules.dataRulesBeingReadWait")
		return
	}
	a.pruneDataRulesLocked("", formats.DataRuleTaskHistory-1)
	unpin := a.pinWorkbooks(wb)
	a.dataRuleJobs[job.ID] = job
	a.mu.Unlock()
	go runSourceMetadataTask(a, job, unpin, func() bool {
		return a.wb == wb && a.dataRuleJobs[job.ID] == job && wb.GetCachedIndex(meta.Path) == idx
	}, func() { a.pruneDataRulesLocked(job.ID, formats.DataRuleTaskHistory) }, func(progress func(int64, int64, string)) (*workbook.SourceDataRuleIndex, error) {
		value, err := wb.SourceDataRules(job.ctx, meta, idx, progress)
		if err == nil && (value == nil || value.BudgetBytes() > formats.DataRuleSnapshotBytes) {
			return nil, i18n.Error("service.dataRules.dataRulesExceedCapacityThis")
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
		writeMessage(w, 404, "service.dataRules.dataRuleTaskBecomeInvalid")
		return
	}
	payload := metadataTaskPayload(responseLocale(w), job)
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
		return nil, nil, i18n.Error("service.dataRules.dataRuleResultBecomeInvalid")
	}
	wb, snapshot := job.Workbook, job.Snapshot
	a.mu.RUnlock()
	if err := wb.CheckSourceUnchanged(); err != nil {
		return nil, nil, err
	}
	if wb.GetCachedIndex(job.Meta.Path) != job.Index {
		return nil, nil, i18n.Error("service.dataRules.worksheetIndexesHaveChangedRead")
	}
	a.mu.RLock()
	current := a.wb == wb && a.dataRuleJobs[job.ID] == job && !job.discarded
	a.mu.RUnlock()
	if !current {
		return nil, nil, i18n.Error("service.dataRules.sourceDataRulesChangedReload")
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
		return 0, i18n.Error("service.dataRules.dataRulesPaginationOrItem")
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
		writeFailure(w, 400, err)
		return
	}
	q := r.URL.Query()
	ref := q.Get("ref")
	col, row := 0, 0
	if ref != "" {
		col, row, err = workbook.ParseCellRef(ref)
		if err != nil || len(ref) > 12 || !strings.EqualFold(ref, workbook.ColumnName(col)+strconv.Itoa(row)) {
			writeMessage(w, 400, "service.dataRules.selectValidDataRuleSource")
			return
		}
		ref = workbook.ColumnName(col) + strconv.Itoa(row)
	}
	job, snapshot, err := a.dataRulesSnapshot(r)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	page, err := snapshot.List(r.Context(), workbook.DataRuleQuery{Row: row, Col: col, Offset: offset, Count: formats.DataRulePageRows, Type: q.Get("type"), Status: q.Get("status")})
	if err != nil {
		writeFailure(w, 400, err)
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
		writeMessage(w, 400, "service.dataRules.dataRuleDetailsParametersInvalid")
		return
	}
	job, snapshot, err := a.dataRulesSnapshot(r)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	if id > snapshot.Summary().Rules {
		writeMessage(w, 404, "service.dataRules.dataRuleEntriesDoNot")
		return
	}
	detail, err := snapshot.Detail(id, rangeOffset, listOffset, formats.DataRulePageRows)
	if err != nil {
		writeFailure(w, 400, err)
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
		writeMessage(w, 400, "service.dataRules.chooseValidDataRuleSource")
		return
	}
	job, snapshot, err := a.dataRulesSnapshot(r)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	if r.URL.Query().Get("workbookKey") != job.Workbook.Fingerprint || r.URL.Query().Get("book") != sourceBookID(job.Workbook) {
		writeMessage(w, 409, "service.dataRules.sourcePositioningSwitchedReopenData")
		return
	}
	rect, err := snapshot.Area(id, area)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	payload := dataRuleEnvelope(job)
	payload["area"] = map[string]string{"sheet": job.Meta.Name, "start": workbook.ColumnName(rect.Col) + strconv.Itoa(rect.Row), "end": workbook.ColumnName(rect.EndCol) + strconv.Itoa(rect.EndRow)}
	writeJSON(w, payload)
}
