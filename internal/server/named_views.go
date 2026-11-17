package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"
	"excellens/internal/reconcile"
	"excellens/internal/workbook"
)

const maxNamedViews = 32
const maxNamedViewBytes = 256 << 10
const maxNamedViewsBytes = 4 << 20

type namedViewLayout struct {
	Rows             map[string]int  `json:"rows"`
	Cols             map[string]int  `json:"cols"`
	Font             string          `json:"font"`
	ColumnMode       string          `json:"columnMode"`
	ColumnVisibility map[string]bool `json:"columnVisibility"`
}

type namedViewTable struct {
	Row      int  `json:"row"`
	StartCol int  `json:"startCol"`
	EndCol   int  `json:"endCol"`
	Sticky   bool `json:"sticky"`
}

type namedViewConfig struct {
	Freeze        freezeSettings             `json:"freeze"`
	Layout        namedViewLayout            `json:"layout"`
	Table         *namedViewTable            `json:"table"`
	Zoom          int                        `json:"zoom"`
	ShowStyles    bool                       `json:"showStyles"`
	ShowFormulas  bool                       `json:"showFormulas"`
	RowVisibility *workbook.AxisViewSettings `json:"rowVisibility,omitempty"`
}

// Content identity allows a renamed byte-equivalent workbook to reuse its
// views. Columns never rebind by position to different workbook contents.
type viewSourceBinding struct {
	Fingerprint string            `json:"fingerprint"`
	Format      string            `json:"format"`
	Name        string            `json:"name"`
	Sheet       string            `json:"sheet"`
	SheetPath   string            `json:"sheetPath"`
	HeaderRow   int               `json:"headerRow"`
	Columns     int               `json:"columns"`
	Fields      []reconcile.Field `json:"fields"`
}

type namedView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	RuleVersion int               `json:"ruleVersion"`
	Source      viewSourceBinding `json:"source"`
	Query       filterSpec        `json:"query"`
	Config      namedViewConfig   `json:"config"`
	Created     string            `json:"created"`
	Updated     string            `json:"updated"`
}

type namedViewStore struct {
	Version     int         `json:"version"`
	Revision    int64       `json:"revision"`
	Fingerprint string      `json:"fingerprint"`
	Items       []namedView `json:"items"`
}

func namedViewsPath(wb *workbook.Workbook) (string, error) {
	return savedConfigPath("named-views", wb.Fingerprint)
}

func readNamedViews(path, fingerprint string) (namedViewStore, error) {
	store := namedViewStore{Version: 1, Fingerprint: fingerprint, Items: []namedView{}}
	found, err := readSavedConfig(path, maxNamedViewsBytes, &store)
	if err != nil || !found {
		return store, err
	}
	if store.Version != 1 || store.Fingerprint != fingerprint || store.Revision < 0 || store.Revision >= maxConfigRevision || len(store.Items) > maxNamedViews {
		return store, errors.New("命名视图版本、来源或容量无效，原文件已保留")
	}
	ids, names := make(map[string]bool), make(map[string]bool)
	for _, view := range store.Items {
		name := strings.ToLower(view.Name)
		encoded, _ := json.Marshal(view)
		if !validSessionID(view.ID) || !validViewName(view.Name) || ids[view.ID] || names[name] || len(encoded) > maxNamedViewBytes || view.Source.Fingerprint != fingerprint {
			return store, errors.New("命名视图结构无效，原文件已保留")
		}
		ids[view.ID], names[name] = true, true
	}
	return store, nil
}

func validViewName(name string) bool {
	return name != "" && name == strings.TrimSpace(name) && utf8.ValidString(name) && utf8.RuneCountInString(name) <= 80 && !strings.ContainsAny(name, "\r\n\x00")
}

func validateNamedViewConfig(config namedViewConfig, maxRow int) error {
	if !validateFreeze(config.Freeze) || config.Zoom < 70 || config.Zoom > 125 || config.Zoom%5 != 0 || !validColumnMode(config.Layout.ColumnMode) || !validFontOverride(config.Layout.Font) {
		return errors.New("视图的冻结、缩放或显示设置无效")
	}
	if config.RowVisibility != nil {
		if _, err := config.RowVisibility.Normalized(formats.ExternalRows); err != nil {
			return err
		}
	}
	for kind, sizes := range map[string]map[string]int{"row": config.Layout.Rows, "col": config.Layout.Cols} {
		for key, value := range sizes {
			index, err := strconv.Atoi(key)
			if err != nil || strconv.Itoa(index) != key || value == 0 || !validLayoutSize(kind, index, value) {
				return errors.New("视图的行列尺寸无效")
			}
		}
	}
	for key := range config.Layout.ColumnVisibility {
		index, err := strconv.Atoi(key)
		if err != nil || strconv.Itoa(index) != key || index < 1 || index > formats.ExcelColumns {
			return errors.New("视图的隐藏列设置无效")
		}
	}
	if table := config.Table; table != nil && (table.Row < 1 || table.Row > maxRow || table.StartCol < 1 || table.EndCol < table.StartCol || table.EndCol > formats.ExcelColumns) {
		return errors.New("视图的表头范围无效")
	}
	return nil
}

