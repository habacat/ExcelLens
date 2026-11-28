package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

// Visual row/column adjustments belong to a workbook fingerprint, not its
// filename. This file never changes the original Excel document.
type sheetLayout struct {
	Rows                     map[string]int               `json:"rows"`
	Cols                     map[string]int               `json:"cols"`
	Font                     string                       `json:"font,omitempty"`
	ColumnMode               string                       `json:"columnMode,omitempty"`
	ColumnVisibility         map[string]bool              `json:"columnVisibility,omitempty"`
	SourceHidden             []workbook.HiddenColumnRange `json:"sourceHidden,omitempty"`
	SourceMerges             []workbook.CellMerge         `json:"sourceMerges,omitempty"`
	SourceRows               []workbook.SourceSizeRange   `json:"sourceRows,omitempty"`
	SourceCols               map[int]float64              `json:"sourceCols,omitempty"`
	SourceGridLines          *bool                        `json:"sourceGridLines,omitempty"`
	SourceDefaultRow         float64                      `json:"sourceDefaultRow,omitempty"`
	SourceDefaultCol         float64                      `json:"sourceDefaultCol,omitempty"`
	SourceRowStates          []workbook.SourceAxisRange   `json:"sourceRowStates,omitempty"`
	SourceColStates          []workbook.SourceAxisRange   `json:"sourceColStates,omitempty"`
	SourceZeroHeight         bool                         `json:"sourceZeroHeight,omitempty"`
	SourceDefaultRowZeroSize bool                         `json:"sourceDefaultRowZeroSize,omitempty"`
	SourceDefaultColZeroSize bool                         `json:"sourceDefaultColZeroSize,omitempty"`
	SourceSummaryBelow       *bool                        `json:"sourceSummaryBelow,omitempty"`
	SourceSummaryRight       *bool                        `json:"sourceSummaryRight,omitempty"`
	SourceStateNotice        string                       `json:"sourceStateNotice,omitempty"`
}
type workbookLayouts struct {
	Version int                    `json:"version"`
	Sheets  map[string]sheetLayout `json:"sheets"`
}

