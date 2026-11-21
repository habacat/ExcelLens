package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
	"excellens/internal/searchdsl"
	"excellens/internal/workbook"
)

// Bump when search interpretation, presentation or the resumable cursor changes.
// This is independent of disposable completed-search caches and source caches.
const searchCheckpointDriver = "source-search"

func searchCheckpointSemantics(format string) uint32 {
	if format == "xlsx" {
		return 3 // Includes the displayed values of elapsed-time number formats.
	}
	return 1
}

type searchRequest struct {
	Sheet       string `json:"sheet"`
	Query       string `json:"query"`
	Mode        string `json:"mode"`
	Scope       string `json:"scope"`
	Recoverable bool   `json:"recoverable,omitempty"`
	WorkbookKey string `json:"workbookKey,omitempty"`
	Book        string `json:"book,omitempty"`
}

func (req *searchRequest) normalize() error {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return errors.New("请输入搜索内容")
	}
	maximum := 256
	if req.Mode == "advanced" {
		maximum = searchdsl.MaxQueryLength
	}
	if len(req.Query) > maximum {
		return fmt.Errorf("搜索内容不能超过 %d 个字符", maximum)
	}
	if req.Mode != "value" && req.Mode != "reference" && req.Mode != "advanced" {
		req.Mode = "formula"
	}
	if req.Mode == "advanced" {
		if _, err := searchdsl.Parse(req.Query); err != nil {
			return err
		}
	}
	if req.Scope != "all" {
		req.Scope = "current"
	}
	return nil
}

type searchCheckpointSheet struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type searchCheckpointSpec struct {
	Query  string                  `json:"query"`
	Mode   string                  `json:"mode"`
	Scope  string                  `json:"scope"`
	Sheet  string                  `json:"sheet"`
	Limit  int                     `json:"limit"`
	Sheets []searchCheckpointSheet `json:"sheets"`
}

// Hits are appended once in verified blocks; the small cursor never embeds
// snippets or repeatedly copies the existing result prefix into the journal.
type searchTaskCursor struct {
	Sheet         int    `json:"sheet"`
	Index         string `json:"index"`
	Rows          int    `json:"rows"`
	Next          int    `json:"next"`
	Before        int    `json:"before"`
	Count         int    `json:"count"`
	ScannedBefore int    `json:"scannedBefore"`
	SheetComplete bool   `json:"sheetComplete"`
	Truncated     bool   `json:"truncated"`
}

func searchTaskDefinition(wb *workbook.Workbook, req searchRequest) (checkpoint.Definition, error) {
	if wb == nil {
		return checkpoint.Definition{}, checkpoint.ErrBinding
	}
	if err := req.normalize(); err != nil {
		return checkpoint.Definition{}, err
	}
	spec := searchCheckpointSpec{Query: req.Query, Mode: req.Mode, Scope: req.Scope, Sheet: req.Sheet, Limit: formats.CheckpointSearchHits}
	if req.Mode != "advanced" {
		spec.Query = strings.ToLower(spec.Query)
	}
	for _, sheet := range wb.Sheets {
		if req.Scope == "all" || sheet.Name == req.Sheet {
			spec.Sheets = append(spec.Sheets, searchCheckpointSheet{Name: sheet.Name, Path: sheet.Path})
		}
	}
	if len(spec.Sheets) == 0 {
		return checkpoint.Definition{}, checkpoint.ErrBinding
	}
	semantics := searchCheckpointSemantics(wb.Format)
	stampData, err := json.Marshal([]any{semantics, wb.Fingerprint, wb.Format, wb.Date1904, wb.SourceInfo, spec.Sheets})
	if err != nil {
		return checkpoint.Definition{}, err
	}
	stamp := sha256.Sum256(stampData)
	payload, err := json.Marshal(spec)
	if err != nil {
		return checkpoint.Definition{}, err
	}
	source := "workbook"
	if req.Scope != "all" {
		source = "wb:" + req.Sheet
	}
	bound, err := checkpointWorkbookSource(wb, source, hex.EncodeToString(stamp[:]))
	if err != nil {
		return checkpoint.Definition{}, err
	}
	return checkpoint.Definition{Kind: "query", Driver: searchCheckpointDriver, Semantics: semantics,
		Title: checkpointTitle("搜索", bound.Path), FileName: workbookDisplayName(wb), Sources: []checkpoint.Source{bound}, Payload: payload}, nil
}

