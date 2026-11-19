package server

import (
	"bytes"
	"encoding/csv"
	"errors"
	"net/http"
	"strings"
	"time"

	"excellens/internal/analysis"
)

func (a *App) handleAnalysisDrill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		Query          analysis.Query `json:"query"`
		JobID          string         `json:"jobId,omitempty"`
		Keys           []string       `json:"keys"`
		ColumnKey      string         `json:"columnKey"`
		ColumnSelected *bool          `json:"columnSelected,omitempty"`
		Offset         int            `json:"offset"`
		Limit          int            `json:"limit"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "下钻参数无效")
		return
	}
	if req.Query.HeaderRow == 0 && !strings.HasPrefix(req.Query.Source, "append:") {
		req.Query.HeaderRow = 1
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-r.Context().Done():
		writeError(w, 408, "下钻已取消")
		return
	}
	rebuildFilter := req.JobID == "" && req.Query.SourceScope != nil && req.Query.SourceScope.Filter != nil
	release, err := a.acquireAnalysisQuerySlot(r.Context(), req.Query.Source, rebuildFilter)
	if err != nil {
		writeError(w, 408, "下钻已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	src, err := a.sourceResult(r.Context(), wb, req.Query, req.JobID)
	if rebuildFilter {
		release()
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defer src.close()
	// Validate with the full source schema; the drill engine itself limits
	// the returned display fields to 40 without losing dimension predicates.
	selected := len(req.Query.Columns) > 0
	if req.ColumnSelected != nil {
		selected = *req.ColumnSelected
	}
	var column *string
	if selected {
		column = &req.ColumnKey
	}
	out, err := analysis.DrillScope(r.Context(), req.Query, src.fields, req.Keys, column, req.Offset, clamp(req.Limit, 1, 100), src.scan)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, out)
}
func (a *App) handleAnalysisMaterialize(w http.ResponseWriter, r *http.Request) {
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
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	job := a.getAnalysisJob(req.JobID)
	if job == nil || job.Workbook != wb {
		writeError(w, 404, "分析任务不存在")
		return
	}
	job.mu.RLock()
	if job.Status != "done" {
		job.mu.RUnlock()
		writeError(w, 409, "分析尚未完成")
		return
	}
	result := job.Result
	q := job.Query
	fieldNames := job.SourceFields
	job.mu.RUnlock()
	if !analysis.NameValid(req.Name) {
		writeError(w, 400, "新工作表名称无效")
		return
	}
	// A single cross-tab representation is shared by screen, XLSX export and
	// writable result materialization. No multi-metric values may disappear.
	if fieldNames == nil {
		a.cacheMu.RLock()
		src, sourceErr := a.sourceResult(r.Context(), wb, q, req.JobID)
		a.cacheMu.RUnlock()
		if sourceErr == nil {
			fieldNames = src.fields
		}
		src.close()
	}
	grid := analysis.BuildPivotTable(q, result, fieldNames)
	cols, rows := grid.Headers, grid.Rows
	if len(cols) > analysis.MaxSheetCols {
		writeError(w, 400, "结果列数超过编辑表限制")
		return
	}
	if len(rows) > analysis.MaxSheetRows {
		writeError(w, 400, "透视结果超过编辑表行数限制")
		return
	}
	id := randomID()
	err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		if len(ws.Sheets) >= analysis.MaxSheets {
			return errors.New("编辑表数量超过限制")
		}
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: id, Name: req.Name, Columns: cols, Rows: rows, Updated: time.Now().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]string{"id": id})
}
func safeCSVString(s string) string {
	// Prevent spreadsheet formula injection when a CSV is later opened in Excel.
	clean := strings.TrimLeft(s, " \t\r\n")
	if clean != "" {
		switch clean[0] {
		case '=', '+', '@':
			return "'" + s
		case '-':
			if _, ok := analysis.Decimal(clean); !ok {
				return "'" + s
			}
		}
	}
	return s
}
func (a *App) handleAnalysisExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getAnalysisJob(r.URL.Query().Get("id"))
	if job == nil || job.Workbook != a.currentWorkbook() {
		writeError(w, 404, "分析任务不存在")
		return
	}
	job.mu.RLock()
	if job.Status != "done" {
		job.mu.RUnlock()
		writeError(w, 409, "分析未完成")
		return
	}
	result := job.Result
	q := job.Query
	fields := job.SourceFields
	job.mu.RUnlock()
	if fields == nil {
		a.cacheMu.RLock()
		source, err := a.sourceResult(r.Context(), job.Workbook, q, r.URL.Query().Get("id"))
		a.cacheMu.RUnlock()
		if err == nil {
			fields = source.fields
		}
		source.close()
	}
	grid := analysis.BuildPivotTable(q, result, fields)
	format := r.URL.Query().Get("format")
	if format == "xlsx" {
		// Buffer before sending headers, so a failed ZIP serialization never
		// appears to the user as a seemingly valid, truncated Excel file.
		var buf bytes.Buffer
		if err := analysis.ExportPivotXLSX(&buf, grid); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-Analysis.xlsx"`)
		_, _ = w.Write(buf.Bytes())
		return
	}
	if format != "" && format != "csv" {
		writeError(w, 400, "仅支持CSV或XLSX")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-Analysis.csv"`)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(w)
	_ = writer.Write(grid.Headers)
	for _, line := range grid.Rows {
		escaped := make([]string, len(line))
		for i, v := range line {
			escaped[i] = safeCSVString(v)
		}
		_ = writer.Write(escaped)
	}
	writer.Flush()
}

