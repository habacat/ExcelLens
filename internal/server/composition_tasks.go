package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"excellens/internal/composition"
	"excellens/internal/formats"
	"excellens/internal/sourceview"
	"excellens/internal/workbook"
)

type compositionChoice struct {
	Book      string            `json:"book"`
	Source    string            `json:"source"`
	HeaderRow int               `json:"headerRow"`
	Scope     *sourceview.Scope `json:"scope,omitempty"`
}

type compositionStartRequest struct {
	WorkbookKey string             `json:"workbookKey"`
	Book        string             `json:"book"`
	Mode        string             `json:"mode"` // fields or append
	Choice      *compositionChoice `json:"choice,omitempty"`
	Definition  *sourceview.Append `json:"definition,omitempty"`
	KeepID      string             `json:"keepId,omitempty"`
}

// App.mu protects task state and publication. A task owns its result and
// optional source lease; consumers retain another lease under that same lock.
// Resource-bearing multi-source tasks cannot use the metadata-only discard
// helpers, which deliberately do not close snapshots.
type compositionTask struct {
	ID, Mode, Status, Stage, Error string
	Primary                        *workbook.Workbook
	Books                          []*workbook.Workbook
	Choice                         *compositionChoice
	Definition                     *sourceview.Append
	Created                        time.Time
	Rows                           int64
	Source                         *compositionSnapshot
	Result                         json.RawMessage
	Bytes                          int64
	check                          func(context.Context) error
	release                        func()
	discarded                      bool
	ctx                            context.Context
	cancel                         context.CancelFunc
	done                           chan struct{}
}

type compositionCleanup struct {
	source  *compositionSnapshot
	release func()
}

func (a *App) handleCompositionLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	writeJSON(w, formats.Composition())
}

func (c compositionCleanup) close() {
	c.source.close()
	if c.release != nil {
		c.release()
	}
}

func closeCompositionResources(resources []compositionCleanup) {
	for _, resource := range resources {
		resource.close()
	}
}

