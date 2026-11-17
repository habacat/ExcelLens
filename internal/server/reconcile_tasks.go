package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

const maxReconcileResults = 4

type reconcileTask struct {
	mu                   sync.RWMutex
	ID                   string
	Workbook             *workbook.Workbook
	Refs                 [2]reconcileSourceRef
	Books                [2]*workbook.Workbook
	Sources              [2]reconcile.Source
	Query                reconcile.Query
	Result               reconcile.Result
	Status, Stage, Error string
	Done, Total          int64
	Created              time.Time
	ctx                  context.Context
	cancel               context.CancelFunc
	done                 chan struct{}
	release              func()
}

func (a *App) handleReconcileStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		WorkbookKey string `json:"workbookKey"`
		PrimaryBook string `json:"primaryBook"`
		Left, Right reconcileSourceRef
		Query       reconcile.Query `json:"query"`
	}
	if decodeAnalysis(r, &req) != nil || len(req.Query.Keys) < 1 || len(req.Query.Keys) > reconcile.MaxKeys || len(req.Query.Fields) > reconcile.MaxFields {
		writeError(w, 400, "核对参数无效，请选择主键及比较字段")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	if primary == nil || primary.Fingerprint != req.WorkbookKey || sourceBookID(primary) != req.PrimaryBook {
		writeError(w, 409, "当前工作簿已切换，请重新选择来源")
		return
	}
	leftBook, rightBook := a.resolveReconcileBook(req.Left.Book), a.resolveReconcileBook(req.Right.Book)
	if leftBook == nil || rightBook == nil {
		writeError(w, 409, "核对来源已关闭或移除，请重新添加")
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != primary || a.maintenance {
		writeError(w, 409, "工作簿已切换或正在维护缓存")
		return
	}
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	for _, old := range a.reconcileJobs {
		old.mu.RLock()
		active := (old.Status == "queued" || old.Status == "running") && old.ctx.Err() == nil
		old.mu.RUnlock()
		if active {
			writeError(w, 429, "已有核对任务，请先完成或取消")
			return
		}
	}
	for len(a.reconcileJobs) >= maxReconcileResults {
		var oldest *reconcileTask
		for _, old := range a.reconcileJobs {
			select {
			case <-old.done:
			default:
				continue
			}
			if oldest == nil || old.Created.Before(oldest.Created) {
				oldest = old
			}
		}
		if oldest == nil {
			writeError(w, 429, "正在回收已取消的核对任务，请稍后重试")
			return
		}
		delete(a.reconcileJobs, oldest.ID)
		oldest.closeSources()
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &reconcileTask{ID: randomID(), Workbook: primary, Books: [2]*workbook.Workbook{leftBook, rightBook}, Refs: [2]reconcileSourceRef{req.Left, req.Right}, Query: req.Query, Status: "queued", Stage: "等待核对任务", Created: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), release: a.pinWorkbooks(leftBook, rightBook)}
	a.reconcileJobs[job.ID] = job
	go a.runReconcile(job)
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (a *App) runReconcile(job *reconcileTask) {
	defer close(job.done)
	defer func() {
		job.mu.RLock()
		retained := job.Status == "done"
		job.mu.RUnlock()
		if !retained {
			job.closeSources()
		}
	}()
	select {
	case a.querySem <- struct{}{}:
		defer func() { <-a.querySem }()
	case <-job.ctx.Done():
		job.finishError(job.ctx.Err())
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	job.mu.Lock()
	job.Status, job.Stage = "running", "准备来源快照"
	job.mu.Unlock()
	var sources [2]reconcile.Source
	for i := range sources {
		var err error
		sources[i], err = a.buildReconcileSource(job.ctx, job.Books[i], job.Refs[i], reconcile.NeededColumns(job.Query, i == 1))
		if err != nil {
			job.finishError(err)
			return
		}
	}
	result, err := reconcile.Run(job.ctx, filepath.Join(job.Workbook.CacheDir, "queries"), job.Query, sources[0], sources[1], reconcile.Sinks{}, func(stage string, done, total int64) {
		job.mu.Lock()
		job.Stage, job.Done, job.Total = stage, done, total
		job.mu.Unlock()
	})
	if err != nil {
		job.finishError(err)
		return
	}
	job.mu.Lock()
	if err := job.ctx.Err(); err != nil {
		job.mu.Unlock()
		job.finishError(err)
		return
	}
	job.Result, job.Sources, job.Status, job.Stage = result, sources, "done", "核对完成"
	job.mu.Unlock()
}

// Successful history owns its source snapshots for navigation and exports.
// Failed/cancelled history needs only its status. Detach under the task lock
// and reclaim outside it; an already borrowed export owns independent pins.
func (job *reconcileTask) closeSources() {
	job.mu.Lock()
	release := job.release
	job.release = nil
	job.Books, job.Sources = [2]*workbook.Workbook{}, [2]reconcile.Source{}
	job.mu.Unlock()
	if release != nil {
		release()
	}
}

func (job *reconcileTask) finishError(err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Status, job.Stage, job.Error = "error", "核对失败", err.Error()
	if errors.Is(err, context.Canceled) {
		job.Status, job.Stage, job.Error = "cancelled", "核对已取消", ""
	}
}

func (a *App) getReconcileTask(id string) *reconcileTask {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	return a.reconcileJobs[id]
}

type reconcileSnapshot struct {
	book    *workbook.Workbook
	books   [2]*workbook.Workbook
	sources [2]reconcile.Source
	refs    [2]reconcileSourceRef
	query   reconcile.Query
	counts  reconcile.Counts
}

// Caller holds the shared cache read lock. Retain source pins under the task
// map lock so pruning cannot release the last owner between lookup and export.
func (a *App) snapshotReconcile(id string) (reconcileSnapshot, func(), error) {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	job := a.reconcileJobs[id]
	if job == nil {
		return reconcileSnapshot{}, nil, errors.New("核对结果已过期，请重新运行")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != "done" {
		return reconcileSnapshot{}, nil, errors.New("核对尚未完成")
	}
	snapshot := reconcileSnapshot{book: job.Workbook, books: job.Books, sources: job.Sources, refs: job.Refs, query: job.Query, counts: job.Result.Counts}
	return snapshot, a.pinWorkbooks(job.Books[0], job.Books[1]), nil
}

func (a *App) handleReconcileStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getReconcileTask(r.URL.Query().Get("id"))
	if job == nil {
		writeError(w, 404, "核对任务已过期，请重新运行")
		return
	}
	job.mu.RLock()
	out := map[string]any{"jobId": job.ID, "status": job.Status, "stage": job.Stage, "error": job.Error, "done": job.Done, "total": job.Total, "elapsedMs": time.Since(job.Created).Milliseconds()}
	if job.Status == "done" {
		out["result"] = job.Result
	}
	job.mu.RUnlock()
	writeJSON(w, out)
}

func (a *App) handleReconcileCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeError(w, 400, "核对任务参数无效")
		return
	}
	if job := a.getReconcileTask(req.ID); job != nil {
		job.cancel()
	}
	writeJSON(w, map[string]bool{"cancelled": true})
}

func (a *App) handleReconcileClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeError(w, 400, "核对结果参数无效")
		return
	}
	a.reconcileMu.Lock()
	job := a.reconcileJobs[req.ID]
	if job != nil {
		select {
		case <-job.done:
		default:
			a.reconcileMu.Unlock()
			writeError(w, 409, "请先取消或完成核对任务")
			return
		}
		delete(a.reconcileJobs, req.ID)
	}
	a.reconcileMu.Unlock()
	if job != nil {
		job.closeSources()
	}
	writeJSON(w, map[string]bool{"cleared": job != nil})
}