func readLayouts(path string) (workbookLayouts, error) {
	out := workbookLayouts{Version: 1, Sheets: make(map[string]sheetLayout)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if len(b) > 2<<20 {
		return out, i18n.Error("service.layout.localLayoutFileExceedsMaximum")
	}
	var found workbookLayouts
	if json.Unmarshal(b, &found) != nil || found.Version != 1 || found.Sheets == nil {
		return out, i18n.Error("service.layout.localLayoutFileDamagedOr")
	}
	for name, layout := range found.Sheets {
		clean := sheetLayout{Rows: make(map[string]int), Cols: make(map[string]int), ColumnVisibility: make(map[string]bool)}
		if validColumnMode(layout.ColumnMode) {
			clean.ColumnMode = layout.ColumnMode
		}
		for key, hidden := range layout.ColumnVisibility {
			i, err := strconv.Atoi(key)
			if err == nil && i >= 1 && i <= formats.ExcelColumns && len(clean.ColumnVisibility) < formats.ExcelColumns {
				clean.ColumnVisibility[key] = hidden
			}
		}
		if validFontOverride(layout.Font) {
			clean.Font = layout.Font
		}
		for kind, source := range map[string]map[string]int{"row": layout.Rows, "col": layout.Cols} {
			for k, v := range source {
				n, err := strconv.Atoi(k)
				if err != nil || !validLayoutSize(kind, n, v) {
					continue
				}
				if kind == "row" {
					clean.Rows[k] = v
				} else {
					clean.Cols[k] = v
				}
			}
		}
		out.Sheets[name] = clean
	}
	return out, nil
}

// Only known platform fonts are accepted as user display overrides. An empty
// name means "respect each cell's original Excel font".
func validColumnMode(mode string) bool {
	return mode == "" || mode == "original" || mode == "show-all" || mode == "hide-all"
}
func validVisibilityAction(mode string) bool {
	return mode == "column" || mode == "range" || mode == "original" || mode == "show-all" || mode == "hide-all"
}

func validFontOverride(font string) bool {
	switch font {
	case "", "宋体", "新宋体", "楷体", "仿宋", "微软雅黑", "等线", "黑体", "华文宋体", "华文楷体", "华文仿宋", "隶书", "Arial", "Calibri", "Consolas":
		return true
	}
	return false
}

func validLayoutSize(kind string, index, value int) bool {
	if value == 0 {
		return index >= 1 && ((kind == "row" && index <= formats.ExternalRows) || (kind == "col" && index <= formats.ExcelColumns))
	}
	if kind == "row" {
		return index >= 1 && index <= formats.ExternalRows && value >= 14 && value <= 500
	}
	return kind == "col" && index >= 1 && index <= formats.ExcelColumns && value >= 32 && value <= 800
}
func writeWorkbookSetting(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".layout-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = json.NewEncoder(f).Encode(v); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (a *App) handleLayout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeMessage(w, http.StatusConflict, "service.layout.noWorkbookOpen")
		return
	}
	if key := r.Header.Get("X-ExcelLens-Workbook"); key != "" && key != wb.Fingerprint {
		writeMessage(w, http.StatusConflict, "service.layout.workbookSwitchedAdjustDisplaySettings")
		return
	}
	sheet := r.URL.Query().Get("sheet")
	var input struct {
		Sheet  string `json:"sheet"`
		Kind   string `json:"kind"`
		Index  int    `json:"index"`
		End    int    `json:"end"`
		Size   int    `json:"size"`
		Font   string `json:"font"`
		Mode   string `json:"mode"`
		Hidden bool   `json:"hidden"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&input); err != nil {
			writeMessage(w, 400, "service.layout.invalidSizeSettings")
			return
		}
		sheet = input.Sheet
		source, ok := formats.Lookup(wb.Format)
		if !ok {
			writeMessage(w, 400, "service.layout.workbookSourceFormatInvalid")
			return
		}
		if (input.Kind == "font" && !validFontOverride(input.Font)) ||
			(input.Kind == "visibility" && (!validVisibilityAction(input.Mode) ||
				(input.Mode == "column" && (input.Index < 1 || input.Index > source.MaxColumns)) ||
				(input.Mode == "range" && (input.Index < 1 || input.End < input.Index || input.End > source.MaxColumns)))) ||
			(input.Kind == "col" && input.Index > source.MaxColumns) ||
			(input.Kind == "row" && input.Index > source.MaxRows) ||
			(input.Kind != "font" && input.Kind != "visibility" && !validLayoutSize(input.Kind, input.Index, input.Size)) {
			writeMessage(w, 400, "service.layout.displaySettingsNotWithinAllowed")
			return
		}
	}
	meta, ok := wb.FindSheet(sheet)
	if !ok {
		writeMessage(w, 404, "service.layout.worksheetDoesNotExist")
		return
	}
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	if a.currentWorkbook() != wb {
		writeMessage(w, 409, "service.layout.workbookSwitched")
		return
	}
	path, unlock, err := lockWorkbookSetting(r.Context(), "layouts", wb.Fingerprint)
	if err != nil {
		writeFailure(w, 500, err)
		return
	}
	defer unlock()
	all, err := readLayouts(path)
	if err != nil {
		writeMessage(w, 500, "service.layout.failedReadLayout", i18n.Args{"detail": err})
		return
	}
	layout := all.Sheets[meta.Name]
	if layout.Rows == nil {
		layout.Rows = make(map[string]int)
	}
	if layout.Cols == nil {
		layout.Cols = make(map[string]int)
	}
	if layout.ColumnVisibility == nil {
		layout.ColumnVisibility = make(map[string]bool)
	}
	if r.Method == http.MethodPost {
		if input.Kind == "visibility" {
			if input.Mode == "column" || input.Mode == "range" {
				end := input.Index
				if input.Mode == "range" {
					end = input.End
				}
				// A single atomic layout update for every selected column. The
				// range has already been checked against the source format.
				for col := input.Index; col <= end; col++ {
					layout.ColumnVisibility[strconv.Itoa(col)] = input.Hidden
				}
			} else {
				layout.ColumnMode = input.Mode
				clear(layout.ColumnVisibility)
			}
			all.Sheets[meta.Name] = layout
			if err := writeWorkbookSetting(path, all); err != nil {
				writeMessage(w, 500, "service.layout.failedSaveColumnDisplaySettings", i18n.Args{"detail": err})
				return
			}
			writeJSON(w, layout)
			return
		}
		if input.Kind == "font" {
			layout.Font = input.Font
			all.Sheets[meta.Name] = layout
			if err := writeWorkbookSetting(path, all); err != nil {
				writeMessage(w, 500, "service.layout.failedSetFontDisplaySaved", i18n.Args{"detail": err})
				return
			}
			writeJSON(w, layout)
			return
		}
		var target map[string]int
		if input.Kind == "row" {
			target = layout.Rows
		} else {
			target = layout.Cols
		}
		key := strconv.Itoa(input.Index)
		if input.Size == 0 {
			delete(target, key)
		} else {
			target[key] = input.Size
		}
		if len(layout.Rows)+len(layout.Cols) > 3000 {
			writeMessage(w, 400, "service.layout.sizeSingleWorksheetExceedsLimit")
			return
		}
		all.Sheets[meta.Name] = layout
		if err := writeWorkbookSetting(path, all); err != nil {
			writeMessage(w, 500, "service.layout.failedSaveRowColumnSize", i18n.Args{"detail": err})
			return
		}
	}
	if r.Method == http.MethodGet {
		idx := wb.GetCachedIndex(meta.Path)
		source, err := wb.SourceLayoutContext(r.Context(), meta, idx)
		if err != nil {
			writeMessage(w, 500, "service.layout.failedReadOriginalRowColumn", i18n.Args{"detail": err})
			return
		}
		layout.SourceRows, layout.SourceCols, layout.SourceGridLines = source.Rows, source.Cols, source.GridLines
		layout.SourceMerges = source.Merges
		layout.SourceDefaultRow, layout.SourceDefaultCol = source.DefaultRow, source.DefaultCol
		layout.SourceRowStates, layout.SourceColStates = source.RowStates, source.ColStates
		layout.SourceZeroHeight = source.ZeroHeight
		layout.SourceDefaultRowZeroSize, layout.SourceDefaultColZeroSize = source.DefaultRowZeroSize, source.DefaultColZeroSize
		layout.SourceSummaryBelow, layout.SourceSummaryRight = source.SummaryBelow, source.SummaryRight
		layout.SourceStateNotice = source.StateNotice
		if idx != nil {
			ranges, err := wb.SourceHiddenColumns(meta, idx)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				writeMessage(w, 500, "service.layout.failedReadOriginalColumnDisplay", i18n.Args{"detail": err})
				return
			}
			layout.SourceHidden = ranges
		}
	}
	writeJSON(w, layout)
}
