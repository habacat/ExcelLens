package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"excellens/internal/checkpoint"
	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

// Export jobs write to disk, in fixed-size row batches. The browser only sees
// lightweight progress/status JSON and a native file download link.
type ExportJob struct {
	mu         sync.RWMutex
	ID         string
	Ticket     string
	Status     string
	Stage      string
	Error      string
	FileName   string
	FilePath   string
	Format     string
	TaskID     string
	Progress   float64
	DoneRows   int64
	TotalRows  int64
	Size       int64
	Started    time.Time
	Finished   time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	wb         *workbook.Workbook
	checkpoint *taskCheckpointRun
}

type exportRequest struct {
	WorkbookKey       string `json:"workbookKey,omitempty"`
	Book              string `json:"book,omitempty"`
	Scope             string `json:"scope"` // selection, sheet, filtered, visible, all
	Sheet             string `json:"sheet"`
	Start             string `json:"start"`
	End               string `json:"end"`
	FilterID          string `json:"filterId"`
	ViewID            string `json:"viewId,omitempty"`
	VisualStart       int    `json:"visualStart,omitempty"`
	VisualEnd         int    `json:"visualEnd,omitempty"`
	ExcludeThroughRow int    `json:"excludeThroughRow,omitempty"`
	Format            string `json:"format"`
	Mode              string `json:"mode"`
	BOM               bool   `json:"bom"`
	ProtectCSV        bool   `json:"protectCSV"`
	Styles            bool   `json:"styles"`
	IncludeHidden     bool   `json:"includeHidden"`
	Recoverable       bool   `json:"recoverable,omitempty"`
}

func ExportDir() string { return filepath.Join(workbook.DataRoot(), "exports") }

func (a *App) handleExportStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req exportRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, 400, "导出参数无效")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "请先打开工作簿")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint || req.Book != "" && req.Book != sourceBookID(wb) {
		writeError(w, 409, "工作簿已切换，请重新选择导出范围")
		return
	}
	if req.Recoverable && (req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb)) {
		writeError(w, 409, "导出来源已切换，请重新选择范围")
		return
	}
	if req.Scope != "all" && req.Scope != "sheet" && req.Scope != "selection" && req.Scope != "filtered" && req.Scope != "visible" {
		writeError(w, 400, "请选择导出范围")
		return
	}
	if req.Scope != "all" {
		if _, ok := wb.FindSheet(req.Sheet); !ok {
			writeError(w, 404, "工作表不存在")
			return
		}
	}
	switch req.Format {
	case "csv", "tsv", "jsonl", "html", "xlsx", "zipcsv":
	default:
		writeError(w, 400, "不支持的导出格式")
		return
	}
	if req.Scope == "all" && req.Format != "zipcsv" && req.Format != "xlsx" {
		writeError(w, 400, "全部工作表请选择 XLSX 或 ZIP/CSV")
		return
	}
	if req.Scope == "filtered" && req.FilterID == "" {
		writeError(w, 400, "当前没有活动的筛选结果")
		return
	}
	if req.Scope == "visible" && req.ViewID == "" {
		writeError(w, 400, "当前可见行导出缺少视图快照")
		return
	}
	if req.ViewID != "" && req.Scope != "visible" && req.Scope != "selection" && req.Scope != "filtered" {
		writeError(w, 400, "可见行视图不适用于此导出范围")
		return
	}
	if req.Scope == "selection" {
		if _, _, err := workbook.ParseCellRef(req.Start); err != nil {
			writeError(w, 400, "选区起点无效")
			return
		}
		if _, _, err := workbook.ParseCellRef(req.End); err != nil {
			writeError(w, 400, "选区终点无效")
			return
		}
	}
	if req.Mode == "formula" && (req.Scope == "filtered" || req.Scope == "selection" || req.Scope == "visible") && req.Format == "xlsx" {
		writeError(w, 400, "筛选/选区导出会移动坐标，不能安全保留原公式引用，请改为缓存值")
		return
	}
	if req.Scope == "selection" && req.FilterID == "" && req.ViewID == "" && (req.VisualStart != 0 || req.VisualEnd != 0) {
		writeError(w, 400, "筛选选区缺少筛选结果标识")
		return
	}
	rows, err := a.captureExportRows(wb, req)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defer func() { rows.close() }()
	job, err := a.newExportJob(wb, req.Format)
	if err != nil {
		a.writeExportStartError(w, err)
		return
	}
	go a.runExport(req, rows, job)
	rows = nil // the export goroutine now owns the captured source and row stream
	writeJSON(w, map[string]any{"jobId": job.ID})
}

