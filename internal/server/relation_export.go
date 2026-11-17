package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/reconcile"
)

func (a *App) handleRelationExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		JobID      string `json:"jobId"`
		Mode       string `json:"mode"`
		Maximum    int64  `json:"maximum,string"`
		ProtectCSV *bool  `json:"protectCSV,omitempty"`
		Format     string `json:"format,omitempty"`
	}
	if decodeRelationRequest(w, r, &req) != nil {
		writeError(w, 400, "关联导出参数无效")
		return
	}
	format, validFormat := formats.RelationExportFormat(req.Format)
	if !validFormat {
		writeError(w, 400, "关联导出格式无效")
		return
	}
	if req.Maximum == 0 {
		req.Maximum = formats.RelationDefaultRows
	}
	if req.Maximum < 1 || req.Maximum > formats.RelationOutputRows {
		writeError(w, 400, "关联提取上限超出容量")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	primary := a.currentWorkbook()
	snapshot, err := a.borrowRelationTask(req.JobID, primary)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	accepted := false
	defer func() {
		if !accepted {
			snapshot.close()
		}
	}()
	plan := snapshot.data.value.plan
	total, err := plan.Counts().Rows(req.Mode)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if total > req.Maximum {
		writeError(w, 413, fmt.Sprintf("关联结果共%d条，超过本次%d条提取上限", total, req.Maximum))
		return
	}
	job, err := a.newExportJob(primary, format)
	if err != nil {
		a.writeExportStartError(w, err)
		return
	}
	accepted = true
	go func() {
		defer close(job.done)
		defer snapshot.close()
		select {
		case a.exportSem <- struct{}{}:
			defer func() { <-a.exportSem }()
		case <-job.ctx.Done():
			job.fail(job.ctx.Err())
			return
		}
		select {
		case a.querySem <- struct{}{}:
			defer func() { <-a.querySem }()
		case <-job.ctx.Done():
			job.fail(job.ctx.Err())
			return
		}
		a.cacheMu.RLock()
		defer a.cacheMu.RUnlock()
		job.mu.Lock()
		job.Status, job.Stage, job.TotalRows = "running", "准备完整关联结果", total
		job.mu.Unlock()
		name := safeExportBase(primary.DisplayName) + "_关联结果_" + time.Now().Format("20060102_150405") + "_" + job.ID[:6] + ".csv"
		csvName := name
		if format == "zipcsv" {
			name += ".zip"
		}
		var rows int64
		writeCSV := func(file io.Writer) error {
			if err := primary.CheckSourceUnchanged(); err != nil {
				return err
			}
			if _, err := file.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
				return err
			}
			var protect func(string) string
			if req.ProtectCSV == nil || *req.ProtectCSV {
				protect = excelExport.ProtectCSVText
			}
			writer, err := reconcile.NewRelationCSVWriter(file, plan, protect)
			if err != nil {
				return err
			}
			_, err = plan.Stream(job.ctx, req.Mode, req.Maximum, func(row reconcile.RelationRow) error {
				if err := writer.Write(row); err != nil {
					return err
				}
				rows++
				if rows == 1 || rows&4095 == 0 || rows == total {
					job.mu.Lock()
					job.Stage, job.DoneRows = "写出完整关联结果", rows
					if total > 0 {
						job.Progress = float64(rows) / float64(total) * 100
					}
					job.mu.Unlock()
				}
				return nil
			})
			if err != nil {
				return err
			}
			if err := primary.CheckSourceUnchanged(); err != nil {
				return err
			}
			return writer.Flush()
		}
		err := publishExportFile(job, name, func(file *os.File) error {
			if format == "zipcsv" {
				return excelExport.WriteCSVArchive(job.ctx, file, csvName, writeCSV)
			}
			return writeCSV(file)
		})
		if err != nil {
			job.fail(err)
			return
		}
		job.mu.Lock()
		job.Status, job.Stage, job.Progress = "done", "完整关联结果已导出", 100
		job.DoneRows, job.TotalRows, job.Finished = rows, rows, time.Now()
		job.mu.Unlock()
	}()
	writeJSON(w, map[string]string{"jobId": job.ID})
}
