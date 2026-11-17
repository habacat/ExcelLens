package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"excellens/internal/formats"
	"excellens/internal/printing"
	"excellens/internal/workbook"
)

type printTask = sourceMetadataTask[printSnapshot]

type printSnapshot struct {
	plan       *printing.Plan
	properties map[string]map[string]workbook.SourcePrintProperty
	merges     []workbook.CellMerge
	notices    []string
	complete   bool
	bytes      int64
}

func (snapshot *printSnapshot) BudgetBytes() int64 { return snapshot.bytes }

type printStartRequest struct {
	WorkbookKey string           `json:"workbookKey"`
	Book        string           `json:"book"`
	Sheet       string           `json:"sheet"`
	Options     printing.Options `json:"options"`
}

func (a *App) handlePrintStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req printStartRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.PrintPreviewRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.Sheet == "" {
		writeError(w, 400, "打印预览参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "打印预览来源已切换，请重新准备")
		return
	}
	source, supported := formats.Lookup(wb.Format)
	if !supported || !source.PrintMetadata {
		writeError(w, 422, "此来源格式没有可读取的Excel打印设置")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	meta, found := wb.FindSheet(req.Sheet)
	if !found {
		writeError(w, 404, "打印预览工作表不存在")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, 409, "请先打开并完成该工作表索引，再准备打印预览")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &printTask{ID: randomID(), Mode: "preview", Status: "queued", Stage: "等待准备打印预览", Workbook: wb, Meta: meta, Index: idx, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	if activeMetadataTasks(a.printJobs) >= formats.PrintPreviewActiveTasks {
		a.mu.Unlock()
		cancel()
		writeError(w, 429, "打印预览正在准备，请等待或取消后重试")
		return
	}
	a.prunePrintTasksLocked("", formats.PrintPreviewTaskHistory-1)
	unpin := a.pinWorkbooks(wb)
	a.printJobs[job.ID] = job
	a.mu.Unlock()
	go runSourceMetadataTask(a, job, unpin, "打印预览", func() bool {
		return a.wb == wb && a.printJobs[job.ID] == job && wb.GetCachedIndex(meta.Path) == idx
	}, func() { a.prunePrintTasksLocked(job.ID, formats.PrintPreviewTaskHistory) }, func(progress func(int64, int64, string)) (*printSnapshot, error) {
		source, err := wb.ReadPrintSource(job.ctx, meta, idx, progress)
		if err != nil {
			return nil, err
		}
		progress(0, 0, "正在计算纸张分页")
		plan, err := printing.New(job.ctx, source, req.Options)
		if err != nil {
			return nil, err
		}
		layout, err := wb.SourceLayoutContext(job.ctx, meta, idx)
		if err != nil {
			return nil, err
		}
		if len(layout.Merges) >= formats.PrintPreviewMergeLimit {
			return nil, errors.New("源合并区域达到显示元数据容量，无法确认完整预览，未发布部分结果")
		}
		if count, known := source.MergeCount(); known && count != len(layout.Merges) {
			return nil, errors.New("打印预览的公共合并元数据与完整来源声明不一致，未发布部分结果")
		}
		settings := source.Settings()
		propertyData, err := json.Marshal(settings.Properties)
		if err != nil {
			return nil, err
		}
		bytes := plan.BudgetBytes() + int64(len(layout.Merges))*64 + int64(len(propertyData))*2 + 4096
		if bytes > formats.PrintPreviewSnapshotBytes {
			return nil, errors.New("打印预览快照超过容量，未发布部分结果")
		}
		notices := slices.Clone(settings.Notices)
		if layout.StateNotice != "" {
			notices = append(notices, layout.StateNotice)
		}
		return &printSnapshot{plan: plan, properties: settings.Properties, merges: slices.Clone(layout.Merges), notices: notices, complete: settings.Complete, bytes: bytes}, nil
	})
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": wb.Fingerprint, "book": sourceBookID(wb), "sheet": meta.Name})
}

func (a *App) prunePrintTasksLocked(keep string, maximum int) {
	pruneMetadataTasks(a.printJobs, keep, metadataTaskLimits{maximum, formats.PrintPreviewSnapshots, formats.PrintPreviewSnapshotBytes}, (*printSnapshot).BudgetBytes)
}

func (a *App) handlePrintStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.printJobs[r.URL.Query().Get("id")]
	if job == nil || a.wb != job.Workbook || job.discarded {
		a.mu.RUnlock()
		writeError(w, 404, "打印预览任务已失效，请重新准备")
		return
	}
	payload := metadataTaskPayload(job)
	a.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) printAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.PrintPreviewRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	changed := metadataTaskAction(a.printJobs, id, clear)
	a.mu.Unlock()
	writeJSON(w, map[string]bool{"changed": changed})
}