func (a *App) startCheckpointSearch(w http.ResponseWriter, r *http.Request, wb *workbook.Workbook, req searchRequest) {
	if req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "搜索来源已切换，请重新运行")
		return
	}
	if !a.admitCheckpoint(w) {
		return
	}
	defer func() { <-a.rootApp().recoverySem }()
	def, err := searchTaskDefinition(wb, req)
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	store, err := openTaskCheckpoints()
	if err != nil {
		checkpointHTTPError(w, err)
		return
	}
	cursor, _ := json.Marshal(searchTaskCursor{})
	lease, err := retryCheckpointBusy(r.Context(), func() (*checkpoint.Lease, error) {
		return store.Create(r.Context(), def, "prepare", cursor)
	})
	if err != nil {
		_ = store.Close()
		checkpointHTTPError(w, err)
		return
	}
	a.queueCheckpointSearch(w, r, wb, req, store, lease, true)
}

// Registration and source pin ownership match ordinary searches. The request
// may disconnect after registration without cancelling the user's durable job.
func (a *App) queueCheckpointSearch(w http.ResponseWriter, r *http.Request, wb *workbook.Workbook, req searchRequest, store *checkpoint.Store, lease *checkpoint.Lease, created bool) {
	a.cacheMu.RLock()
	release := a.pinWorkbooks(wb)
	a.cacheMu.RUnlock()
	run := &taskCheckpointRun{store: store, lease: lease, release: release}
	ctx, cancel := context.WithCancel(context.Background())
	m := lease.Snapshot()
	job := &SearchJob{ID: randomID(), TaskID: m.ID, Sheet: req.Sheet, Query: req.Query, Mode: req.Mode, Scope: req.Scope,
		Status: "running", Stage: "准备搜索", Started: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), checkpoint: run}
	a.mu.Lock()
	if a.wb != wb || a.maintenance || r.Context().Err() != nil {
		a.mu.Unlock()
		cancel()
		if err := run.reject(created); err != nil {
			checkpointHTTPError(w, err)
			return
		}
		writeError(w, 409, "搜索来源已切换或请求已取消，请重试")
		return
	}
	if !pruneSearchJobsLocked(a.searchJobs, formats.QueryTaskHistory) {
		a.mu.Unlock()
		cancel()
		if err := run.reject(created); err != nil {
			checkpointHTTPError(w, err)
			return
		}
		writeError(w, http.StatusTooManyRequests, "正在回收搜索任务，请稍后重试")
		return
	}
	a.searchJobs[job.ID] = job
	a.mu.Unlock()
	go a.runSearch(wb, workbook.SheetMeta{}, nil, job)
	writeJSON(w, map[string]any{"jobId": job.ID, "taskId": m.ID, "recoverable": true, "operation": "search"})
}

func searchCheckpointSpecFor(m checkpoint.Manifest) (searchCheckpointSpec, error) {
	var spec searchCheckpointSpec
	if m.Definition.Kind != "query" || m.Definition.Driver != searchCheckpointDriver || len(m.Definition.Sources) != 1 || m.Definition.Semantics != searchCheckpointSemantics(m.Definition.Sources[0].Format) || decodeSavedConfig(m.Definition.Payload, &spec) != nil || spec.Limit != formats.CheckpointSearchHits || len(spec.Sheets) == 0 {
		return spec, checkpoint.ErrBinding
	}
	return spec, nil
}

