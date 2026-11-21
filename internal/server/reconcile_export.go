package server

import (
	"errors"
	"io"
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
		Format     string `json:"format"`
		Status     string `json:"status"`
	}
	if decodeAnalysis(r, &req) != nil || (req.Format != "" && req.Format != "csv" && req.Format != "zipcsv") || !validReconcileDetailStatus(req.Status) || req.Status != "" && req.Format != "zipcsv" {
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
	book := snapshot.book
	ext := "csv"
	if req.Format == "zipcsv" {
		ext = "zip"
		if snapshot.record == nil || req.Status == "equal" && !snapshot.query.IncludeEqual {
			release()
			reason := snapshot.recordError
			if reason == "" {
				reason = "该结果没有可用的核查记录，或本次未包含一致记录"
			}
			writeError(w, 409, reason)
			return
		}
	}
	exportJob, err := a.newExportJob(book, ext)
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
		releaseScan, admissionErr := a.backgroundQuery(exportJob.ctx)
		if admissionErr != nil {
			exportJob.fail(exportJob.ctx.Err())
			return
		}
		defer releaseScan()
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		exportJob.mu.Lock()
		exportJob.Status, exportJob.Stage = "running", "导出完整核对清单"
		exportJob.mu.Unlock()
		name := safeExportBase(book.DisplayName) + "_双表核对_" + time.Now().Format("20060102_150405") + "_" + exportJob.ID[:6] + "." + ext
		var rows int64
		writeCSV := func(dst io.Writer) (int64, error) {
			count, err := writeReconcileCSV(exportJob, dst, snapshot, req.Status, req.ProtectCSV)
			rows = count
			return count, err
		}
		err := publishExportFile(exportJob, name, func(file *os.File) error {
			if ext == "zip" {
				return snapshot.record.writeBundle(file, req.Status, req.ProtectCSV, writeCSV)
			}
			_, err := writeCSV(file)
			return err
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

func validReconcileDetailStatus(status string) bool {
	switch status {
	case "", "equal", "within_tolerance", "different", "left_only", "right_only", "duplicate", "review", "invalid_key":
		return true
	}
	return false
}

func writeReconcileCSV(job *ExportJob, dst io.Writer, snapshot reconcileSnapshot, status string, protectText bool) (int64, error) {
	if _, err := dst.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
		return 0, err
	}
	var protect func(string) string
	if protectText {
		protect = excelExport.ProtectCSVText
	}
	sources := snapshot.sources
	writer, err := reconcile.NewCSVWriter(dst, snapshot.query, sources[0].Info, sources[1].Info, protect)
	if err != nil {
		return 0, err
	}
	sinks := writer.Sinks()
	if status != "" {
		writeGroup := sinks.Group
		sinks.Group = func(group reconcile.Group) error {
			if group.Status != status {
				return nil
			}
			return writeGroup(group)
		}
		if status != "duplicate" {
			sinks.Duplicate = nil
		}
	}
	result, err := reconcile.Run(job.ctx, filepath.Join(snapshot.book.CacheDir, "queries"), snapshot.query, sources[0], sources[1], sinks, func(stage string, done, total int64) {
		job.mu.Lock()
		job.Stage, job.DoneRows, job.TotalRows = stage, done, total
		if total > 0 {
			job.Progress = float64(done) / float64(total) * 100
		}
		job.mu.Unlock()
	})
	if err != nil {
		return 0, err
	}
	if result.Counts != snapshot.counts {
		return 0, errors.New("核对来源或结果数量与快照不一致，请重新核对")
	}
	return writer.Rows, writer.Flush()
}