func (a *App) handleCompositionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req compositionStartRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.CompositionRequestBytes))
	if err != nil || decodeSavedConfig(data, &req) != nil || req.Mode != "fields" && req.Mode != "append" || req.Mode == "fields" && (req.Choice == nil || req.Definition != nil) || req.Mode == "append" && (req.Definition == nil || req.Choice != nil) {
		writeError(w, 400, "多表来源参数无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	if primary == nil || primary.Fingerprint != req.WorkbookKey || sourceBookID(primary) != req.Book {
		writeError(w, 409, "工作簿已切换，请重新选择多表来源")
		return
	}
	var books []*workbook.Workbook
	if req.Mode == "append" {
		definition, err := req.Definition.Normalized()
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		req.Definition = &definition
		for _, input := range definition.Inputs {
			wb := a.resolveReconcileBook(input.Ref.Book)
			if wb == nil || wb.Fingerprint != input.Ref.Fingerprint {
				writeError(w, 409, "多表来源已关闭或变化，请重新选择")
				return
			}
			books = append(books, wb)
		}
	} else {
		choice := *req.Choice
		wb := a.resolveReconcileBook(choice.Book)
		if wb == nil || len(choice.Source) > 512 || choice.HeaderRow < 0 || choice.HeaderRow > formats.ExternalRows || !(strings.HasPrefix(choice.Source, "wb:") && len(choice.Source) > 3 || strings.HasPrefix(choice.Source, "scratch:") && len(choice.Source) > 8) {
			writeError(w, 400, "字段来源或表头无效")
			return
		}
		if strings.HasPrefix(choice.Source, "scratch:") && (choice.HeaderRow != 0 || choice.Scope != nil) {
			writeError(w, 400, "分析表不使用源表头或源范围")
			return
		}
		if choice.Scope != nil {
			scope, err := normalizedAnalysisScope(wb, choice.Source, *choice.Scope)
			if err != nil {
				writeError(w, 400, err.Error())
				return
			}
			choice.Scope = &scope
		}
		req.Choice = &choice
		books = []*workbook.Workbook{wb}
	}
	configuration, err := json.Marshal(req)
	if err != nil {
		writeError(w, 400, "来源配置无法读取")
		return
	}
	releaseConfiguration, err := a.reserveCompositionBytes(int64(len(configuration))*4 + 4096)
	if err != nil {
		writeError(w, 429, err.Error())
		return
	}
	accepted := false
	defer func() {
		if !accepted {
			releaseConfiguration()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	job := &compositionTask{ID: randomID(), Mode: req.Mode, Status: "queued", Stage: "等待来源任务", Primary: primary, Books: books, Choice: req.Choice, Definition: req.Definition, Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{})}
	a.mu.Lock()
	if a.wb != primary || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	active := 0
	keep := req.KeepID
	for _, old := range a.compositionJobs {
		select {
		case <-old.done:
		default:
			if old.ctx.Err() == nil {
				active++
			}
		}
		if req.KeepID == "" && old.Status == "done" && (keep == "" || old.Created.After(a.compositionJobs[keep].Created)) {
			keep = old.ID
		}
	}
	if active >= formats.CompositionActiveTasks {
		a.mu.Unlock()
		cancel()
		writeError(w, 429, "来源任务正在准备，请等待或取消后重试")
		return
	}
	retired := a.pruneCompositionLocked(keep, formats.CompositionTaskHistory-1, formats.CompositionSnapshots-1)
	if len(a.compositionJobs) >= formats.CompositionTaskHistory {
		a.mu.Unlock()
		closeCompositionResources(retired)
		cancel()
		writeError(w, 429, "正在回收来源任务，请稍后重试")
		return
	}
	releasePins := a.pinWorkbooks(append(slices.Clone(books), primary)...)
	unpin := func() { releasePins(); releaseConfiguration() }
	a.compositionJobs[job.ID] = job
	a.mu.Unlock()
	closeCompositionResources(retired)
	accepted = true
	go a.runCompositionTask(job, unpin)
	writeJSON(w, map[string]string{"jobId": job.ID, "workbookKey": primary.Fingerprint, "book": sourceBookID(primary)})
}

func (a *App) runCompositionTask(job *compositionTask, unpin func()) {
	var source *compositionSnapshot
	var result json.RawMessage
	var resultRelease func()
	var err error
	var rows int64
	var check func(context.Context) error
	defer func() {
		a.mu.Lock()
		if job.ctx.Err() != nil || job.discarded || a.wb != job.Primary || a.compositionJobs[job.ID] != job {
			job.Status, job.Stage, job.Error = "cancelled", "已取消", ""
		} else if err != nil {
			job.Status, job.Stage, job.Error = "error", "来源准备失败", err.Error()
		} else if result == nil {
			job.Status, job.Stage, job.Error = "error", "来源准备失败", "未取得来源结果"
		} else {
			job.Source, source = source, nil
			job.Result, job.Rows, job.Bytes = result, rows, int64(len(result))
			job.check = check
			job.Status, job.Stage = "done", "来源已就绪"
			job.release, resultRelease = resultRelease, nil
			if job.Mode == "fields" {
				bytesRelease, sourceRelease := job.release, unpin
				job.release = func() { bytesRelease(); sourceRelease() }
				unpin = nil
			}
		}
		job.Choice, job.Definition, job.Books = nil, nil, nil
		a.mu.Unlock()
		source.close()
		if resultRelease != nil {
			resultRelease()
		}
		if unpin != nil {
			unpin()
		}
		close(job.done)
		a.mu.Lock()
		retired := a.pruneCompositionLocked(job.ID, formats.CompositionTaskHistory, formats.CompositionSnapshots)
		a.mu.Unlock()
		closeCompositionResources(retired)
	}()
	// Slot acquisition precedes cacheMu.RLock. Rebuilt filter scopes explicitly
	// borrow this slot; neither nested waits nor a cache-writer inversion occurs.
	releaseScan, admissionErr := a.backgroundQuery(job.ctx)
	if admissionErr != nil {
		return
	}
	defer releaseScan()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.mu.Lock()
	current := a.wb == job.Primary && a.compositionJobs[job.ID] == job && !job.discarded && job.ctx.Err() == nil
	if current {
		job.Status, job.Stage = "running", "正在准备来源"
	}
	a.mu.Unlock()
	if !current {
		return
	}
	if err = job.Primary.CheckSourceUnchanged(); err != nil {
		return
	}
	var payload compositionTaskResult
	payload.ID, payload.Mode = job.ID, job.Mode
	payload.WorkbookKey, payload.Book = job.Primary.Fingerprint, sourceBookID(job.Primary)
	if job.Mode == "fields" {
		wb := job.Books[0]
		ref := sourceview.TableRef{Book: job.Choice.Book, Fingerprint: wb.Fingerprint, Source: job.Choice.Source, HeaderRow: job.Choice.HeaderRow, Scope: job.Choice.Scope}
		var info compositionSourceInfo
		var input composition.Source
		var release func()
		input, info, release, _, err = a.compositionSource(job.ctx, wb, ref)
		if release != nil {
			defer release()
		}
		if err != nil {
			return
		}
		if err = input.Check(job.ctx); err != nil {
			return
		}
		if err = compositionFieldSamples(job.ctx, wb, input, &info); err != nil {
			return
		}
		check = input.Check
		payload.Info, payload.Source, rows = &info, info.Ref.Source, info.Rows
	} else {
		source, err = a.captureComposition(job.ctx, *job.Definition)
		if err != nil {
			return
		}
		rows = source.data.plan.Rows()
		definition := source.data.plan.Definition()
		payload.Definition, payload.Source = &definition, "append:"+definition.Digest()
		for _, info := range source.data.info {
			info.Fields = nil
			payload.Sources = append(payload.Sources, info)
		}
		payload.Preview, payload.PreviewTruncated, err = compositionPreview(job.ctx, source)
		if err != nil {
			return
		}
	}
	payload.Rows = rows
	result, err = json.Marshal(payload)
	if err != nil {
		return
	}
	if len(result) > formats.CompositionResultBytes {
		err = errors.New("来源结果超过显示容量，请减少字段或来源")
		return
	}
	if resultRelease, err = a.reserveCompositionBytes(int64(len(result))*2 + 4096); err != nil {
		return
	}
	if err = job.Primary.CheckSourceUnchanged(); err != nil {
		return
	}
	err = job.ctx.Err()
}

// Callers release resources after unlocking App.mu, so file/mapping teardown
// never runs under the application state lock.
func retireCompositionLocked(job *compositionTask) compositionCleanup {
	job.cancel()
	job.discarded = true
	resource := compositionCleanup{source: job.Source, release: job.release}
	job.Source, job.release, job.Result, job.Bytes, job.check = nil, nil, nil, 0, nil
	return resource
}

func (a *App) pruneCompositionLocked(keep string, history, snapshots int) []compositionCleanup {
	var retired []compositionCleanup
	for {
		count, oldest := 0, ""
		for id, job := range a.compositionJobs {
			if job.Result != nil {
				count++
			}
			select {
			case <-job.done:
				if id != keep && (oldest == "" || job.Created.Before(a.compositionJobs[oldest].Created)) {
					oldest = id
				}
			default:
			}
		}
		if len(a.compositionJobs) <= history && count <= snapshots || oldest == "" {
			return retired
		}
		retired = append(retired, retireCompositionLocked(a.compositionJobs[oldest]))
		delete(a.compositionJobs, oldest)
	}
}

func (a *App) clearCompositionTasksLocked() ([]*compositionTask, []compositionCleanup) {
	jobs := make([]*compositionTask, 0, len(a.compositionJobs))
	resources := make([]compositionCleanup, 0, len(a.compositionJobs))
	for id, job := range a.compositionJobs {
		jobs = append(jobs, job)
		resources = append(resources, retireCompositionLocked(job))
		delete(a.compositionJobs, id)
	}
	return jobs, resources
}

func waitCompositionTasks(jobs []*compositionTask, resources []compositionCleanup) {
	closeCompositionResources(resources)
	for _, job := range jobs {
		<-job.done
	}
}

func (a *App) handleCompositionStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.compositionJobs[r.URL.Query().Get("id")]
	if job == nil || job.discarded || a.wb != job.Primary {
		a.mu.RUnlock()
		writeError(w, 404, "来源任务已失效")
		return
	}
	payload := map[string]any{"id": job.ID, "mode": job.Mode, "status": job.Status, "stage": job.Stage, "error": job.Error, "rows": job.Rows, "workbookKey": job.Primary.Fingerprint, "book": sourceBookID(job.Primary)}
	a.mu.RUnlock()
	writeJSON(w, payload)
}

