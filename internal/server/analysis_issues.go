package server

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"excellens/internal/analysis"
	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

var errAnalysisSourceChanged = errors.New("分析来源已修改，请重新运行分析后提取异常明细")

type issueSnapshot struct {
	query analysis.Query
	stamp string
	count int64
	rows  *analysisSourceSnapshot
}

func (s issueSnapshot) close() { s.rows.close() }

func (a *App) handleAnalysisIssuesExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		JobID      string `json:"jobId"`
		ProtectCSV bool   `json:"protectCSV"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeError(w, 400, "异常清单导出参数无效")
		return
	}
	snapshot, err := a.issueSnapshot(wb, req.JobID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	job, err := a.newExportJob(wb, "csv")
	if err != nil {
		snapshot.close()
		a.writeExportStartError(w, err)
		return
	}
	go a.runIssuesExport(job, snapshot, req.ProtectCSV)
	writeJSON(w, map[string]any{"jobId": job.ID})
}

func (a *App) runIssuesExport(job *ExportJob, snapshot issueSnapshot, protect bool) {
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
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-job.ctx.Done():
		job.fail(job.ctx.Err())
		return
	}
	release, err := a.acquireCompositionAnalysisSlot(job.ctx, snapshot.query.Source)
	if err != nil {
		job.fail(err)
		return
	}
	defer release()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	src, err := a.sourceForIssues(job.ctx, job.wb, snapshot)
	if err != nil {
		job.fail(err)
		return
	}
	defer src.close()
	job.mu.Lock()
	job.Status, job.Stage, job.TotalRows = "running", "正在导出完整异常清单", snapshot.count
	job.mu.Unlock()
	name := safeExportBase(job.wb.DisplayName) + "_异常清单_" + time.Now().Format("20060102_150405") + "_" + job.ID[:6] + ".csv"
	var written int64
	err = publishExportFile(job, name, func(file *os.File) error {
		if _, err := file.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
			return err
		}
		out := csv.NewWriter(file)
		columns := issueOutputColumnsForQuery(snapshot.query)
		if protect {
			for _, i := range []int{9, 10, 11} {
				columns[i] += "（文本保护）"
			}
		}
		if err := out.Write(columns); err != nil {
			return err
		}
		provenance := issueProvenance(job.wb, snapshot)
		_, err := analysis.VisitIssues(job.ctx, snapshot.query, src.fields, src.scan, 0, func(issue analysis.Issue) error {
			row := analysis.IssueCellsForQuery(snapshot.query, issue)
			if snapshot.query.Composition == nil {
				row = append(append([]string(nil), provenance...), row...)
			}
			if protect {
				for i, text := range row {
					row[i] = excelExport.ProtectCSVText(text)
				}
			}
			if err := out.Write(row); err != nil {
				return err
			}
			written++
			if written%256 == 0 {
				job.mu.Lock()
				job.DoneRows = written
				if snapshot.count > 0 {
					job.Progress = float64(written) / float64(snapshot.count) * 100
				}
				job.mu.Unlock()
			}
			return nil
		})
		if err != nil {
			return err
		}
		if written != snapshot.count {
			return fmt.Errorf("异常项目数量与分析结果不一致（%d / %d），请重新分析", written, snapshot.count)
		}
		out.Flush()
		return out.Error()
	})
	if err != nil {
		job.fail(err)
		return
	}
	job.mu.Lock()
	job.Status, job.Stage, job.Progress = "done", "完整异常清单导出完成", 100
	job.DoneRows, job.Finished = written, time.Now()
	job.mu.Unlock()
}

func (a *App) issueSnapshot(wb *workbook.Workbook, id string) (issueSnapshot, error) {
	job := a.getAnalysisJob(id)
	if job == nil || job.Workbook != wb {
		return issueSnapshot{}, errors.New("分析任务已过期，请重新运行分析")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != "done" || job.SourceStamp == "" {
		return issueSnapshot{}, errors.New("分析尚未完成")
	}
	var rows *analysisSourceSnapshot
	if job.Query.SourceScope != nil || job.Query.Composition != nil {
		var err error
		rows, err = job.SourceRows.retain()
		if err != nil {
			return issueSnapshot{}, err
		}
	}
	return issueSnapshot{query: job.Query, stamp: job.SourceStamp, rows: rows,
		count: job.Result.InvalidNumbers + job.Result.InvalidConversions + job.Result.MissingFormulaCaches}, nil
}

func (a *App) sourceForIssues(ctx context.Context, wb *workbook.Workbook, snapshot issueSnapshot) (analysisSource, error) {
	src, err := a.sourceQuery(ctx, wb, snapshot.query, nil, snapshot.rows)
	if err == nil && src.stamp() != snapshot.stamp {
		src.close()
		err = errAnalysisSourceChanged
	}
	return src, err
}

func issueProvenance(wb *workbook.Workbook, snapshot issueSnapshot) []string {
	return []string{wb.DisplayName, wb.Fingerprint, strings.TrimPrefix(strings.TrimPrefix(snapshot.query.Source, "wb:"), "scratch:"), snapshot.stamp}
}

func issueOutputColumns() []string {
	return append([]string{"来源文件", "源指纹", "来源表", "源版本"}, analysis.IssueColumns()...)
}

func issueOutputColumnsForQuery(q analysis.Query) []string {
	if q.Composition != nil {
		return analysis.IssueColumnsForQuery(q)
	}
	return issueOutputColumns()
}

func (a *App) handleAnalysisIssuesMaterialize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		JobID string `json:"jobId"`
		Name  string `json:"name"`
		Limit int    `json:"limit"`
	}
	if decodeAnalysis(r, &req) != nil || !analysis.NameValid(req.Name) || req.Limit < 1 || req.Limit > analysis.MaxSheetRows {
		writeError(w, 400, "异常明细提取参数无效（最多10,000项）")
		return
	}
	snapshot, err := a.issueSnapshot(wb, req.JobID)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	defer snapshot.close()
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-r.Context().Done():
		writeError(w, 408, "异常明细提取已取消")
		return
	}
	release, err := a.acquireCompositionAnalysisSlot(r.Context(), snapshot.query.Source)
	if err != nil {
		writeError(w, 408, "异常明细提取已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	src, err := a.sourceForIssues(r.Context(), wb, snapshot)
	var rows [][]string
	var truncated bool
	if err == nil {
		rows, truncated, err = analysis.CollectIssues(r.Context(), snapshot.query, src.fields, src.scan, req.Limit)
	}
	src.close()
	a.cacheMu.RUnlock()
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	provenance := issueProvenance(wb, snapshot)
	if snapshot.query.Composition == nil {
		for i, row := range rows {
			rows[i] = append(append([]string(nil), provenance...), row...)
		}
	}
	id := randomID()
	err = a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		if len(ws.Sheets) >= analysis.MaxSheets {
			return errors.New("编辑表数量超过40")
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: id, Name: req.Name, Columns: issueOutputColumnsForQuery(snapshot.query), Rows: rows, Updated: time.Now().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "rows": len(rows), "total": snapshot.count, "truncated": truncated})
}
