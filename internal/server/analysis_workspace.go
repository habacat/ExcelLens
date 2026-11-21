package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/workbook"
)

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

func (a *App) registerAnalysisRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/analysis/clusters/start", a.handleClusterStart)
	mux.HandleFunc("/api/analysis/clusters/status", a.handleClusterStatus)
	mux.HandleFunc("/api/analysis/clusters/cancel", a.handleClusterCancel)
	mux.HandleFunc("/api/analysis/clusters/clear", a.handleClusterClear)
	mux.HandleFunc("/api/analysis/clusters/groups", a.handleClusterGroups)
	mux.HandleFunc("/api/analysis/clusters/detail", a.handleClusterDetail)
	mux.HandleFunc("/api/analysis/clusters/confirm", a.handleClusterConfirm)
	mux.HandleFunc("/api/analysis/clusters/resolve", a.handleClusterResolve)
	mux.HandleFunc("/api/analysis/clusters/export", a.handleClusterExport)
	mux.HandleFunc("/api/analysis/snapshots", a.handleComparisonSnapshots)
	mux.HandleFunc("/api/analysis/snapshots/compare", a.handleSnapshotComparison)
	mux.HandleFunc("/api/recipes", a.handleRecipes)
	mux.HandleFunc("/api/recipes/capture/start", a.handleRecipeCaptureStart)
	mux.HandleFunc("/api/recipes/capture/status", a.handleRecipeCaptureStatus)
	mux.HandleFunc("/api/recipes/capture/cancel", a.handleRecipeCaptureCancel)
	mux.HandleFunc("/api/recipes/capture/commit", a.handleRecipeCaptureCommit)
	mux.HandleFunc("/api/recipes/preview", a.handleRecipePreview)
	mux.HandleFunc("/api/recipes/run/start", a.handleRecipeRunStart)
	mux.HandleFunc("/api/recipes/run/status", a.handleAnalysisQueryStatus)
	mux.HandleFunc("/api/recipes/run/cancel", a.handleAnalysisQueryCancel)
	mux.HandleFunc("/api/analysis/sheet/rows", a.handleAnalysisAppendRows)
	mux.HandleFunc("/api/analysis/issues/materialize", a.handleAnalysisIssuesMaterialize)
	mux.HandleFunc("/api/analysis/issues/export", a.handleAnalysisIssuesExport)
	mux.HandleFunc("/api/analysis/workspace", a.handleAnalysisWorkspace)
	mux.HandleFunc("/api/analysis/backup", a.handleAnalysisBackup)
	mux.HandleFunc("/api/analysis/profile", a.handleAnalysisProfile)
	mux.HandleFunc("/api/analysis/sheet/batch", a.handleAnalysisBatch)
	mux.HandleFunc("/api/analysis/fields", a.handleAnalysisFields)
	mux.HandleFunc("/api/analysis/source/capture", a.handleAnalysisSourceCapture)
	mux.HandleFunc("/api/analysis/source/restore", a.handleAnalysisSourceRestore)
	mux.HandleFunc("/api/analysis/source/clear", a.handleAnalysisSourceClear)
	mux.HandleFunc("/api/analysis/sheet", a.handleAnalysisSheet)
	mux.HandleFunc("/api/analysis/sheet/edit", a.handleAnalysisEdit)
	mux.HandleFunc("/api/analysis/sheet/paste", a.handleAnalysisPaste)
	mux.HandleFunc("/api/analysis/sheet/create", a.handleAnalysisCreate)
	mux.HandleFunc("/api/analysis/sheet/columns", a.handleAnalysisColumns)
	mux.HandleFunc("/api/analysis/report", a.handleAnalysisReport)
	mux.HandleFunc("/api/analysis/delete", a.handleAnalysisDelete)
	mux.HandleFunc("/api/analysis/query/start", a.handleAnalysisQueryStart)
	mux.HandleFunc("/api/analysis/query/status", a.handleAnalysisQueryStatus)
	mux.HandleFunc("/api/analysis/query/cancel", a.handleAnalysisQueryCancel)
	mux.HandleFunc("/api/analysis/tree/start", a.handleAnalysisTreeStart)
	mux.HandleFunc("/api/analysis/tree/status", a.handleAnalysisTreeStatus)
	mux.HandleFunc("/api/analysis/tree/cancel", a.handleAnalysisTreeCancel)
	mux.HandleFunc("/api/analysis/tree/clear", a.handleAnalysisTreeClear)
	mux.HandleFunc("/api/analysis/drill", a.handleAnalysisDrill)
	mux.HandleFunc("/api/analysis/composition/navigate", a.handleAnalysisCompositionNavigate)
	mux.HandleFunc("/api/analysis/materialize", a.handleAnalysisMaterialize)
	mux.HandleFunc("/api/analysis/detail/materialize", a.handleAnalysisMaterializeDetail)
	mux.HandleFunc("/api/analysis/dashboard/order", a.handleAnalysisDashboardOrder)
	mux.HandleFunc("/api/analysis/export", a.handleAnalysisExport)
	mux.HandleFunc("/api/settings/analysis-display", a.handleAnalysisDisplay)
}