func (a *App) captureViewBinding(ctx context.Context, wb *workbook.Workbook, sheet string, query filterSpec) (viewSourceBinding, *workbook.SheetIndex, error) {
	meta, ok := wb.FindSheet(sheet)
	if !ok {
		return viewSourceBinding{}, nil, errors.New("命名视图的工作表不存在")
	}
	source, err := a.buildReconcileSource(ctx, wb, reconcileSourceRef{Book: sourceBookID(wb), Source: "wb:" + meta.Name, HeaderRow: query.HeaderRow}, nil)
	if err != nil {
		return viewSourceBinding{}, nil, err
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return viewSourceBinding{}, nil, errors.New("视图来源索引失效，请重新打开文件")
	}
	if query.active() {
		if err := workbook.ValidateFilterSpec(idx, query.Rules, query.options()); err != nil {
			return viewSourceBinding{}, nil, err
		}
	}
	binding := viewSourceBinding{Fingerprint: wb.Fingerprint, Format: wb.Format, Name: workbookDisplayName(wb), Sheet: meta.Name, SheetPath: meta.Path, HeaderRow: query.HeaderRow, Columns: idx.MaxCol, Fields: []reconcile.Field{}}
	needed := make(map[int]bool)
	for _, rule := range query.Rules {
		needed[rule.Column] = true
	}
	for _, key := range query.SortKeys {
		needed[key.Column] = true
	}
	if query.SortColumn > 0 {
		needed[query.SortColumn] = true
	}
	for column := 1; column <= formats.ExcelColumns; column++ {
		if needed[column] {
			field := sourceField(column, "", false)
			if column <= len(source.Info.Fields) {
				field = source.Info.Fields[column-1]
			}
			binding.Fields = append(binding.Fields, field)
		}
	}
	return binding, idx, nil
}

