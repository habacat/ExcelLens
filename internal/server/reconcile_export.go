package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/reconcile"
)

func (a *App) handleReconcileExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID      string `json:"jobId"`
		ProtectCSV bool   `json:"protectCSV"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeError(w, 400, "核对导出参数无效")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	snapshot, release, err := a.snapshotReconcile(req.JobID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if a.currentWorkbook() != snapshot.book {
		release()
		writeError(w, 409, "工作簿已切换")
		return
	}
	sources, query, expected, book := snapshot.sources, snapshot.query, snapshot.counts, snapshot.book
	exportJob, err := a.newExportJob(book, "csv")
	if err != nil {
		release()
		a.writeExportStartError(w, err)
		return
	}
	go func() {
		defer close(exportJob.done)
		defer release()
		select {
		case a.exportSem <- struct{}{}:
			defer func() { <-a.exportSem }()
		case <-exportJob.ctx.Done():
			exportJob.fail(exportJob.ctx.Err())
			return
		}
		select {
		case a.querySem <- struct{}{}:
			defer func() { <-a.querySem }()
		case <-exportJob.ctx.Done():
			exportJob.fail(exportJob.ctx.Err())
			return
		}
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		exportJob.mu.Lock()
		exportJob.Status, exportJob.Stage = "running", "导出完整核对清单"
		exportJob.mu.Unlock()
		name := safeExportBase(book.DisplayName) + "_双表核对_" + time.Now().Format("20060102_150405") + "_" + exportJob.ID[:6] + ".csv"
		var rows int64
		err := publishExportFile(exportJob, name, func(file *os.File) error {
			if _, err := file.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
				return err
			}
			var protect func(string) string
			if req.ProtectCSV {
				protect = excelExport.ProtectCSVText
			}
			writer, err := reconcile.NewCSVWriter(file, query, sources[0].Info, sources[1].Info, protect)
			if err != nil {
				return err
			}
			result, err := reconcile.Run(exportJob.ctx, filepath.Join(book.CacheDir, "queries"), query, sources[0], sources[1], writer.Sinks(), func(stage string, done, total int64) {
				exportJob.mu.Lock()
				exportJob.Stage, exportJob.DoneRows, exportJob.TotalRows = stage, done, total
				if total > 0 {
					exportJob.Progress = float64(done) / float64(total) * 100
				}
				exportJob.mu.Unlock()
			})
			if err != nil {
				return err
			}
			if result.Counts != expected {
				return errors.New("核对来源或结果数量与快照不一致，请重新核对")
			}
			rows = writer.Rows
			return writer.Flush()
		})
		if err != nil {
			exportJob.fail(err)
			return
		}
		exportJob.mu.Lock()
		exportJob.Status, exportJob.Stage, exportJob.Progress = "done", "完整核对清单已导出", 100
		exportJob.DoneRows, exportJob.TotalRows, exportJob.Finished = rows, rows, time.Now()
		exportJob.mu.Unlock()
	}()
	writeJSON(w, map[string]string{"jobId": exportJob.ID})
}
