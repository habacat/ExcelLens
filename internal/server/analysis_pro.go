package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/formats"
)

// Batch writes implement optimistic concurrency. Clients with an outdated
// revision receive 409, never silently overwrite a newer cell value.
func (a *App) handleAnalysisBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		WorkbookKey      string                `json:"workbookKey,omitempty"`
		ID               string                `json:"id"`
		ExpectedRevision *int64                `json:"expectedRevision"`
		Changes          []analysis.CellChange `json:"changes"`
	}
	if err := decodeAnalysis(r, &req); err != nil {
		writeError(w, 400, "修改内容不合法")
		return
	}
	if req.ExpectedRevision == nil {
		writeError(w, 400, "必须携带预期版本号")
		return
	}
	if req.WorkbookKey != "" && req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，编辑草稿未写入")
		return
	}
	var previous []analysis.CellChange
	var revision int64
	err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		sheet := ws.Sheet(req.ID)
		if sheet == nil {
			return errors.New("编辑表不存在")
		}
		if sheet.Revision != *req.ExpectedRevision {
			return errAnalysisRevision
		}
		var applyErr error
		previous, applyErr = sheet.ApplyEdits(req.Changes)
		if applyErr == nil {
			revision = sheet.Revision
		}
		return applyErr
	})
	if errors.Is(err, errAnalysisRevision) {
		writeError(w, 409, "编辑表已被其他操作修改，请刷新后重试")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"saved": true, "revision": revision, "previous": previous})
}

func (a *App) handleAnalysisAppendRows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	var req struct {
		ID               string `json:"id"`
		WorkbookKey      string `json:"workbookKey"`
		ExpectedRevision *int64 `json:"expectedRevision"`
		Count            int    `json:"count"`
	}
	if err := decodeAnalysis(r, &req); err != nil || req.ExpectedRevision == nil {
		writeError(w, 400, "追加行参数或预期版本无效")
		return
	}
	if req.WorkbookKey != wb.Fingerprint {
		writeError(w, 409, "工作簿已切换，未追加行")
		return
	}
	var revision int64
	var total int
	err := a.editAnalysis(wb, func(ws *analysis.Workspace) error {
		s := ws.Sheet(req.ID)
		if s == nil {
			return errors.New("编辑表不存在")
		}
		if s.Revision != *req.ExpectedRevision {
			return errAnalysisRevision
		}
		if err := s.AppendRows(req.Count); err != nil {
			return err
		}
		revision, total = s.Revision, len(s.Rows)
		return nil
	})
	if errors.Is(err, errAnalysisRevision) {
		writeError(w, 409, "编辑表已变化，请刷新后追加行")
		return
	}
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"revision": revision, "total": total})
}

var errAnalysisRevision = errors.New("analysis revision mismatch")

// A backup is bound to one immutable workbook fingerprint to prevent accidental
// import into an unrelated workbook. Old workspace is checkpointed on save.
type analysisBackup struct {
	Format      string             `json:"format"`
	Fingerprint string             `json:"fingerprint"`
	Created     string             `json:"created"`
	Workspace   analysis.Workspace `json:"workspace"`
}