func readSearchTask(lease *checkpoint.Lease, ctx context.Context, spec searchCheckpointSpec) (searchTaskCursor, []workbook.SearchResult, error) {
	m := lease.Snapshot()
	var cursor searchTaskCursor
	if decodeSavedConfig(m.Cursor, &cursor) != nil || cursor.Sheet < 0 || cursor.Sheet >= len(spec.Sheets) || cursor.Before < 0 || cursor.Count < cursor.Before || cursor.Count > formats.CheckpointSearchHits || cursor.ScannedBefore < 0 || cursor.Rows < 0 || cursor.Next < 0 || cursor.Next > cursor.Rows || m.Records != int64(cursor.Count) || m.DataBytes > formats.CheckpointSearchBytes {
		return cursor, nil, checkpoint.ErrCorrupt
	}
	terminal := cursor.SheetComplete && (cursor.Truncated || cursor.Count == spec.Limit || cursor.Sheet == len(spec.Sheets)-1)
	if m.Complete != terminal || cursor.Truncated && !cursor.SheetComplete || cursor.Index == "" && (cursor.Rows != 0 || cursor.Next != 0 || cursor.Before != cursor.Count || cursor.SheetComplete || m.Stage != "prepare") || cursor.Index != "" && (len(cursor.Index) != sha256.Size*2 || m.Stage != "scan" && m.Stage != "done") || m.Stage == "done" != m.Complete {
		return cursor, nil, checkpoint.ErrCorrupt
	}
	hits := make([]workbook.SearchResult, 0, cursor.Count)
	var consumed int64
	err := lease.Visit(ctx, func(block checkpoint.Block, source io.Reader) error {
		if block.Records <= 0 || block.Records > int64(cursor.Count-len(hits)) || block.Bytes > int64(formats.CheckpointSearchBytes)-consumed {
			return checkpoint.ErrCorrupt
		}
		data, err := io.ReadAll(io.LimitReader(source, block.Bytes+1))
		if err != nil {
			return err
		}
		var added []workbook.SearchResult
		if int64(len(data)) != block.Bytes || decodeSavedConfig(data, &added) != nil || int64(len(added)) != block.Records {
			return checkpoint.ErrCorrupt
		}
		consumed += int64(len(data))
		hits = append(hits, added...)
		return nil
	})
	if err != nil {
		return cursor, nil, err
	}
	if len(hits) != cursor.Count {
		return cursor, nil, checkpoint.ErrCorrupt
	}
	positions := make(map[string]int, len(spec.Sheets))
	for i, sheet := range spec.Sheets {
		if _, duplicate := positions[sheet.Name]; duplicate || sheet.Name == "" || sheet.Path == "" {
			return cursor, nil, checkpoint.ErrCorrupt
		}
		positions[sheet.Name] = i
	}
	previousSheet, previousRow, previousColumn := -1, 0, 0
	format, supported := formats.Lookup(m.Definition.Sources[0].Format)
	if !supported {
		return cursor, nil, checkpoint.ErrBinding
	}
	for i, hit := range hits {
		sheet, found := positions[hit.Sheet]
		col, row, err := workbook.ParseCellRef(hit.Ref)
		if !found || err != nil || row != hit.Row || col != hit.Col || row > format.MaxRows || col > format.MaxColumns || sheet < previousSheet || sheet > cursor.Sheet || (sheet < cursor.Sheet) != (i < cursor.Before) || sheet == previousSheet && (row < previousRow || row == previousRow && (spec.Mode == "advanced" || col <= previousColumn)) {
			return cursor, nil, checkpoint.ErrCorrupt
		}
		previousSheet, previousRow, previousColumn = sheet, row, col
	}
	return cursor, hits, nil
}

func (a *App) runCheckpointSearch(wb *workbook.Workbook, job *SearchJob) {
	err := a.scanCheckpointSearch(wb, job)
	job.mu.Lock()
	defer job.mu.Unlock()
	if err != nil {
		if job.ctx.Err() != nil || errors.Is(err, context.Canceled) {
			job.Status, job.Stage = "cancelled", "已取消"
		} else {
			job.Status, job.Error = "error", err.Error()
		}
		return
	}
	job.Status, job.Stage, job.Progress = "done", "搜索完成", 100
}