var errExportBusy = errors.New("已有两个导出任务正在运行，请先完成或取消其中一个")

// All generated and source exports share admission, cancellation, history,
// download tickets and disk publication; new features do not get extra slots.
func (a *App) newExportJob(wb *workbook.Workbook, format string) (*ExportJob, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wb != wb || a.maintenance {
		return nil, errors.New("工作簿已切换或正在维护缓存")
	}
	active := 0
	for _, j := range a.exportJobs {
		select {
		case <-j.done:
		default:
			active++
		}
	}
	if active >= 2 {
		return nil, errExportBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &ExportJob{ID: randomID(), Ticket: randomID() + randomID(), Status: "queued", Stage: "等待导出槽位", Started: time.Now(), ctx: ctx, cancel: cancel, done: make(chan struct{}), wb: wb, Format: format}
	a.exportJobs[job.ID] = job
	// Trim history in batches, keeping recent download tickets. A displayed
	// terminal status can precede deferred cleanup: only done confirms exit.
	// Files remain on disk until the user explicitly cleans them.
	if len(a.exportJobs) > 26 {
		for len(a.exportJobs) > 20 {
			var oldest *ExportJob
			for _, old := range a.exportJobs {
				select {
				case <-old.done:
					if oldest == nil || old.Started.Before(oldest.Started) || old.Started.Equal(oldest.Started) && old.ID < oldest.ID {
						oldest = old
					}
				default:
				}
			}
			if oldest == nil {
				break
			}
			oldest.cancel()
			delete(a.exportJobs, oldest.ID)
		}
	}
	return job, nil
}

func (a *App) writeExportStartError(w http.ResponseWriter, err error) {
	status := http.StatusConflict
	if errors.Is(err, errExportBusy) {
		status = http.StatusTooManyRequests
	}
	writeError(w, status, err.Error())
}

func (a *App) runExport(req exportRequest, rows *exportRowSource, job *ExportJob) {
	defer close(job.done)
	defer rows.close()
	fail := job.fail
	select {
	case a.exportSem <- struct{}{}:
		defer func() { <-a.exportSem }()
	case <-job.ctx.Done():
		fail(job.ctx.Err())
		return
	}
	if err := job.ctx.Err(); err != nil {
		fail(err)
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.mu.Unlock()
	a.cacheMu.RLock() // manual cache cleanup must not remove the active index during export
	defer a.cacheMu.RUnlock()
	selections, err := a.prepareExportSelections(job.ctx, job.wb, req, rows, func(stage string) {
		job.mu.Lock()
		job.Stage = stage
		job.mu.Unlock()
	})
	if err != nil {
		fail(err)
		return
	}
	opts := req.options()
	var total int64
	for _, s := range selections {
		total += s.RowCount()
	}
	job.mu.Lock()
	job.TotalRows = total
	job.Stage = "正在写入文件"
	job.mu.Unlock()
	if req.Recoverable {
		a.runNewCheckpointExport(req, selections, job)
		return
	}
	name := sourceExportName(workbookDisplayName(job.wb), req.Scope, req.Sheet, job.ID, req.Format)
	err = publishExportFile(job, name, func(tmp *os.File) error {
		return excelExport.Write(job.ctx, tmp, job.wb, selections, opts, func(done, total int64, stage string) {
			job.mu.Lock()
			job.DoneRows = done
			job.TotalRows = total
			job.Stage = stage
			if total > 0 {
				job.Progress = float64(done) / float64(total) * 100
			}
			job.mu.Unlock()
		})
	})
	if err != nil {
		fail(err)
		return
	}
	job.complete(total)
}

func sourceExportName(sourceName, scope, sheet, id, format string) string {
	ext := "." + format
	if format == "zipcsv" {
		ext = ".zip"
	}
	name := safeExportBase(sourceName)
	if scope == "all" {
		name += "_全部工作表"
	} else {
		name += "_" + safeExportBase(sheet)
	}
	return name + "_" + time.Now().Format("20060102_150405") + "_" + id[:6] + ext
}

func (job *ExportJob) complete(total int64) {
	job.mu.Lock()
	job.Status = "done"
	job.Stage = "导出完成"
	job.Progress = 100
	job.DoneRows = total
	job.Finished = time.Now()
	job.mu.Unlock()
}

func (job *ExportJob) fail(err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	job.Finished = time.Now()
	if errors.Is(err, context.Canceled) && !errors.Is(err, checkpoint.ErrUncertain) {
		job.Status, job.Stage = "cancelled", "已取消"
	} else {
		job.Status, job.Stage, job.Error = "failed", "导出失败", err.Error()
	}
}

func publishExportFile(job *ExportJob, name string, write func(*os.File) error) error {
	if err := os.MkdirAll(ExportDir(), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(ExportDir(), ".export-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = write(tmp)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = job.ctx.Err()
	}
	if err != nil {
		return err
	}
	path := filepath.Join(ExportDir(), name)
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	job.mu.Lock()
	job.FileName, job.FilePath, job.Size = name, path, info.Size()
	job.mu.Unlock()
	return nil
}

func safeExportBase(name string) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`\\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		return "ExcelLens"
	}
	rs := []rune(name)
	if len(rs) > 45 {
		name = string(rs[:45])
	}
	return name
}

func (a *App) handleExportStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	a.mu.RLock()
	job := a.exportJobs[r.URL.Query().Get("id")]
	a.mu.RUnlock()
	if job == nil {
		writeError(w, 404, "导出任务不存在")
		return
	}
	job.mu.RLock()
	resp := map[string]any{"id": job.ID, "status": job.Status, "stage": job.Stage, "error": job.Error, "progress": job.Progress, "doneRows": job.DoneRows, "totalRows": job.TotalRows, "size": job.Size, "fileName": job.FileName, "elapsedMs": time.Since(job.Started).Milliseconds()}
	if job.TaskID != "" {
		resp["taskId"], resp["recoverable"] = job.TaskID, true
	}
	if job.Status == "done" {
		resp["downloadUrl"] = "/api/export/download?id=" + url.QueryEscape(job.ID) + "&ticket=" + url.QueryEscape(job.Ticket)
		if id := r.Header.Get("X-ExcelLens-Session"); id != "" {
			resp["downloadUrl"] = resp["downloadUrl"].(string) + "&session=" + url.QueryEscape(id)
		}
	}
	job.mu.RUnlock()
	writeJSON(w, resp)
}

func (a *App) handleExportCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求无效")
		return
	}
	a.mu.RLock()
	job := a.exportJobs[req.ID]
	a.mu.RUnlock()
	if job == nil {
		writeError(w, 404, "导出任务不存在")
		return
	}
	job.cancel()
	writeJSON(w, map[string]any{"cancelled": true})
}

func (a *App) handleExportDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	id, ticket := r.URL.Query().Get("id"), r.URL.Query().Get("ticket")
	if id == "" || len(ticket) < 40 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	a.mu.RLock()
	job := a.exportJobs[id]
	a.mu.RUnlock()
	if job == nil {
		http.NotFound(w, r)
		return
	}
	job.mu.RLock()
	valid := job.Status == "done" && job.Ticket == ticket && job.FilePath != ""
	path, name := job.FilePath, job.FileName
	job.mu.RUnlock()
	if !valid {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-export`+filepath.Ext(name)+`"; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

func (a *App) handleExportFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	dir := ExportDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("explorer.exe", dir)
	} else if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", dir)
	} else {
		cmd = exec.Command("xdg-open", dir)
	}
	if err := cmd.Start(); err != nil {
		writeError(w, 500, "打开导出目录失败："+err.Error())
		return
	}
	go func() { _ = cmd.Wait() }()
	writeJSON(w, map[string]any{"opened": true, "path": dir})
}