func (a *App) handleNamedViews(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	path, err := namedViewsPath(wb)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err := readNamedViews(path, wb.Fingerprint)
		lock.RUnlock()
		if err != nil {
			writeError(w, 422, err.Error())
			return
		}
		writeJSON(w, map[string]any{"book": sourceBookID(wb), "store": store})
		return
	}
	var req struct {
		Action           string          `json:"action"`
		ID               string          `json:"id"`
		Name             string          `json:"name"`
		Book             string          `json:"book"`
		WorkbookKey      string          `json:"workbookKey"`
		ExpectedRevision *int64          `json:"expectedRevision"`
		Sheet            string          `json:"sheet"`
		FilterID         string          `json:"filterId"`
		ViewID           string          `json:"viewId,omitempty"`
		HeaderRow        int             `json:"headerRow"`
		Config           namedViewConfig `json:"config"`
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxNamedViewBytes+8193))
	if err != nil || len(data) > maxNamedViewBytes+8192 || decodeSavedConfig(data, &req) != nil || req.ExpectedRevision == nil || req.Action != "save" && req.Action != "delete" {
		writeError(w, 400, "命名视图请求无效或过大")
		return
	}
	if req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "工作簿已切换，视图未保存")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	var view namedView
	if req.Action == "save" {
		name := strings.TrimSpace(req.Name)
		if !validViewName(name) {
			writeError(w, 400, "视图名称须为1至80字，不能包含换行")
			return
		}
		query := filterSpec{HeaderRow: req.HeaderRow, Rules: []workbook.FilterRule{}, SortKeys: []workbook.SortKey{}}
		ruleVersion := 1
		var selected selectionSnapshot
		if req.ViewID != "" {
			meta, ok := wb.FindSheet(req.Sheet)
			if !ok {
				writeError(w, 404, "命名视图的工作表不存在")
				return
			}
			idx := wb.GetCachedIndex(meta.Path)
			if idx == nil {
				writeError(w, 409, "视图来源索引已失效")
				return
			}
			selected, err = a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
			if err != nil {
				writeError(w, 409, err.Error())
				return
			}
			defer selected.close()
			settings, err := selected.visible.data.settings.Normalized(workbook.MaxRowsForIndex(idx))
			if err != nil {
				writeError(w, 400, err.Error())
				return
			}
			req.Config.RowVisibility = &settings
			ruleVersion = 2
			if selected.filter != nil {
				query = selected.filter.Spec
			}
		} else if req.Config.RowVisibility != nil {
			writeError(w, 400, "保存行显隐设置需要当前有效的可见行视图")
			return
		} else if req.FilterID != "" {
			result := a.filterSnapshot(req.FilterID)
			if result == nil || result.Sheet != req.Sheet {
				result.close()
				writeError(w, 409, "当前筛选结果已过期，未保存为无筛选视图")
				return
			}
			query = result.Spec
			result.close()
		}
		binding, idx, err := a.captureViewBinding(r.Context(), wb, req.Sheet, query)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		if selected.visible != nil && selected.visible.data.idx != idx {
			writeError(w, 409, "可见行视图的来源索引已变化")
			return
		}
		if err := validateNamedViewConfig(req.Config, idx.MaxRow); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if req.Config.Table != nil && req.Config.Table.Sticky && wb.Format != "txt" {
			writeError(w, 400, "仅TXT表头可使用字段栏显示")
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		view = namedView{ID: req.ID, Name: name, RuleVersion: ruleVersion, Source: binding, Query: query, Config: req.Config, Created: now, Updated: now}
		if view.ID == "" {
			view.ID = randomID()
		}
		encoded, _ := json.Marshal(view)
		if len(encoded) > maxNamedViewBytes {
			writeError(w, 400, "单个视图超过256KiB，请减少尺寸覆盖或筛选值")
			return
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || a.maintenance {
		writeError(w, 409, "工作簿已切换或正在维护，视图未保存")
		return
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readNamedViews(path, wb.Fingerprint)
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	if store.Revision != *req.ExpectedRevision {
		writeError(w, 409, "视图列表已被其他页面修改，请刷新后重试")
		return
	}
	found := -1
	for i, old := range store.Items {
		if old.ID == req.ID {
			found = i
		}
		if req.Action == "save" && old.ID != req.ID && strings.EqualFold(old.Name, view.Name) {
			writeError(w, 409, "已有同名视图，请更换名称或更新该视图")
			return
		}
	}
	if req.ID != "" && found < 0 {
		writeError(w, 404, "所选视图已移除，请刷新列表")
		return
	}
	if req.Action == "delete" {
		if found < 0 {
			writeError(w, 400, "请选择要删除的视图")
			return
		}
		store.Items = append(store.Items[:found], store.Items[found+1:]...)
	} else if found >= 0 {
		view.Created = store.Items[found].Created
		store.Items[found] = view
	} else {
		if len(store.Items) >= maxNamedViews {
			writeError(w, 400, "最多保存32个命名视图，请先删除不用的视图")
			return
		}
		store.Items = append(store.Items, view)
	}
	store.Revision++
	if store.Revision >= maxConfigRevision {
		writeError(w, 409, "视图修订号已达到上限，原文件已保留")
		return
	}
	if err := writeSavedConfig(path, maxNamedViewsBytes, store, durableConfigOps); err != nil {
		writeError(w, 500, "保存命名视图失败："+err.Error())
		return
	}
	writeJSON(w, map[string]any{"book": req.Book, "store": store, "id": view.ID})
}

func (a *App) handleNamedViewResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		ID          string `json:"id"`
		Book        string `json:"book"`
		WorkbookKey string `json:"workbookKey"`
	}
	if decodeAnalysis(r, &req) != nil {
		writeError(w, 400, "视图参数无效")
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	if req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeError(w, 409, "工作簿已切换，请重新选择视图")
		return
	}
	path, err := namedViewsPath(wb)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	lock := persistentFileLock(path)
	lock.RLock()
	store, err := readNamedViews(path, wb.Fingerprint)
	lock.RUnlock()
	if err != nil {
		writeError(w, 422, err.Error())
		return
	}
	var view *namedView
	for i := range store.Items {
		if store.Items[i].ID == req.ID {
			view = &store.Items[i]
			break
		}
	}
	if view == nil {
		writeError(w, 404, "视图已移除，请刷新列表")
		return
	}
	if view.RuleVersion != 1 && view.RuleVersion != 2 || view.RuleVersion == 1 && view.Config.RowVisibility != nil || view.RuleVersion == 2 && view.Config.RowVisibility == nil {
		writeError(w, 409, "此视图使用未支持的规则版本，未应用")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	current, idx, err := a.captureViewBinding(r.Context(), wb, view.Source.Sheet, view.Query)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	saved := view.Source
	if saved.Fingerprint != current.Fingerprint || saved.Format != current.Format || saved.SheetPath != current.SheetPath || saved.HeaderRow != current.HeaderRow || saved.Columns != current.Columns || len(saved.Fields) != len(current.Fields) {
		writeError(w, 409, "视图来源或字段已变化，未应用")
		return
	}
	for i, field := range saved.Fields {
		if field != current.Fields[i] {
			writeError(w, 409, "视图字段身份不一致，未应用")
			return
		}
	}
	if err := validateNamedViewConfig(view.Config, idx.MaxRow); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if view.Config.RowVisibility != nil {
		meta, _ := wb.FindSheet(current.Sheet)
		layout, err := wb.SourceLayoutContext(r.Context(), meta, idx)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		if _, err := layout.BuildAxisVisibility(r.Context(), "row", workbook.MaxRowsForIndex(idx), *view.Config.RowVisibility); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	var header *workbook.TextTableHeader
	if table := view.Config.Table; table != nil && table.Sticky {
		meta, _ := wb.FindSheet(current.Sheet)
		header, err = wb.ReadTextTableHeader(r.Context(), meta, table.Row)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	if a.currentWorkbook() != wb {
		writeError(w, 409, "工作簿已切换，未应用视图")
		return
	}
	writeJSON(w, map[string]any{"book": req.Book, "view": view, "tableHeader": header})
}
