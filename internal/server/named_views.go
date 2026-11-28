package server

import (
	"context"
	"encoding/json"

	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"excellens/internal/formats"
	"excellens/internal/i18n"
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
		return store, i18n.Error("service.namedViews.nameViewVersionSourceOr")
	}
	ids, names := make(map[string]bool), make(map[string]bool)
	for _, view := range store.Items {
		name := strings.ToLower(view.Name)
		encoded, _ := json.Marshal(view)
		if !validSessionID(view.ID) || !validViewName(view.Name) || ids[view.ID] || names[name] || len(encoded) > maxNamedViewBytes || view.Source.Fingerprint != fingerprint {
			return store, i18n.Error("service.namedViews.namedViewStructureInvalidOriginal")
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
		return i18n.Error("service.namedViews.freezingZoomingOrDisplaySettings")
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
				return i18n.Error("service.namedViews.rowColumnDimensionsViewInvalid")
			}
		}
	}
	for key := range config.Layout.ColumnVisibility {
		index, err := strconv.Atoi(key)
		if err != nil || strconv.Itoa(index) != key || index < 1 || index > formats.ExcelColumns {
			return i18n.Error("service.namedViews.hiddenColumnSettingsViewInvalid")
		}
	}
	if table := config.Table; table != nil && (table.Row < 1 || table.Row > maxRow || table.StartCol < 1 || table.EndCol < table.StartCol || table.EndCol > formats.ExcelColumns) {
		return i18n.Error("service.namedViews.headerRangeViewInvalid")
	}
	return nil
}