func (a *App) handleAnalysisBackup(w http.ResponseWriter, r *http.Request) {
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.analysisMu.Lock()
		ws, err := a.loadAnalysis(wb)
		a.analysisMu.Unlock()
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="ExcelLens_analysis_backup.json"`)
		writeJSON(w, analysisBackup{Format: "ExcelLens-Analysis-v1", Fingerprint: wb.Fingerprint, Created: time.Now().UTC().Format(time.RFC3339), Workspace: ws})
	case http.MethodPost:
		var backup analysisBackup
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, analysis.MaxWorkspaceBytes+(1<<20)))
		if err := decoder.Decode(&backup); err != nil {
			writeError(w, 400, "备份文件无法解析或超过大小限制")
			return
		}
		if backup.Format != "ExcelLens-Analysis-v1" || backup.Fingerprint != wb.Fingerprint {
			writeError(w, 400, "备份的原始工作簿与当前文件不匹配")
			return
		}
		if err := analysis.ValidateWorkspace(backup.Workspace); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		err := a.editAnalysis(wb, func(ws *analysis.Workspace) error { *ws = backup.Workspace; return nil })
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"restored": true})
	default:
		methodNotAllowed(w)
	}
}

// Field profile reads only the requested source field, using bounded memory.
func (a *App) handleAnalysisProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	select {
	case a.analysisSem <- struct{}{}:
		defer func() { <-a.analysisSem }()
	case <-r.Context().Done():
		writeError(w, 408, "字段画像已取消")
		return
	}
	source := r.URL.Query().Get("source")
	release, err := a.acquireCompositionAnalysisSlot(r.Context(), source)
	if err != nil {
		writeError(w, 408, "字段画像已取消")
		return
	}
	defer release()
	header := clamp(parseIntDefault(r.URL.Query().Get("headerRow"), 1), 1, formats.ExternalRows)
	field := strings.TrimSpace(r.URL.Query().Get("field"))
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	src, err := a.sourceRequest(r.Context(), wb, source, header, r.URL.Query().Get("sourceId"))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	defer src.close()
	valid := false
	name := ""
	for _, f := range src.fields {
		if f.ID == field {
			valid = true
			name = f.Name
			break
		}
	}
	if !valid {
		writeError(w, 400, "字段不存在")
		return
	}
	const maxUnique = 50000
	counts := make(map[string]int, 256)
	var frequencyBytes int64
	uniqueLimited := false
	values, total, empty, numeric, missing := 0, 0, 0, 0, 0
	minText, maxText := "", ""
	var minNum, maxNum string
	scanErr := src.scan(r.Context(), []string{field}, func(rec analysis.Record) error {
		total++
		v := rec.Values[field]
		if v.MissingCache {
			missing++
			return nil
		}
		if v.Text == "" {
			empty++
			return nil
		}
		values++
		if counts[v.Text] > 0 {
			counts[v.Text]++
		} else if len(counts) < maxUnique && frequencyBytes+int64(len(v.Text))+64 <= 32<<20 {
			counts[v.Text] = 1
			frequencyBytes += int64(len(v.Text)) + 64
		} else {
			uniqueLimited = true
		}
		if minText == "" || strings.Compare(v.Text, minText) < 0 {
			minText = v.Text
		}
		if maxText == "" || strings.Compare(v.Text, maxText) > 0 {
			maxText = v.Text
		}
		if d, ok := analysis.Decimal(v.Text); ok && v.Numeric {
			numeric++
			if minNum == "" {
				minNum = v.Text
				maxNum = v.Text
			} else {
				mn, _ := analysis.Decimal(minNum)
				mx, _ := analysis.Decimal(maxNum)
				if d.Cmp(mn) < 0 {
					minNum = v.Text
				}
				if d.Cmp(mx) > 0 {
					maxNum = v.Text
				}
			}
		}
		return nil
	})
	if scanErr != nil {
		writeError(w, 500, scanErr.Error())
		return
	}
	type item struct {
		Value string `json:"value"`
		Count int    `json:"count"`
	}
	top := make([]item, 0, len(counts))
	for key, n := range counts {
		top = append(top, item{key, n})
	}
	sort.Slice(top, func(i, j int) bool {
		if top[i].Count != top[j].Count {
			return top[i].Count > top[j].Count
		}
		return top[i].Value < top[j].Value
	})
	if len(top) > 12 {
		top = top[:12]
	}
	writeJSON(w, map[string]any{"field": field, "name": name, "rows": total, "nonEmpty": values, "empty": empty, "missingFormulaCaches": missing,
		"unique": len(counts), "uniqueTruncated": uniqueLimited, "numeric": numeric, "minText": minText, "maxText": maxText,
		"minNumber": minNum, "maxNumber": maxNum, "frequent": top})
}
