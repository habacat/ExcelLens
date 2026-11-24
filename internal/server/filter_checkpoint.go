package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

const filterCheckpointDriver = "source-filter"
const filterCheckpointSemantics = 1

type filterRequest struct {
	Sheet       string                `json:"sheet"`
	WorkbookKey string                `json:"workbookKey"`
	Book        string                `json:"book"`
	Rules       []workbook.FilterRule `json:"rules"`
	HeaderRow   int                   `json:"headerRow"`
	SortColumn  int                   `json:"sortColumn"`
	Descending  bool                  `json:"descending"`
	SortKeys    []workbook.SortKey    `json:"sortKeys"`
	Recoverable bool                  `json:"recoverable,omitempty"`
}

func (req filterRequest) options() workbook.FilterOptions {
	return workbook.FilterOptions{HeaderRow: req.HeaderRow, SortColumn: req.SortColumn, Descending: req.Descending, SortKeys: req.SortKeys}
}

type filterCheckpointSpec struct {
	Sheet     string     `json:"sheet"`
	SheetPath string     `json:"sheetPath"`
	Filter    filterSpec `json:"filter"`
}

func filterTaskDefinition(wb *workbook.Workbook, sheet string, input filterSpec) (checkpoint.Definition, error) {
	if wb == nil {
		return checkpoint.Definition{}, checkpoint.ErrBinding
	}
	meta, found := wb.FindSheet(sheet)
	if !found {
		return checkpoint.Definition{}, checkpoint.ErrBinding
	}
	spec := filterCheckpointSpec{Sheet: meta.Name, SheetPath: meta.Path, Filter: newFilterSpec(input.Rules, input.options())}
	if err := workbook.ValidateFilterSpec(&workbook.SheetIndex{SourceFormat: wb.Format}, spec.Filter.Rules, spec.Filter.options()); err != nil {
		return checkpoint.Definition{}, err
	}
	payload, err := json.Marshal(spec)
	if err != nil {
		return checkpoint.Definition{}, err
	}
	if len(payload) > formats.CheckpointDefinitionBytes {
		return checkpoint.Definition{}, checkpoint.ErrCapacity
	}
	stampData, err := json.Marshal([]any{filterCheckpointSemantics, wb.Fingerprint, wb.Format, wb.Date1904, wb.SourceInfo, meta.Name, meta.Path})
	if err != nil {
		return checkpoint.Definition{}, err
	}
	stamp := sha256.Sum256(stampData)
	source, err := checkpointWorkbookSource(wb, "wb:"+meta.Name, hex.EncodeToString(stamp[:]))
	if err != nil {
		return checkpoint.Definition{}, err
	}
	return checkpoint.Definition{Kind: "query", Driver: filterCheckpointDriver, Semantics: filterCheckpointSemantics, Title: checkpointTitle("筛选", source.Path), FileName: workbookDisplayName(wb), Sources: []checkpoint.Source{source}, Payload: payload}, nil
}

func filterCheckpointSpecFor(m checkpoint.Manifest) (filterCheckpointSpec, error) {
	var spec filterCheckpointSpec
	if m.Definition.Kind != "query" || m.Definition.Driver != filterCheckpointDriver || m.Definition.Semantics != filterCheckpointSemantics || len(m.Definition.Sources) != 1 || decodeSavedConfig(m.Definition.Payload, &spec) != nil || spec.Sheet == "" || spec.SheetPath == "" || !spec.Filter.active() {
		return spec, checkpoint.ErrBinding
	}
	return spec, nil
}

func (a *App) startCheckpointFilter(w http.ResponseWriter, r *http.Request, wb *workbook.Workbook, req filterRequest) {
	if req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "筛选来源已切换，请重新运行")
		return
	}
	if !a.admitCheckpoint(w) {
		return
	}
	defer func() { <-a.rootApp().recoverySem }()
	def, err := filterTaskDefinition(wb, req.Sheet, newFilterSpec(req.Rules, req.options()))
	if err == nil {
		err = setTaskDigest(r.Context(), wb, &def)
	}
	if err != nil {
		if errors.Is(err, checkpoint.ErrCapacity) {
			checkpointHTTPError(w, err)
		} else {
			writeError(w, 400, err.Error())
		}
		return
	}
	store, err := openTaskCheckpoints()
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	cursor, _ := json.Marshal(filterTaskCursor{})
	lease, err := retryCheckpointBusy(r.Context(), func() (*checkpoint.Lease, error) { return store.Create(r.Context(), def, "prepare", cursor) })
	if err != nil {
		_ = store.Close()
		checkpointHTTPError(w, err)
		return
	}
	a.queueCheckpointFilter(w, r, wb, req.Sheet, store, lease, true)
}

