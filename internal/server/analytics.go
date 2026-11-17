package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type analysisTask struct {
	mu           sync.RWMutex
	ID           string
	Workbook     *workbook.Workbook
	Query        analysis.Query
	SourceStamp  string
	SourceFields []analysis.Field
	RecipePlan   *recipeRunPlan
	Status       string
	Stage        string
	Progress     int64
	Total        int64
	Result       analysis.Result
	Error        string
	Created      time.Time
	cancel       context.CancelFunc
	ctx          context.Context
	done         chan struct{}
	release      func()
	SourceRows   *analysisSourceSnapshot
}

func (a *App) analysisPath(wb *workbook.Workbook) string {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		root = workbook.DataRoot()
	}
	return analysis.WorkspacePath(filepath.Join(root, "ExcelLens"), wb.Fingerprint)
}

// A lock per workbook fingerprint makes edits from two browser tabs atomic,
// without serializing analysis writes of unrelated workbooks. Always acquire
// App.analysisMu *before* the workspace lock: read endpoints already hold it,
// so taking the locks in reverse order can deadlock under concurrent requests.
func (a *App) loadAnalysis(wb *workbook.Workbook) (analysis.Workspace, error) {
	path := a.analysisPath(wb)
	lock := persistentFileLock(path)
	lock.RLock()
	defer lock.RUnlock()
	return analysis.LoadWorkspace(path)
}
func (a *App) editAnalysis(wb *workbook.Workbook, edit func(*analysis.Workspace) error) error {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	if a.currentWorkbook() != wb {
		return errors.New("工作簿已切换")
	}
	path := a.analysisPath(wb)
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	w, err := analysis.LoadWorkspace(path)
	if err != nil {
		return err
	}
	if err = edit(&w); err != nil {
		return err
	}
	return analysis.SaveWorkspace(path, w)
}
func decodeAnalysis(r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 2<<20)).Decode(dst)
}
func requireAnalysisWorkbook(a *App, w http.ResponseWriter) *workbook.Workbook {
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, 409, "请先打开原始工作簿")
		return nil
	}
	return wb
}
func (a *App) handleAnalysisWorkspace(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	a.analysisMu.Lock()
	data, err := a.loadAnalysis(wb)
	a.analysisMu.Unlock()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	sheets := make([]map[string]any, 0, len(data.Sheets))
	for _, s := range data.Sheets {
		sheets = append(sheets, map[string]any{"id": s.ID, "name": s.Name, "columns": s.Columns, "rows": len(s.Rows), "updated": s.Updated, "revision": s.Revision})
	}
	writeJSON(w, map[string]any{"sheets": sheets, "reports": data.Reports, "dashboardOrder": data.DashboardOrder, "sourceSheets": wb.Sheets, "workbook": wb.DisplayName, "fingerprint": wb.Fingerprint})
}
func (a *App) handleAnalysisSheet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	a.analysisMu.Lock()
	data, err := a.loadAnalysis(wb)
	a.analysisMu.Unlock()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s := data.Sheet(r.URL.Query().Get("id"))
	if s == nil {
		writeError(w, 404, "编辑表不存在")
		return
	}
	from := clamp(parseIntDefault(r.URL.Query().Get("offset"), 0), 0, analysis.MaxSheetRows-1)
	limit := clamp(parseIntDefault(r.URL.Query().Get("limit"), 100), 1, 300)
	var rows [][]string
	if from < len(s.Rows) {
		rows = s.Rows[from:min(len(s.Rows), from+limit)]
	}
	if rows == nil {
		rows = [][]string{}
	}
	writeJSON(w, map[string]any{"id": s.ID, "name": s.Name, "columns": s.Columns, "rows": rows, "offset": from, "total": len(s.Rows), "revision": s.Revision})
}
func (a *App) handleAnalysisCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		Name    string     `json:"name"`
		Columns []string   `json:"columns"`
		Rows    [][]string `json:"rows"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	if !analysis.NameValid(req.Name) {
		writeError(w, 400, "请输入有效的工作表名称")
		return
	}
	if len(req.Columns) == 0 {
		req.Columns = []string{"字段1", "字段2", "字段3", "字段4", "字段5"}
	}
	if len(req.Columns) > analysis.MaxSheetCols || len(req.Rows) > analysis.MaxSheetRows {
		writeError(w, 400, "编辑表尺寸超出限制")
		return
	}
	for _, col := range req.Columns {
		if !analysis.NameValid(col) {
			writeError(w, 400, "列标题无效")
			return
		}
	}
	id := randomID()
	err := a.editAnalysis(wb, func(v *analysis.Workspace) error {
		if len(v.Sheets) >= analysis.MaxSheets {
			return errors.New("最多创建40张编辑表")
		}
		s := analysis.Sheet{ID: id, Name: req.Name, Columns: req.Columns, Rows: [][]string{}, Updated: time.Now().UTC().Format(time.RFC3339)}
		if err := s.Paste(0, 0, req.Rows); err != nil {
			return err
		}
		v.Sheets = append(v.Sheets, s)
		return nil
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]string{"id": id})
}
func (a *App) handleAnalysisEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		ID    string `json:"id"`
		Row   int    `json:"row"`
		Col   int    `json:"col"`
		Value string `json:"value"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	err := a.editAnalysis(wb, func(v *analysis.Workspace) error {
		s := v.Sheet(req.ID)
		if s == nil {
			return errors.New("工作表不存在")
		}
		return s.Edit(req.Row, req.Col, req.Value)
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
func (a *App) handleAnalysisPaste(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		ID   string     `json:"id"`
		Row  int        `json:"row"`
		Col  int        `json:"col"`
		Rows [][]string `json:"rows"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	err := a.editAnalysis(wb, func(v *analysis.Workspace) error {
		s := v.Sheet(req.ID)
		if s == nil {
			return errors.New("工作表不存在")
		}
		return s.Paste(req.Row, req.Col, req.Rows)
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
func (a *App) handleAnalysisColumns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		ID               string   `json:"id"`
		Columns          []string `json:"columns"`
		ExpectedRevision *int64   `json:"expectedRevision,omitempty"`
		WorkbookKey      string   `json:"workbookKey,omitempty"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	if len(req.Columns) < 1 || len(req.Columns) > analysis.MaxSheetCols {
		writeError(w, 400, "列数无效")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，列未修改")
		return
	}
	for _, x := range req.Columns {
		if !analysis.NameValid(x) {
			writeError(w, 400, "列名称无效")
			return
		}
	}
	var revision int64
	err := a.editAnalysis(wb, func(v *analysis.Workspace) error {
		s := v.Sheet(req.ID)
		if s == nil {
			return errors.New("工作表不存在")
		}
		if req.ExpectedRevision != nil && s.Revision != *req.ExpectedRevision {
			return errAnalysisRevision
		}
		for _, row := range s.Rows {
			if len(row) > len(req.Columns) {
				for _, x := range row[len(req.Columns):] {
					if x != "" {
						return errors.New("不能删除含有数据的列")
					}
				}
			}
		}
		for i := range s.Rows {
			if len(s.Rows[i]) > len(req.Columns) {
				s.Rows[i] = s.Rows[i][:len(req.Columns)]
			}
		}
		s.Columns = req.Columns
		s.Updated = time.Now().UTC().Format(time.RFC3339)
		s.Revision++
		revision = s.Revision
		return nil
	})
	if errors.Is(err, errAnalysisRevision) {
		writeError(w, 409, "列标题已由其他操作更新，请重新载入")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"saved": true, "revision": revision})
}
func (a *App) handleAnalysisReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Query       analysis.Query `json:"query"`
		WorkbookKey string         `json:"workbookKey,omitempty"`
		Book        string         `json:"book,omitempty"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint || req.Book != "" && req.Book != sourceBookID(wb) {
		writeError(w, 409, "工作簿已切换，报表未保存")
		return
	}
	if !analysis.NameValid(req.Name) {
		writeError(w, 400, "报表名称无效")
		return
	}
	id := req.ID
	if id == "" {
		id = randomID()
	}
	err := a.editAnalysis(wb, func(v *analysis.Workspace) error {
		if report := v.Report(id); report != nil {
			report.Name = req.Name
			report.Query = req.Query
			report.Updated = time.Now().UTC().Format(time.RFC3339)
			return nil
		}
		if len(v.Reports) >= analysis.MaxReports {
			return errors.New("保存报表数量超过80")
		}
		v.Reports = append(v.Reports, analysis.Report{ID: id, Name: req.Name, Query: req.Query, Updated: time.Now().UTC().Format(time.RFC3339)})
		return nil
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]string{"id": id})
}
func (a *App) handleAnalysisDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		ID   string `json:"id"`
		Kind string `json:"kind"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	err := a.editAnalysis(wb, func(v *analysis.Workspace) error {
		if req.Kind == "sheet" {
			for i, s := range v.Sheets {
				if s.ID == req.ID {
					v.Sheets = append(v.Sheets[:i], v.Sheets[i+1:]...)
					return nil
				}
			}
		}
		if req.Kind == "report" {
			for i, s := range v.Reports {
				if s.ID == req.ID {
					v.Reports = append(v.Reports[:i], v.Reports[i+1:]...)
					filtered := v.DashboardOrder[:0]
					for _, id := range v.DashboardOrder {
						if id != req.ID {
							filtered = append(filtered, id)
						}
					}
					v.DashboardOrder = filtered
					return nil
				}
			}
		}
		return errors.New("对象不存在")
	})
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}