func (a *App) captureViewBinding(ctx context.Context, wb *workbook.Workbook, sheet string, query filterSpec) (viewSourceBinding, *workbook.SheetIndex, error) {
	meta, ok := wb.FindSheet(sheet)
	if !ok {
		return viewSourceBinding{}, nil, i18n.Error("service.namedViews.namedViewDoesNotExist")
	}
	source, err := a.buildReconcileSource(ctx, wb, reconcileSourceRef{Book: sourceBookID(wb), Source: "wb:" + meta.Name, HeaderRow: query.HeaderRow}, nil)
	if err != nil {
		return viewSourceBinding{}, nil, err
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return viewSourceBinding{}, nil, i18n.Error("service.namedViews.viewSourceIndexInvalidReopen")
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
		writeFailure(w, 500, err)
		return
	}
	if r.Method == http.MethodGet {
		lock := persistentFileLock(path)
		lock.RLock()
		store, err := readNamedViews(path, wb.Fingerprint)
		lock.RUnlock()
		if err != nil {
			writeFailure(w, 422, err)
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
		writeMessage(w, 400, "service.namedViews.nameViewRequestInvalidOr")
		return
	}
	if req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.namedViews.workbookSwitchedViewNotSaved")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	var view namedView
	if req.Action == "save" {
		name := strings.TrimSpace(req.Name)
		if !validViewName(name) {
			writeMessage(w, 400, "service.namedViews.viewNameMustContainCharacters")
			return
		}
		query := filterSpec{HeaderRow: req.HeaderRow, Rules: []workbook.FilterRule{}, SortKeys: []workbook.SortKey{}}
		ruleVersion := 1
		var selected selectionSnapshot
		if req.ViewID != "" {
			meta, ok := wb.FindSheet(req.Sheet)
			if !ok {
				writeMessage(w, 404, "service.namedViews.namedViewDoesNotExist")
				return
			}
			idx := wb.GetCachedIndex(meta.Path)
			if idx == nil {
				writeMessage(w, 409, "service.namedViews.viewSourceIndexInvalid")
				return
			}
			selected, err = a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
			if err != nil {
				writeFailure(w, 409, err)
				return
			}
			defer selected.close()
			settings, err := selected.visible.data.settings.Normalized(workbook.MaxRowsForIndex(idx))
			if err != nil {
				writeFailure(w, 400, err)
				return
			}
			req.Config.RowVisibility = &settings
			ruleVersion = 2
			if selected.filter != nil {
				query = selected.filter.Spec
			}
		} else if req.Config.RowVisibility != nil {
			writeMessage(w, 400, "service.namedViews.settingWhichRowsDisplayNeeds")
			return
		} else if req.FilterID != "" {
			result := a.filterSnapshot(req.FilterID)
			if result == nil || result.Sheet != req.Sheet {
				result.close()
				writeMessage(w, 409, "service.namedViews.currentFilterResultsHaveExpired")
				return
			}
			query = result.Spec
			result.close()
		}
		binding, idx, err := a.captureViewBinding(r.Context(), wb, req.Sheet, query)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		if selected.visible != nil && selected.visible.data.idx != idx {
			writeMessage(w, 409, "service.namedViews.sourceIndexVisibleRowView")
			return
		}
		if err := validateNamedViewConfig(req.Config, idx.MaxRow); err != nil {
			writeFailure(w, 400, err)
			return
		}
		if req.Config.Table != nil && req.Config.Table.Sticky && wb.Format != "txt" {
			writeMessage(w, 400, "service.namedViews.fieldColumnsCanOnlyBe")
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		view = namedView{ID: req.ID, Name: name, RuleVersion: ruleVersion, Source: binding, Query: query, Config: req.Config, Created: now, Updated: now}
		if view.ID == "" {
			view.ID = randomID()
		}
		encoded, _ := json.Marshal(view)
		if len(encoded) > maxNamedViewBytes {
			writeMessage(w, 400, "service.namedViews.singleViewExceedsKibReduce")
			return
		}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.wb != wb || a.maintenance {
		writeMessage(w, 409, "service.namedViews.workbookSwitchedOrUnderMaintenance")
		return
	}
	lock := persistentFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	store, err := readNamedViews(path, wb.Fingerprint)
	if err != nil {
		writeFailure(w, 422, err)
		return
	}
	if store.Revision != *req.ExpectedRevision {
		writeMessage(w, 409, "service.namedViews.viewListModifiedAnotherPage")
		return
	}
	found := -1
	for i, old := range store.Items {
		if old.ID == req.ID {
			found = i
		}
		if req.Action == "save" && old.ID != req.ID && strings.EqualFold(old.Name, view.Name) {
			writeMessage(w, 409, "service.namedViews.thereAlreadyViewSameName")
			return
		}
	}
	if req.ID != "" && found < 0 {
		writeMessage(w, 404, "service.namedViews.selectedViewRemovedRefreshList")
		return
	}
	if req.Action == "delete" {
		if found < 0 {
			writeMessage(w, 400, "service.namedViews.selectViewBeDeleted")
			return
		}
		store.Items = append(store.Items[:found], store.Items[found+1:]...)
	} else if found >= 0 {
		view.Created = store.Items[found].Created
		store.Items[found] = view
	} else {
		if len(store.Items) >= maxNamedViews {
			writeMessage(w, 400, "service.namedViews.upNamedViewsCanBe")
			return
		}
		store.Items = append(store.Items, view)
	}
	store.Revision++
	if store.Revision >= maxConfigRevision {
		writeMessage(w, 409, "service.namedViews.viewRevisionNumberReachedLimit")
		return
	}
	if err := writeSavedConfig(path, maxNamedViewsBytes, store, durableConfigOps); err != nil {
		writeMessage(w, 500, "service.namedViews.failedSaveNamedView", i18n.Args{"detail": err})
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
		writeMessage(w, 400, "service.namedViews.viewParametersInvalid")
		return
	}
	wb := requireAnalysisWorkbook(a, w)
	if wb == nil {
		return
	}
	if req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.namedViews.workbookSwitchedReselectView")
		return
	}
	path, err := namedViewsPath(wb)
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	lock := persistentFileLock(path)
	lock.RLock()
	store, err := readNamedViews(path, wb.Fingerprint)
	lock.RUnlock()
	if err != nil {
		writeFailure(w, 422, err)
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
		writeMessage(w, 404, "service.namedViews.viewRemovedRefreshList")
		return
	}
	if view.RuleVersion != 1 && view.RuleVersion != 2 || view.RuleVersion == 1 && view.Config.RowVisibility != nil || view.RuleVersion == 2 && view.Config.RowVisibility == nil {
		writeMessage(w, 409, "service.namedViews.thisViewUsesUnsupportedRule")
		return
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	current, idx, err := a.captureViewBinding(r.Context(), wb, view.Source.Sheet, view.Query)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	saved := view.Source
	if saved.Fingerprint != current.Fingerprint || saved.Format != current.Format || saved.SheetPath != current.SheetPath || saved.HeaderRow != current.HeaderRow || saved.Columns != current.Columns || len(saved.Fields) != len(current.Fields) {
		writeMessage(w, 409, "service.namedViews.viewSourceOrFieldsHave")
		return
	}
	for i, field := range saved.Fields {
		if field != current.Fields[i] {
			writeMessage(w, 409, "service.namedViews.viewFieldIdentitiesInconsistentNot")
			return
		}
	}
	if err := validateNamedViewConfig(view.Config, idx.MaxRow); err != nil {
		writeFailure(w, 409, err)
		return
	}
	if view.Config.RowVisibility != nil {
		meta, _ := wb.FindSheet(current.Sheet)
		layout, err := wb.SourceLayoutContext(r.Context(), meta, idx)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		if _, err := layout.BuildAxisVisibility(r.Context(), "row", workbook.MaxRowsForIndex(idx), *view.Config.RowVisibility); err != nil {
			writeFailure(w, 409, err)
			return
		}
	}
	var header *workbook.TextTableHeader
	if table := view.Config.Table; table != nil && table.Sticky {
		meta, _ := wb.FindSheet(current.Sheet)
		header, err = wb.ReadTextTableHeader(r.Context(), meta, table.Row)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
	}
	if a.currentWorkbook() != wb {
		writeMessage(w, 409, "service.namedViews.workbookSwitchedViewNotApplied")
		return
	}
	writeJSON(w, map[string]any{"book": req.Book, "view": view, "tableHeader": header})
}