func (a *App) compositionAction(w http.ResponseWriter, r *http.Request, clear bool) {
	id, err := metadataActionID(w, r, formats.CompositionRequestBytes)
	if err != nil {
		return
	}
	a.mu.Lock()
	job := a.compositionJobs[id]
	var resource compositionCleanup
	if job != nil {
		job.cancel()
		if job.Status == "done" || job.Status == "error" || job.Status == "cancelled" {
			clear = true
		}
		select {
		case <-job.done:
			clear = true
		default:
		}
		if clear {
			resource = retireCompositionLocked(job)
			delete(a.compositionJobs, id)
		}
	}
	a.mu.Unlock()
	resource.close()
	writeJSON(w, map[string]bool{"changed": job != nil})
}

func (a *App) handleCompositionCancel(w http.ResponseWriter, r *http.Request) {
	a.compositionAction(w, r, false)
}

func (a *App) handleCompositionClear(w http.ResponseWriter, r *http.Request) {
	a.compositionAction(w, r, true)
}

// Caller holds cacheMu.RLock. Pruning can close the task owner immediately
// after this returns, while the independent source lease remains valid.
func (a *App) borrowCompositionTask(id string, primary *workbook.Workbook) (*compositionSnapshot, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	job := a.compositionJobs[id]
	if job == nil || job.Status != "done" || job.ctx.Err() != nil || job.discarded || job.Source == nil || job.Primary != primary || a.wb != primary {
		return nil, errors.New("多表来源尚未准备或已失效，请重新准备")
	}
	return job.Source.retain()
}

func (a *App) handleCompositionResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	a.mu.RLock()
	job := a.compositionJobs[r.URL.Query().Get("id")]
	if job == nil || job.Status != "done" || job.ctx.Err() != nil || job.Result == nil || job.discarded || a.wb != job.Primary {
		a.mu.RUnlock()
		writeError(w, 404, "来源结果已失效")
		return
	}
	data, primary, check := job.Result, job.Primary, job.check
	var source *compositionSnapshot
	if job.Source != nil {
		var err error
		source, err = job.Source.retain()
		if err != nil {
			a.mu.RUnlock()
			writeError(w, 409, err.Error())
			return
		}
	}
	a.mu.RUnlock()
	defer source.close()
	if err := primary.CheckSourceUnchanged(); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if source != nil {
		check = source.data.plan.CheckSources
	}
	if check != nil {
		if err := check(r.Context()); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	a.mu.RLock()
	current := a.wb == primary && a.compositionJobs[job.ID] == job && !job.discarded && job.Result != nil
	a.mu.RUnlock()
	if !current {
		writeError(w, 409, "工作簿或来源结果已切换")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(data)
}