func (a *App) queueCheckpointFilter(w http.ResponseWriter, r *http.Request, wb *workbook.Workbook, sheet string, store *checkpoint.Store, lease *checkpoint.Lease, created bool) {
	a.cacheMu.RLock()
	release := a.pinWorkbooks(wb)
	a.cacheMu.RUnlock()
	run := &taskCheckpointRun{store: store, lease: lease, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	manifest := lease.Snapshot()
	job := &FilterJob{ID: randomID(), TaskID: manifest.ID, Sheet: sheet, Status: "queued", Stage: "等待筛选任务", Started: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), checkpoint: run}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		if err := run.reject(created); err != nil {
			checkpointHTTPError(w, err)
			return
		}
		writeError(w, 409, "筛选来源已切换或请求已取消，请重试")
		return
	}
	if !pruneFilterJobsLocked(a.filterJobs, formats.QueryTaskHistory) {
		a.mu.Unlock()
		cancel()
		if err := run.reject(created); err != nil {
			checkpointHTTPError(w, err)
			return
		}
		writeError(w, http.StatusTooManyRequests, "正在回收筛选任务，请稍后重试")
		return
	}
	for _, old := range a.filterJobs {
		old.mu.RLock()
		active := old.Status == "queued" || old.Status == "running"
		old.mu.RUnlock()
		if active && old.Sheet == sheet {
			old.cancel()
		}
	}
	a.filterJobs[job.ID] = job
	a.mu.Unlock()
	a.retainSubmitted(job.ID, job.done)
	go a.runFilter(wb, workbook.SheetMeta{}, nil, nil, workbook.FilterOptions{}, job)
	spec, _ := filterCheckpointSpecFor(manifest)
	writeJSON(w, map[string]any{"jobId": job.ID, "taskId": manifest.ID, "recoverable": true, "operation": "filter", "filter": spec.Filter})
}

func (a *App) runCheckpointFilter(wb *workbook.Workbook, job *FilterJob) {
	a.cacheMu.RLock()
	var result *workbook.RowOrder
	var idx *workbook.SheetIndex
	var spec filterCheckpointSpec
	var err error
	func() {
		defer a.cacheMu.RUnlock()
		result, idx, spec, err = a.scanCheckpointFilter(wb, job)
	}()
	if err != nil {
		failFilterJob(job, err)
		return
	}
	meta, found := wb.FindSheet(spec.Sheet)
	if !found {
		_ = result.Close()
		failFilterJob(job, checkpoint.ErrBinding)
		return
	}
	a.publishFilterResult(wb, meta, idx, spec.Filter.Rules, spec.Filter.options(), result, job)
}