func (a *App) cancelReconciliations() {
	a.reconcileMu.Lock()
	defer a.reconcileMu.Unlock()
	for _, job := range a.reconcileJobs {
		job.cancel()
	}
}

func (a *App) clearReconciliations() {
	a.reconcileMu.Lock()
	jobs, books := a.reconcileJobs, a.reconcileBooks
	for _, job := range jobs {
		job.cancel()
	}
	a.reconcileJobs, a.reconcileBooks = make(map[string]*reconcileTask), make(map[string]*reconcileAttachment)
	a.reconcileMu.Unlock()
	for _, job := range jobs {
		<-job.done
		job.closeSources()
	}
	for _, book := range books {
		book.release()
	}
}

func (a *App) handleReconcileNavigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID    string `json:"jobId"`
		Side     string `json:"side"`
		Row, Col int
	}
	if decodeAnalysis(r, &req) != nil || req.Side != "left" && req.Side != "right" {
		writeError(w, 400, "核对定位参数无效")
		return
	}
	index := 0
	if req.Side == "right" {
		index = 1
	}
	a.cacheMu.RLock()
	locked := true
	defer func() {
		if locked {
			a.cacheMu.RUnlock()
		}
	}()
	snapshot, release, err := a.snapshotReconcile(req.JobID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer release()
	if a.currentWorkbook() != snapshot.book {
		writeError(w, 409, "工作簿已切换，请重新运行核对")
		return
	}
	book, source, ref := snapshot.books[index], snapshot.sources[index], snapshot.refs[index]
	validCol := false
	for _, field := range source.Info.Fields {
		if field.Column == req.Col {
			validCol = true
			break
		}
	}
	if req.Row < 1 || !validCol {
		writeError(w, 400, "定位源坐标无效")
		return
	}
	if source.Info.Kind == "analysis" {
		current, err := a.reconcileScratchSource(book, reconcileSourceRef{Book: ref.Book, Source: ref.Source, Stamp: source.Info.Stamp}, nil)
		if err != nil || req.Row > current.Info.Total {
			writeError(w, 409, "分析编辑表已修改或移除，请先核对原结果快照")
			return
		}
	} else {
		if err := book.CheckSourceUnchanged(); err != nil {
			writeError(w, 409, "来源文件已改变或不可访问，不能定位到原记录；原结果和CSV仍保留核对快照："+err.Error())
			return
		}
		meta, _ := book.FindSheet(source.Info.Sheet)
		idx := book.GetCachedIndex(meta.Path)
		if idx == nil {
			writeError(w, 409, "来源索引已失效")
			return
		}
		at := idx.FindRowOffset(req.Row)
		if at >= idx.RowCount() || idx.RowAt(at).Number != req.Row {
			writeError(w, 400, "来源行不存在")
			return
		}
	}
	a.cacheMu.RUnlock()
	locked = false
	target, err := a.sourceNavigation(book, source.Info.Kind, ref.Source, source.Info.Sheet, req.Row, req.Col)
	if err != nil {
		writeError(w, 429, err.Error())
		return
	}
	writeJSON(w, target)
}