// MaterializeDetail applies the same filter and calculated-column pipeline as
// the pivot engine. This is a *derived snapshot* and never writes to the
// originating Excel file. One bounded scan replaces 50+ pages of rescans.
func (a *App) handleAnalysisMaterializeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		Name           string         `json:"name"`
		Query          analysis.Query `json:"query"`
		JobID          string         `json:"jobId,omitempty"`
		Keys           []string       `json:"keys"`
		ColumnKey      string         `json:"columnKey"`
		ColumnSelected *bool          `json:"columnSelected,omitempty"`
		Limit          int            `json:"limit"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "明细提取条件无效")
		return
	}
	if !analysis.NameValid(req.Name) {
		writeError(w, 400, "工作表名称无效")
		return
	}
	if req.Query.HeaderRow == 0 && !strings.HasPrefix(req.Query.Source, "append:") {
		req.Query.HeaderRow = 1
	}
	if req.Limit == 0 {
		req.Limit = 10000
	}
	if req.Limit < 1 || req.Limit > 10000 {
		writeError(w, 400, "最多提取10,000行，请缩小范围")
		return
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-r.Context().Done():
		writeError(w, 408, "明细提取已取消")
		return
	}
	rebuildFilter := req.JobID == "" && req.Query.SourceScope != nil && req.Query.SourceScope.Filter != nil
	release, err := a.acquireAnalysisQuerySlot(r.Context(), req.Query.Source, rebuildFilter)
	if err != nil {
		writeError(w, 408, "明细提取已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	src, err := a.sourceResult(r.Context(), wb, req.Query, req.JobID)
	if rebuildFilter {
		release()
	}
	if err != nil {
		a.cacheMu.RUnlock()
		writeError(w, 400, err.Error())
		return
	}
	selected := req.Keys != nil && len(req.Query.Columns) > 0
	if req.ColumnSelected != nil {
		selected = *req.ColumnSelected
	}
	var column *string
	if selected {
		column = &req.ColumnKey
	}
	cols, rows, truncated, err := analysis.CollectDetailsScope(r.Context(), req.Query, src.fields, req.Keys, column, req.Limit, src.scan)
	src.close()
	a.cacheMu.RUnlock()
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	id := randomID()
	err = a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		if len(ws.Sheets) >= analysis.MaxSheets {
			return errors.New("编辑表数量超过40")
		}
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: id, Name: req.Name, Columns: cols, Rows: rows, Updated: time.Now().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": id, "rows": len(rows), "truncated": truncated})
}