func (a *App) scanCheckpointFilter(wb *workbook.Workbook, job *FilterJob) (*workbook.RowOrder, *workbook.SheetIndex, filterCheckpointSpec, error) {
	lease := job.checkpoint.lease
	m := lease.Snapshot()
	spec, err := filterCheckpointSpecFor(m)
	if err != nil {
		return nil, nil, spec, err
	}
	failure := func(err error) (*workbook.RowOrder, *workbook.SheetIndex, filterCheckpointSpec, error) {
		return nil, nil, spec, err
	}
	def, err := filterTaskDefinition(wb, spec.Sheet, spec.Filter)
	if err != nil {
		return failure(err)
	}
	binding, err := taskBinding(job.ctx, wb, def, m, true)
	if err != nil || binding != m.DefinitionHash {
		return failure(checkpoint.ErrBinding)
	}
	cursor, err := filterCheckpointCursor(m, spec)
	if err != nil {
		return failure(err)
	}
	policy, err := indexPolicy(wb, m)
	if err != nil {
		return failure(err)
	}
	meta, _ := wb.FindSheet(spec.Sheet)
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		if cursor.Index != "" && !policy.rebuild {
			return failure(errors.New("筛选恢复所需索引已被清理，请重新运行"))
		}
		releaseIndex, admissionErr := a.backgroundIndex(job.ctx)
		if admissionErr != nil {
			return failure(job.ctx.Err())
		}
		idx, err = wb.BuildIndex(meta, nil, job.ctx.Done())
		releaseIndex()
		if err != nil {
			return failure(err)
		}
	}
	if err := workbook.ValidateFilterSpec(idx, spec.Filter.Rules, spec.Filter.options()); err != nil {
		return failure(err)
	}
	stamp, err := wb.CheckpointIndexStamp(job.ctx, meta, idx, policy.portable)
	if err != nil {
		return failure(err)
	}
	if cursor.Index == "" {
		cursor.Index, cursor.Rows = stamp, idx.RowCount()
		data, _ := json.Marshal(cursor)
		m, err = advanceTaskCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: "filter", Cursor: data}, nil)
		if err != nil {
			return failure(err)
		}
	} else if policy.expected(cursor.Index) != stamp || cursor.Rows != idx.RowCount() {
		return failure(checkpoint.ErrBinding)
	}
	check := func() error {
		current, err := wb.CheckpointIndexStamp(job.ctx, meta, idx, policy.portable)
		if err != nil {
			return err
		}
		if current != stamp {
			return checkpoint.ErrBinding
		}
		return nil
	}
	job.mu.Lock()
	job.Status, job.Total, job.Scanned = "running", idx.RowCount(), cursor.Next
	job.mu.Unlock()
	filtered, ordered, err := readFilterCheckpointOrders(job.ctx, wb, lease, cursor)
	if err != nil {
		return failure(err)
	}
	defer func() { _ = filtered.Close(); _ = ordered.Close() }()
	headers := min(cursor.Next, idx.FindRowOffset(spec.Filter.HeaderRow+1))
	if filtered.Len() < headers {
		return failure(checkpoint.ErrCorrupt)
	}
	for position := 0; position < headers; position++ {
		if filtered.At(position) != position || position < ordered.Len() && ordered.At(position) != position {
			return failure(checkpoint.ErrCorrupt)
		}
	}
	if !cursor.FilterComplete {
		// Prefix validation happened above. Only the new source suffix is read.
		_ = filtered.Close()
		_ = ordered.Close()
		state := workbook.FilterCheckpoint{Next: cursor.Next, Matched: cursor.Matched, Complete: cursor.FilterComplete}
		_, err = wb.FilterSheetResumable(job.ctx, idx, spec.Filter.Rules, spec.Filter.options(), state, func(saved workbook.FilterCheckpoint, delta []int) error {
			if saved.Next < cursor.Next || saved.Next > cursor.Rows || saved.Matched != cursor.Matched+len(delta) {
				return checkpoint.ErrCorrupt
			}
			previous := cursor.Next - 1
			for _, position := range delta {
				if position <= previous || position >= saved.Next {
					return checkpoint.ErrCorrupt
				}
				previous = position
			}
			if saved.Complete {
				if err := check(); err != nil {
					return err
				}
			}
			next := cursor
			next.Next, next.Matched, next.FilterComplete = saved.Next, saved.Matched, saved.Complete
			complete := saved.Complete && (!spec.sorted() || saved.Matched == 0)
			stage := "filter"
			if saved.Complete {
				stage = "sort"
			}
			if complete {
				stage = "done"
			}
			payload, err := encodeFilterPositions(delta)
			if err != nil {
				return err
			}
			data, _ := json.Marshal(next)
			committed, err := advanceTaskCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: stage, Cursor: data, Records: int64(len(delta)), Bytes: int64(len(payload)), Complete: complete}, payload)
			if err == nil {
				cursor, m = next, committed
			}
			return err
		}, func(done, total int) { filterCheckpointProgress(job, "正在筛选", done, total, 0, 75) })
		if err != nil {
			return failure(err)
		}
		filtered, ordered, err = readFilterCheckpointOrders(job.ctx, wb, lease, cursor)
		if err != nil {
			return failure(err)
		}
	}
	if !spec.sorted() {
		if err := check(); err != nil {
			return failure(err)
		}
		result, err := filtered.Retain()
		return result, idx, spec, err
	}
	if m.Complete {
		if err := check(); err != nil {
			return failure(err)
		}
		result, err := ordered.Retain()
		return result, idx, spec, err
	}
	opts := spec.Filter.options()
	opts.Phase = func(stage string, done, total int) { filterCheckpointProgress(job, stage, done, total, 85, 10) }
	filterCheckpointProgress(job, "正在排序", 0, filtered.Len(), 75, 10)
	result, err := wb.SortFilteredRowOrder(job.ctx, idx, filtered, opts, func(done, total int) { filterCheckpointProgress(job, "正在排序", done, total, 75, 10) })
	if err != nil {
		return failure(err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = result.Close()
		}
	}()
	if result.Len() != cursor.Matched {
		return failure(checkpoint.ErrCorrupt)
	}
	for position := 0; position < cursor.OrderWritten; position++ {
		if position%formats.CheckpointOrderRows == 0 && job.ctx.Err() != nil {
			return failure(job.ctx.Err())
		}
		if result.At(position) != ordered.At(position) {
			return failure(checkpoint.ErrCorrupt)
		}
	}
	for start := cursor.OrderWritten; start < result.Len(); start += formats.CheckpointOrderRows {
		end := min(result.Len(), start+formats.CheckpointOrderRows)
		positions := make([]int, 0, end-start)
		for position := range result.IterRange(start, end-start) {
			positions = append(positions, position)
		}
		complete := end == result.Len()
		if complete {
			if err := check(); err != nil {
				return failure(err)
			}
		}
		payload, err := encodeFilterPositions(positions)
		if err != nil {
			return failure(err)
		}
		next := cursor
		next.OrderWritten = end
		data, _ := json.Marshal(next)
		stage := "sort"
		if complete {
			stage = "done"
		}
		m, err = advanceTaskCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: stage, Cursor: data, Records: int64(len(positions)), Bytes: int64(len(payload)), Complete: complete}, payload)
		if err != nil {
			return failure(err)
		}
		cursor = next
		filterCheckpointProgress(job, "正在准备结果", end, result.Len(), 95, 4)
	}
	accepted = true
	return result, idx, spec, nil
}

func filterCheckpointProgress(job *FilterJob, stage string, done, total int, start, span float64) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Status, job.Stage = "running", stage
	if start == 0 {
		job.Scanned, job.Total = done, total
	}
	progress := start
	if total > 0 {
		progress += span * float64(done) / float64(total)
	}
	job.Progress = max(job.Progress, progress)
}