// Fields and source scans share the same strict source snapshot. A sheet source
// never changes the workbook, while editable sheets are snapshotted from JSON.
type analysisSource struct {
	fields          []analysis.Field
	scan            analysis.Scan
	total           int
	stamp           func() string
	truncatedFields bool
	lease           *analysisSourceSnapshot
}

// The observer is opt-in for recipe schema capture and guarded replay. Ordinary
// analysis keeps its existing source values and numeric interpretation.
type sourceCellObserver func(row int, cell workbook.CellData, numeric bool) error

func (a *App) source(wb *workbook.Workbook, source string, header int) (analysisSource, error) {
	return a.sourceObserved(wb, source, header, nil)
}

func (a *App) sourceObserved(wb *workbook.Workbook, source string, header int, observer sourceCellObserver) (analysisSource, error) {
	if strings.HasPrefix(source, "scratch:") {
		a.analysisMu.Lock()
		ws, err := a.loadAnalysis(wb)
		a.analysisMu.Unlock()
		if err != nil {
			return analysisSource{}, err
		}
		s := ws.Sheet(strings.TrimPrefix(source, "scratch:"))
		if s == nil {
			return analysisSource{}, errors.New("编辑表不存在")
		}
		fields := make([]analysis.Field, len(s.Columns))
		for i, name := range s.Columns {
			numeric, nonempty := 0, 0
			for j := 0; j < min(len(s.Rows), 48); j++ {
				if i < len(s.Rows[j]) && strings.TrimSpace(s.Rows[j][i]) != "" {
					nonempty++
					if analysis.IsNumericLiteral(s.Rows[j][i]) {
						numeric++
					}
				}
			}
			fields[i] = analysis.Field{ID: fmt.Sprintf("C%d", i+1), Column: i + 1, Name: name, Numeric: nonempty > 0 && numeric*10 >= nonempty*7}
		}
		scan := func(ctx context.Context, need []string, visit func(analysis.Record) error) error {
			for i, row := range s.Rows {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				vals := make(map[string]analysis.Value, len(need))
				for _, id := range need {
					col, _ := strconv.Atoi(strings.TrimPrefix(id, "C"))
					if col < 1 || col > len(row) {
						continue
					}
					val := row[col-1]
					numeric := analysis.IsNumericLiteral(val)
					if observer != nil {
						if err := observer(i+1, workbook.CellData{Col: col, Raw: val, Display: val, Type: "s"}, numeric); err != nil {
							return err
						}
					}
					vals[id] = analysis.Value{Text: val, Display: val, Numeric: numeric, Textual: !numeric}
				}
				if err := visit(analysis.Record{Number: i + 1, Values: vals}); err != nil {
					return err
				}
			}
			return nil
		}
		// Hash row by row: a backup can restore different contents with the same
		// numeric revision. Avoid encoding a second whole worksheet in memory.
		stamp := func() string { return analysisSheetStamp(s) }
		return analysisSource{fields: fields, scan: scan, total: len(s.Rows), stamp: stamp}, nil
	}
	if !strings.HasPrefix(source, "wb:") {
		return analysisSource{}, errors.New("数据源格式错误")
	}
	meta, ok := wb.FindSheet(strings.TrimPrefix(source, "wb:"))
	if !ok {
		return analysisSource{}, errors.New("源工作表不存在")
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return analysisSource{}, errors.New("源工作表尚未索引，请先选择该表或等待分析任务建立索引")
	}
	if header < 1 || header > idx.MaxRow {
		return analysisSource{}, errors.New("表头行号无效")
	}
	cols := min(idx.MaxCol, analysis.MaxSourceColumns)
	if cols < 1 {
		return analysisSource{}, errors.New("源表没有可分析的列")
	}
	pos := idx.FindRowOffset(header)
	var headerCells []workbook.CellData
	if pos < idx.RowCount() && idx.RowAt(pos).Number == header {
		row, err := wb.ReadRow(idx, pos, 1, cols)
		if err != nil {
			return analysisSource{}, err
		}
		headerCells = row.Cells
	}
	names := make(map[int]string, len(headerCells))
	for _, cell := range headerCells {
		names[cell.Col] = strings.TrimSpace(cell.Display)
	}
	fields := make([]analysis.Field, cols)
	usedNames := make(map[string]bool)
	for i := 0; i < cols; i++ {
		name := names[i+1]
		if name == "" {
			name = excelColName(i+1) + " 列"
		}
		if usedNames[name] {
			name = name + " · " + excelColName(i+1)
		}
		usedNames[name] = true
		fields[i] = analysis.Field{ID: fmt.Sprintf("C%d", i+1), Column: i + 1, Name: name}
	}
	scan := func(ctx context.Context, need []string, visit func(analysis.Record) error) error {
		first := idx.FindRowOffset(header + 1)
		references := func(yield func(workbook.SourceRowReference) bool) {
			for i := first; i < idx.RowCount(); i++ {
				if !yield(workbook.SourceRowReference{Number: idx.RowAt(i).Number, Position: i}) {
					return
				}
			}
		}
		return scanWorkbookAnalysisRows(ctx, wb, idx, iter.Seq[workbook.SourceRowReference](references), need, observer, visit)
	}
	return analysisSource{fields: fields, scan: scan, total: idx.RowCount() - idx.FindRowOffset(header+1), stamp: func() string { return wb.Fingerprint }, truncatedFields: idx.MaxCol > analysis.MaxSourceColumns}, nil
}
func excelColName(col int) string {
	out := ""
	for col > 0 {
		col--
		out = string(rune('A'+col%26)) + out
		col /= 26
	}
	return out
}

