package server

import (
	"bytes"
	"encoding/csv"

	"net/http"
	"strings"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/i18n"
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
		writeMessage(w, 400, "service.analysisResultsHttp.drillDownParameterInvalid")
		return
	}
	if req.Query.HeaderRow == 0 && !strings.HasPrefix(req.Query.Source, "append:") {
		req.Query.HeaderRow = 1
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-r.Context().Done():
		writeMessage(w, 408, "service.analysisResultsHttp.drillDownCanceled")
		return
	}
	rebuildFilter := req.JobID == "" && req.Query.SourceScope != nil && req.Query.SourceScope.Filter != nil
	release, err := a.acquireAnalysisQuerySlot(r.Context(), req.Query.Source, rebuildFilter)
	if err != nil {
		writeMessage(w, 408, "service.analysisResultsHttp.drillDownCanceled")
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
		writeFailure(w, 400, err)
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
		writeFailure(w, 400, err)
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
		writeMessage(w, 400, "service.analysisResultsHttp.parameterInvalid")
		return
	}
	job := a.getAnalysisJob(req.JobID)
	if job == nil || job.Workbook != wb {
		writeMessage(w, 404, "service.analysisResultsHttp.analysisTaskDoesNotExist")
		return
	}
	job.mu.RLock()
	if job.Status != "done" {
		job.mu.RUnlock()
		writeMessage(w, 409, "service.analysisResultsHttp.analysisNotCompletedYet")
		return
	}
	result := job.Result
	q := job.Query
	fieldNames := job.SourceFields
	job.mu.RUnlock()
	if !analysis.NameValid(req.Name) {
		writeMessage(w, 400, "service.analysisResultsHttp.invalidNewWorksheetName")
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
		writeMessage(w, 400, "service.analysisResultsHttp.numberResultColumnsExceedsEditing")
		return
	}
	if len(rows) > analysis.MaxSheetRows {
		writeMessage(w, 400, "service.analysisResultsHttp.numberVisibleRowsExceedsEditing")
		return
	}
	id := randomID()
	err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		if len(ws.Sheets) >= analysis.MaxSheets {
			return i18n.Error("service.analysisResultsHttp.editingTableQuantitiesExceedsLimit")
		}
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: id, Name: req.Name, Columns: cols, Rows: rows, Updated: time.Now().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		writeFailure(w, 400, err)
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
		writeMessage(w, 404, "service.analysisResultsHttp.analysisTaskDoesNotExist")
		return
	}
	job.mu.RLock()
	if job.Status != "done" {
		job.mu.RUnlock()
		writeMessage(w, 409, "service.analysisResultsHttp.analysisNotCompleted")
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
			writeFailure(w, 500, err)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens-Analysis.xlsx"`)
		_, _ = w.Write(buf.Bytes())
		return
	}
	if format != "" && format != "csv" {
		writeMessage(w, 400, "service.analysisResultsHttp.onlySupportsCsvOrXlsx")
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
		writeMessage(w, 400, "service.analysisResultsHttp.invalidExtractionConditions")
		return
	}
	if !analysis.NameValid(req.Name) {
		writeMessage(w, 400, "service.analysisResultsHttp.invalidWorksheetName")
		return
	}
	if req.Query.HeaderRow == 0 && !strings.HasPrefix(req.Query.Source, "append:") {
		req.Query.HeaderRow = 1
	}
	if req.Limit == 0 {
		req.Limit = 10000
	}
	if req.Limit < 1 || req.Limit > 10000 {
		writeMessage(w, 400, "service.analysisResultsHttp.extractMaximumRowsNarrowRange")
		return
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-r.Context().Done():
		writeMessage(w, 408, "service.analysisResultsHttp.detailExtractionCanceled")
		return
	}
	rebuildFilter := req.JobID == "" && req.Query.SourceScope != nil && req.Query.SourceScope.Filter != nil
	release, err := a.acquireAnalysisQuerySlot(r.Context(), req.Query.Source, rebuildFilter)
	if err != nil {
		writeMessage(w, 408, "service.analysisResultsHttp.detailExtractionCanceled")
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
		writeFailure(w, 400, err)
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
		writeFailure(w, 400, err)
		return
	}
	id := randomID()
	err = a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		if len(ws.Sheets) >= analysis.MaxSheets {
			return i18n.Error("service.analysisResultsHttp.editingTablesExceeds")
		}
		ws.Sheets = append(ws.Sheets, analysis.Sheet{ID: id, Name: req.Name, Columns: cols, Rows: rows, Updated: time.Now().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	writeJSON(w, map[string]any{"id": id, "rows": len(rows), "truncated": truncated})
}