func (a *App) scanCheckpointSearch(wb *workbook.Workbook, job *SearchJob) error {
	lease := job.checkpoint.lease
	m := lease.Snapshot()
	spec, err := searchCheckpointSpecFor(m)
	if err != nil {
		return err
	}
	def, err := searchTaskDefinition(wb, searchRequest{Sheet: spec.Sheet, Query: spec.Query, Mode: spec.Mode, Scope: spec.Scope})
	if err != nil {
		return err
	}
	binding, err := checkpoint.DefinitionHash(def)
	if err != nil || binding != m.DefinitionHash {
		return checkpoint.ErrBinding
	}
	cursor, hits, err := readSearchTask(lease, job.ctx, spec)
	if err != nil {
		return err
	}
	update := func(next int, current []workbook.SearchResult) {
		job.mu.Lock()
		job.Matches = append(job.Matches[:0], hits[:cursor.Before]...)
		job.Matches = append(job.Matches, current...)
		job.Scanned = cursor.ScannedBefore + next
		job.Total = cursor.ScannedBefore + cursor.Rows
		job.SheetsDone, job.SheetsTotal = cursor.Sheet, len(spec.Sheets)
		fraction := float64(next) / float64(max(1, cursor.Rows))
		job.Progress = (float64(cursor.Sheet) + fraction) / float64(len(spec.Sheets)) * 100
		job.Stage = "搜索 · " + spec.Sheets[cursor.Sheet].Name
		job.mu.Unlock()
	}
	for {
		if err := job.ctx.Err(); err != nil {
			return err
		}
		meta := workbook.SheetMeta{Name: spec.Sheets[cursor.Sheet].Name, Path: spec.Sheets[cursor.Sheet].Path}
		idx := wb.GetCachedIndex(meta.Path)
		if idx == nil {
			if cursor.Index != "" {
				return errors.New("搜索恢复所需索引已被清理，请重新运行")
			}
			releaseIndex, admissionErr := a.backgroundIndex(job.ctx)
			if admissionErr != nil {
				return job.ctx.Err()
			}
			idx, err = wb.BuildIndex(meta, nil, job.ctx.Done())
			releaseIndex()
			if err != nil {
				return err
			}
		}
		stamp, err := wb.SearchSourceStamp(job.ctx, meta, idx)
		if err != nil {
			return err
		}
		if cursor.Index == "" {
			cursor.Index, cursor.Rows = stamp, idx.RowCount()
			data, _ := json.Marshal(cursor)
			m, err = advanceSearchCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: "scan", Cursor: data}, nil)
			if err != nil {
				return err
			}
		} else if cursor.Index != stamp || cursor.Rows != idx.RowCount() {
			return checkpoint.ErrBinding
		}
		update(cursor.Next, hits[cursor.Before:])
		state := workbook.SearchCheckpoint{Next: cursor.Next, Matches: hits[cursor.Before:], Complete: cursor.SheetComplete, Truncated: cursor.Truncated}
		save := func(saved workbook.SearchCheckpoint) error {
			next := cursor
			next.Next, next.SheetComplete, next.Truncated = saved.Next, saved.Complete, saved.Truncated
			next.Count = next.Before + len(saved.Matches)
			already := cursor.Count - cursor.Before
			if len(saved.Matches) < already || next.Count > spec.Limit {
				return checkpoint.ErrCorrupt
			}
			for i := 0; i < already; i++ {
				if saved.Matches[i] != hits[cursor.Before+i] {
					return checkpoint.ErrCorrupt
				}
			}
			terminal := saved.Complete && (saved.Truncated || next.Count == spec.Limit || next.Sheet == len(spec.Sheets)-1)
			stage := "scan"
			if saved.Complete {
				current, err := wb.SearchSourceStamp(job.ctx, meta, idx)
				if err != nil {
					return err
				}
				if current != stamp {
					return checkpoint.ErrBinding
				}
			}
			if terminal {
				stage = "done"
			}
			added := saved.Matches[already:]
			// Bound encoding before json.Marshal can allocate a second huge copy.
			var budget int64
			for _, hit := range added {
				budget += int64(len(hit.Sheet)+len(hit.Ref)+len(hit.Formula)+len(hit.Value))*6 + 256
				if budget > int64(formats.CheckpointSearchBytes)-m.DataBytes {
					return checkpoint.ErrCapacity
				}
			}
			var payload []byte
			if len(added) > 0 {
				payload, err = json.Marshal(added)
				if err != nil {
					return err
				}
			}
			data, _ := json.Marshal(next)
			committed, err := advanceSearchCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: stage, Cursor: data, Records: int64(len(added)), Bytes: int64(len(payload)), Complete: terminal}, payload)
			if err != nil {
				return err
			}
			hits = append(hits, added...)
			cursor, m = next, committed
			return nil
		}
		state, err = wb.SearchSheetResumable(job.ctx, idx, meta.Name, spec.Query, spec.Mode, spec.Limit-cursor.Before, state, save, func(next, _ int, current []workbook.SearchResult) { update(next, current) })
		if err != nil {
			return err
		}
		if !state.Complete {
			return checkpoint.ErrCorrupt
		}
		if m.Complete {
			// Completed task replay still revalidates its live source/index;
			// it never appends a second terminal checkpoint or rescans records.
			job.mu.Lock()
			job.Matches = append([]workbook.SearchResult{}, hits...)
			job.Truncated = state.Truncated || len(hits) == spec.Limit && cursor.Sheet < len(spec.Sheets)-1
			job.SheetsDone = cursor.Sheet + 1
			job.Scanned = cursor.ScannedBefore + cursor.Next
			if state.Truncated && cursor.Next < cursor.Rows {
				job.Scanned++ // the capped row was read, but is not a resumable boundary
			}
			job.mu.Unlock()
			return nil
		}
		cursor = searchTaskCursor{Sheet: cursor.Sheet + 1, Before: len(hits), Count: len(hits), ScannedBefore: cursor.ScannedBefore + cursor.Rows}
		data, _ := json.Marshal(cursor)
		m, err = advanceSearchCheckpoint(job.ctx, lease, checkpoint.Advance{Revision: m.Revision, Stage: "prepare", Cursor: data}, nil)
		if err != nil {
			return err
		}
	}
}