func (a *App) handleAnalysisFields(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	source := r.URL.Query().Get("source")
	header := clamp(parseIntDefault(r.URL.Query().Get("headerRow"), 1), 1, formats.ExternalRows)
	release, err := a.acquireCompositionAnalysisSlot(r.Context(), source)
	if err != nil {
		writeError(w, 408, "读取联合来源已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	s, err := a.sourceRequest(r.Context(), wb, source, header, r.URL.Query().Get("sourceId"))
	if err != nil {
		status := 400
		if strings.Contains(err.Error(), "尚未索引") {
			status = 409
		}
		writeError(w, status, err.Error())
		return
	}
	defer s.close()
	// Sample up to eight actual rows across *all* 256 source columns. The
	// initial preview still transfers only 16 columns, but templates need the
	// numeric typing for fields beyond P. Reading eight rows directly avoids
	// running a whole 256-row scan batch on every schema inspection.
	sampled := make([]analysis.DrillRow, 0, 8)
	if s.lease != nil && s.lease.data.composition != nil {
		sampled, err = compositionAnalysisPreview(r.Context(), &s)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		header = 0
	} else if s.lease != nil {
		sampled, err = scopedAnalysisPreview(r.Context(), wb, &s, header)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
	} else if strings.HasPrefix(source, "wb:") {
		meta, _ := wb.FindSheet(strings.TrimPrefix(source, "wb:"))
		idx := wb.GetCachedIndex(meta.Path)
		start := idx.FindRowOffset(header + 1)
		if start < idx.RowCount() {
			rows, readErr := wb.ReadRowsContext(r.Context(), idx, start, min(8, idx.RowCount()-start), 1, len(s.fields))
			if readErr != nil {
				writeError(w, 500, readErr.Error())
				return
			}
			counts := make([]int, len(s.fields))
			numbers := make([]int, len(s.fields))
			for _, row := range rows {
				first := make([]string, min(16, len(s.fields)))
				for _, c := range row.Cells {
					if c.Col < 1 || c.Col > len(s.fields) {
						continue
					}
					text := c.Raw
					if c.Type != "" && c.Type != "n" {
						text = c.Display
					}
					if strings.TrimSpace(text) != "" {
						counts[c.Col-1]++
						if sourceCellNumeric(wb, c) {
							numbers[c.Col-1]++
						}
					}
					if c.Col <= len(first) {
						first[c.Col-1] = c.Display
					}
				}
				sampled = append(sampled, analysis.DrillRow{Row: row.Number, Cells: first})
			}
			for i := range s.fields {
				s.fields[i].Numeric = counts[i] > 0 && numbers[i]*10 >= counts[i]*7
			}
		}
	} else {
		need := make([]string, 0, min(16, len(s.fields)))
		for _, f := range s.fields[:min(16, len(s.fields))] {
			need = append(need, f.ID)
		}
		err = s.scan(r.Context(), need, func(rec analysis.Record) error {
			cells := make([]string, len(need))
			for i, id := range need {
				v := rec.Values[id]
				if v.Display != "" {
					cells[i] = v.Display
				} else {
					cells[i] = v.Text
				}
			}
			sampled = append(sampled, analysis.DrillRow{Row: rec.Number, Cells: cells})
			if len(sampled) >= 8 {
				return errAnalysisSampleDone
			}
			return nil
		})
		if err != nil && !errors.Is(err, errAnalysisSampleDone) {
			writeError(w, 500, err.Error())
			return
		}
	}
	response := map[string]any{"fields": s.fields, "preview": sampled, "totalRows": s.total, "headerRow": header, "truncatedFields": s.truncatedFields}
	if s.lease != nil {
		if s.lease.data.composition != nil {
			response["composition"] = s.lease.data.composition.data.plan.Definition()
		} else {
			response["sourceScope"] = s.lease.canonicalScope()
		}
	}
	// Replay adopts a schema only after confirming its completed source stamp.
	// Ordinary field previews do not hash the entire saved analysis table.
	if r.URL.Query().Get("stamp") == "1" {
		response["sourceStamp"] = s.stamp()
	}
	writeJSON(w, response)
}

var errAnalysisSampleDone = errors.New("preview complete")

func (a *App) handleAnalysisQueryStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	if key := r.Header.Get("X-ExcelLens-Workbook"); key != "" && key != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，请重新选择分析数据源")
		return
	}
	var req struct {
		analysis.Query
		SourceID string `json:"sourceId,omitempty"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "分析条件无效")
		return
	}
	q := req.Query
	if q.HeaderRow == 0 && !strings.HasPrefix(q.Source, "append:") {
		q.HeaderRow = 1
	}
	if len(q.Source) == 0 || len(q.Rows) > 3 || len(q.Filters) > 8 || len(q.Transforms) > 8 {
		writeError(w, 400, "分析条件超出限制")
		return
	}
	var captured *analysisSourceSnapshot
	if req.SourceID != "" {
		var err error
		if strings.HasPrefix(q.Source, "append:") {
			captured, err = a.borrowCompositionAnalysisSource(wb, q.Source, req.SourceID)
		} else {
			captured, err = a.analysisSourceSnapshot(req.SourceID, wb)
		}
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		defer captured.close()
	}
	job, err := a.startAnalysisTask(wb, q, nil, captured)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]string{"jobId": job.ID})
}

func (a *App) startAnalysisTask(wb *workbook.Workbook, q analysis.Query, plan *recipeRunPlan, captured ...*analysisSourceSnapshot) (*analysisTask, error) {
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if len(captured) > 1 {
		return nil, errors.New("分析来源快照数量无效")
	}
	var base *analysisSourceSnapshot
	if len(captured) == 1 {
		base = captured[0]
	}
	if base != nil && base.closed.Load() {
		return nil, errors.New("分析来源快照已释放")
	}
	var err error
	q, err = compositionAnalysisDefinition(q, base)
	if err != nil {
		return nil, err
	}
	if q.Composition != nil && plan != nil {
		return nil, errors.New("联合来源分析方案尚未接入")
	}
	if q.SourceScope != nil {
		scope, err := normalizedAnalysisScope(wb, q.Source, *q.SourceScope)
		if err != nil {
			return nil, err
		}
		q.SourceScope = &scope
	}
	var rows *analysisSourceSnapshot
	if base != nil {
		if base.closed.Load() || base.data.wb != wb || base.data.source != q.Source || q.SourceScope != nil && q.SourceScope.Digest() != base.data.scope.Digest() {
			return nil, errors.New("分析来源快照与查询范围不一致")
		}
		var err error
		rows, err = base.retain()
		if err != nil {
			return nil, err
		}
		q.SourceScope = base.canonicalScope()
	}
	accepted := false
	var releaseBooks func()
	if q.Composition != nil && rows == nil {
		releaseBooks, err = a.pinCompositionAnalysisBooks(q)
		if err != nil {
			rows.close()
			return nil, err
		}
	}
	defer func() {
		if !accepted {
			rows.close()
			if releaseBooks != nil {
				releaseBooks()
			}
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	job := &analysisTask{ID: randomID(), Workbook: wb, Query: q, RecipePlan: plan, SourceRows: rows, Status: "queued", Stage: "准备数据源", Created: time.Now(), cancel: cancel, ctx: ctx, done: make(chan struct{})}
	a.analysisMu.Lock()
	if a.currentWorkbook() != wb {
		a.analysisMu.Unlock()
		cancel()
		return nil, errors.New("工作簿已切换，分析未启动")
	}
	// Bound retained jobs; old completed results remain accessible until evicted.
	if len(a.analysisJobs) >= 12 {
		var oldest *analysisTask
		for _, j := range a.analysisJobs {
			select {
			case <-j.done:
				if oldest == nil || j.Created.Before(oldest.Created) {
					oldest = j
				}
			default:
			}
		}
		if oldest == nil {
			a.analysisMu.Unlock()
			cancel()
			return nil, errors.New("已有12项分析任务，请完成或取消后重试")
		}
		oldest.cancel()
		oldest.closeSourceRows()
		delete(a.analysisJobs, oldest.ID)
	}
	primaryRelease := a.pinWorkbooks(wb)
	job.release = func() {
		primaryRelease()
		if releaseBooks != nil {
			releaseBooks()
		}
	}
	a.analysisJobs[job.ID] = job
	a.analysisMu.Unlock()
	accepted = true
	go a.runAnalysisQuery(job, q)
	return job, nil
}
func (a *App) runAnalysisQuery(job *analysisTask, q analysis.Query) {
	defer close(job.done)
	defer func() {
		job.mu.Lock()
		if job.Status != "done" {
			job.SourceRows.close()
			job.SourceRows = nil
		}
		job.mu.Unlock()
	}()
	if job.release != nil {
		defer job.release()
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-job.ctx.Done():
		job.mu.Lock()
		job.Status = "cancelled"
		job.mu.Unlock()
		return
	}
	job.mu.Lock()
	job.Status = "running"
	job.Stage = "读取字段及建立索引"
	job.mu.Unlock()
	wb := job.Workbook
	if strings.HasPrefix(q.Source, "wb:") {
		meta, ok := wb.FindSheet(strings.TrimPrefix(q.Source, "wb:"))
		if !ok {
			a.finishAnalysis(job, analysis.Result{}, errors.New("源工作表不存在"))
			return
		}
		if wb.GetCachedIndex(meta.Path) == nil {
			select {
			case a.indexSem <- struct{}{}:
			case <-job.ctx.Done():
				a.finishAnalysis(job, analysis.Result{}, job.ctx.Err())
				return
			}
			_, err := wb.BuildIndex(meta, nil, job.ctx.Done())
			<-a.indexSem
			if err != nil {
				a.finishAnalysis(job, analysis.Result{}, err)
				return
			}
		}
	}
	releaseSlot, err := a.acquireCompositionAnalysisSlot(job.ctx, q.Source)
	if err != nil {
		a.finishAnalysis(job, analysis.Result{}, err)
		return
	}
	defer releaseSlot()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if q.Composition != nil && job.SourceRows == nil {
		composition, err := a.captureCompositionWithQuerySlot(job.ctx, *q.Composition, true)
		if err != nil {
			a.finishAnalysis(job, analysis.Result{}, err)
			return
		}
		job.mu.Lock()
		job.SourceRows = ownCompositionAnalysisSource(wb, composition)
		job.mu.Unlock()
	}
	var observer sourceCellObserver
	if strings.HasPrefix(q.Source, "wb:") && (job.RecipePlan != nil || q.Layout == "tree") {
		if err := wb.CheckSourceUnchanged(); err != nil {
			a.finishAnalysis(job, analysis.Result{}, err)
			return
		}
	}
	if plan := job.RecipePlan; plan != nil {
		observer = guardRecipeTypes(wb.Format, plan.Masks)
		if strings.HasPrefix(q.Source, "scratch:") {
			observer = guardRecipeTypes("scratch", plan.Masks)
		}
	}
	src, err := a.sourceQuery(job.ctx, wb, q, observer, job.SourceRows)
	if err != nil {
		a.finishAnalysis(job, analysis.Result{}, err)
		return
	}
	defer src.close()
	if src.lease != nil && job.SourceRows == nil {
		job.mu.Lock()
		job.SourceRows, _ = src.lease.retain()
		job.mu.Unlock()
	}
	if err = analysis.Validate(q, src.fields); err != nil {
		a.finishAnalysis(job, analysis.Result{}, err)
		return
	}
	stamp := src.stamp()
	if job.RecipePlan != nil && job.RecipePlan.Stamp != stamp {
		a.finishAnalysis(job, analysis.Result{}, errors.New("分析方案目标来源已修改，请重新读取字段并核对映射"))
		return
	}
	job.mu.Lock()
	job.Stage = "正在逐行聚合（精确十进制）"
	job.Total = int64(src.total)
	job.SourceStamp = stamp
	needed := make(map[string]bool)
	for _, id := range analysis.RequiredFields(q) {
		needed[id] = true
	}
	job.SourceFields = make([]analysis.Field, 0, len(needed))
	for _, field := range src.fields {
		if needed[field.ID] {
			job.SourceFields = append(job.SourceFields, field)
		}
	}
	job.mu.Unlock()
	scan := src.scan
	if job.RecipePlan != nil {
		scan = recipeScanInputs(scan, job.RecipePlan.Masks)
	}
	result, err := analysis.Run(job.ctx, q, src.fields, scan, func(scanned int64) { job.mu.Lock(); job.Progress = scanned; job.mu.Unlock() })
	a.finishAnalysis(job, result, err)
}
func (a *App) finishAnalysis(job *analysisTask, res analysis.Result, err error) {
	job.mu.Lock()
	defer job.mu.Unlock()
	if err == nil && job.ctx.Err() != nil {
		err = job.ctx.Err()
	}
	if err != nil {
		if job.ctx.Err() != nil {
			job.Status = "cancelled"
		} else {
			job.Status = "error"
		}
		job.Stage = "任务结束"
		job.Error = err.Error()
		return
	}
	if a.currentWorkbook() != job.Workbook {
		job.Status = "cancelled"
		return
	}
	job.Result = res
	job.Status = "done"
	job.Stage = "完成"
	job.Progress = res.Scanned
}
func (a *App) getAnalysisJob(id string) *analysisTask {
	a.analysisMu.Lock()
	defer a.analysisMu.Unlock()
	return a.analysisJobs[id]
}

func (a *App) clearAnalysisTasks() {
	a.analysisMu.Lock()
	jobs := a.analysisJobs
	a.analysisJobs = make(map[string]*analysisTask)
	trees := a.analysisTreeJobs
	a.analysisTreeJobs = make(map[string]*analysisTreeTask)
	sources := a.analysisSources
	a.analysisSources = make(map[string]*analysisSourceSnapshot)
	for _, job := range jobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
	for _, job := range trees {
		job.cancel()
	}
	a.analysisMu.Unlock()
	for _, source := range sources {
		source.close()
	}
	for _, job := range jobs {
		if job.done != nil {
			<-job.done
		}
		job.closeSourceRows()
	}
	for _, job := range trees {
		<-job.done
	}
}
func (a *App) handleAnalysisQueryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	job := a.getAnalysisJob(r.URL.Query().Get("id"))
	if job == nil || a.currentWorkbook() != job.Workbook {
		writeError(w, 404, "分析任务不存在或工作簿已切换")
		return
	}
	job.mu.RLock()
	response := map[string]any{"status": job.Status, "stage": job.Stage, "progress": job.Progress, "total": job.Total, "error": job.Error}
	if job.Status == "done" {
		response["result"] = job.Result
		response["query"] = job.Query
		response["sourceFields"] = job.SourceFields
		if job.RecipePlan != nil {
			response["recipeId"], response["exportFormat"] = job.RecipePlan.RecipeID, job.RecipePlan.ExportFormat
		}
	}
	// Published results and query fields are immutable. Copy their references
	// with the status, then keep both JSON encoding and network IO outside mu.
	job.mu.RUnlock()
	writeJSON(w, response)
}
func (a *App) handleAnalysisQueryCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数无效")
		return
	}
	job := a.getAnalysisJob(req.ID)
	if job == nil {
		writeError(w, 404, "任务不存在")
		return
	}
	job.cancel()
	writeJSON(w, map[string]bool{"cancelled": true})
}
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
	release, err := a.acquireCompositionAnalysisSlot(r.Context(), req.Query.Source)
	if err != nil {
		writeError(w, 408, "下钻已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	src, err := a.sourceResult(r.Context(), wb, req.Query, req.JobID)
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
	release, err := a.acquireCompositionAnalysisSlot(r.Context(), req.Query.Source)
	if err != nil {
		writeError(w, 408, "明细提取已取消")
		return
	}
	defer release()
	a.cacheMu.RLock()
	src, err := a.sourceResult(r.Context(), wb, req.Query, req.JobID)
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

func (a *App) handleAnalysisDashboardOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "参数格式无效")
		return
	}
	if len(req.IDs) > analysis.MaxReports {
		writeError(w, 400, "仪表盘顺序超出限制")
		return
	}
	err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		known := make(map[string]bool, len(ws.Reports))
		for _, rp := range ws.Reports {
			known[rp.ID] = true
		}
		seen := make(map[string]bool, len(req.IDs))
		for _, id := range req.IDs {
			if !known[id] || seen[id] {
				return errors.New("报表顺序包含重复或不存在的标识")
			}
			seen[id] = true
		}
		ws.DashboardOrder = append([]string{}, req.IDs...)
		return nil
	})
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"saved": true})
}