func (a *App) handlePrintCancel(w http.ResponseWriter, r *http.Request) { a.printAction(w, r, false) }
func (a *App) handlePrintClear(w http.ResponseWriter, r *http.Request)  { a.printAction(w, r, true) }

// Keep both the cache read lock and a source pin until the request has finished
// reading and validating. Replacing a tab can invalidate the task concurrently;
// it cannot remove the old source while this request still owns its pin.
func (a *App) printSnapshotFor(r *http.Request) (*printTask, *printSnapshot, func(), error) {
	a.cacheMu.RLock()
	a.mu.RLock()
	job := a.printJobs[r.URL.Query().Get("id")]
	if job == nil || job.Status != "done" || job.Snapshot == nil || job.discarded || a.wb != job.Workbook {
		a.mu.RUnlock()
		a.cacheMu.RUnlock()
		return nil, nil, nil, errors.New("打印预览结果已失效，请重新准备")
	}
	snapshot := job.Snapshot
	unpin := a.pinWorkbooks(job.Workbook)
	a.mu.RUnlock()
	release := func() { unpin(); a.cacheMu.RUnlock() }
	if err := snapshot.plan.Validate(r.Context()); err != nil {
		release()
		return nil, nil, nil, err
	}
	return job, snapshot, release, nil
}

func (a *App) printSnapshotCurrent(ctx context.Context, job *printTask, snapshot *printSnapshot) error {
	if err := snapshot.plan.Validate(ctx); err != nil {
		return err
	}
	a.mu.RLock()
	current := a.wb == job.Workbook && a.printJobs[job.ID] == job && !job.discarded && job.Snapshot == snapshot
	a.mu.RUnlock()
	if !current {
		return errors.New("打印预览来源或任务已变化，请重新准备")
	}
	return nil
}

func printEnvelope(job *printTask) map[string]any {
	return map[string]any{"id": job.ID, "workbookKey": job.Workbook.Fingerprint, "book": sourceBookID(job.Workbook), "sheet": job.Meta.Name}
}

func (a *App) handlePrintSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job, snapshot, release, err := a.printSnapshotFor(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer release()
	payload := printEnvelope(job)
	payload["summary"], payload["sourceProperties"] = snapshot.plan.Summary(), snapshot.properties
	payload["sourceComplete"], payload["sourceNotices"] = snapshot.complete, snapshot.notices
	if err := a.printSnapshotCurrent(r.Context(), job, snapshot); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, payload)
}

func (a *App) handlePrintPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	index, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || index < 0 {
		writeError(w, 400, "打印预览页码无效")
		return
	}
	job, snapshot, release, err := a.printSnapshotFor(r)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer release()
	if index >= snapshot.plan.Summary().Pages {
		writeError(w, 400, "打印预览页码越界")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(job.ctx, cancel)
	defer func() { stop(); cancel() }()
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-ctx.Done():
		if r.Context().Err() == nil {
			writeError(w, 409, "打印预览任务已取消，请重新准备")
		}
		return
	}
	page, err := readPrintPage(ctx, job, snapshot, index)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			if r.Context().Err() == nil {
				writeError(w, 409, "打印预览任务已取消，请重新准备")
			}
			return
		}
		status := http.StatusInternalServerError
		if errors.Is(err, errPrintPageCapacity) {
			status = http.StatusRequestEntityTooLarge
		}
		if currentErr := a.printSnapshotCurrent(ctx, job, snapshot); currentErr != nil {
			err, status = currentErr, http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	payload := printEnvelope(job)
	payload["page"], payload["content"] = index, page
	data, err := json.Marshal(payload)
	if err != nil || len(data) > formats.PrintPreviewPageBytes {
		writeError(w, 413, "本页内容超过预览字节容量，请减少每页行列或缩小范围")
		return
	}
	if err := a.printSnapshotCurrent(ctx, job, snapshot); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
