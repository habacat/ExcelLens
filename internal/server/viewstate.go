package server

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"

	"excellens/internal/workbook"
)

// Freeze overrides belong to the workbook fingerprint in the user profile.
type freezeSettings struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}
type savedViews struct {
	Version int                       `json:"version"`
	Sheets  map[string]freezeSettings `json:"sheets"`
}

const maxFrozenRows = 30
const maxFrozenCols = 12

func validateFreeze(f freezeSettings) bool {
	return f.Rows >= 0 && f.Rows <= maxFrozenRows && f.Cols >= 0 && f.Cols <= maxFrozenCols
}

func readViews(path string) (savedViews, error) {
	v := savedViews{Version: 1, Sheets: make(map[string]freezeSettings)}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	var stored savedViews
	if len(b) > 1<<20 || json.Unmarshal(b, &stored) != nil || stored.Version != 1 || stored.Sheets == nil {
		return v, errors.New("冻结布局文件损坏或版本不兼容")
	}
	for sheet, p := range stored.Sheets {
		if validateFreeze(p) {
			v.Sheets[sheet] = p
		}
	}
	return v, nil
}

func nativeFrozenPane(wb *workbook.Workbook, meta workbook.SheetMeta) freezeSettings {
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		return freezeSettings{}
	}
	if wb.Format == "xlsb" {
		layout, err := wb.SourceLayout(meta, idx)
		freeze := freezeSettings{Rows: layout.FrozenRows, Cols: layout.FrozenCols}
		if err == nil && validateFreeze(freeze) {
			return freeze
		}
		return freezeSettings{}
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		return freezeSettings{}
	}
	defer f.Close()
	dec := xml.NewDecoder(io.LimitReader(f, 512<<10))
	for {
		token, err := dec.Token()
		if err != nil {
			return freezeSettings{}
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "sheetData" {
			return freezeSettings{}
		}
		if start.Name.Local != "pane" {
			continue
		}
		var p freezeSettings
		frozen := false
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "state":
				frozen = strings.HasPrefix(attr.Value, "frozen")
			case "xSplit":
				n, _ := strconv.ParseFloat(attr.Value, 64)
				p.Cols = int(math.Round(n))
			case "ySplit":
				n, _ := strconv.ParseFloat(attr.Value, 64)
				p.Rows = int(math.Round(n))
			}
		}
		if frozen && validateFreeze(p) {
			return p
		}
		return freezeSettings{}
	}
}

func (a *App) handleView(w http.ResponseWriter, r *http.Request) {
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	var sheet string
	var requested freezeSettings
	if r.Method == http.MethodGet {
		sheet = r.URL.Query().Get("sheet")
	} else if r.Method == http.MethodPost {
		var input struct {
			Sheet string `json:"sheet"`
			Rows  int    `json:"rows"`
			Cols  int    `json:"cols"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "冻结设置无效")
			return
		}
		sheet = input.Sheet
		requested = freezeSettings{Rows: input.Rows, Cols: input.Cols}
		if !validateFreeze(requested) {
			writeError(w, http.StatusBadRequest, "冻结行数最多30、列数最多12，不能为负数")
			return
		}
	} else {
		methodNotAllowed(w)
		return
	}
	meta, ok := wb.FindSheet(sheet)
	if !ok {
		writeError(w, http.StatusNotFound, "找不到指定工作表")
		return
	}
	a.viewMu.Lock()
	defer a.viewMu.Unlock()
	if a.currentWorkbook() != wb {
		writeError(w, http.StatusConflict, "工作簿已切换")
		return
	}
	path, unlock, err := lockWorkbookSetting(r.Context(), "frozen-panes", wb.Fingerprint)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	defer unlock()
	views, err := readViews(path)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if r.Method == http.MethodPost {
		views.Sheets[meta.Name] = requested
		if err := writeWorkbookSetting(path, views); err != nil {
			writeError(w, http.StatusInternalServerError, "保存冻结设置失败："+err.Error())
			return
		}
		writeJSON(w, map[string]any{"sheet": meta.Name, "rows": requested.Rows, "cols": requested.Cols, "source": "saved"})
		return
	}
	if pane, found := views.Sheets[meta.Name]; found {
		writeJSON(w, map[string]any{"sheet": meta.Name, "rows": pane.Rows, "cols": pane.Cols, "source": "saved"})
		return
	}
	pane := nativeFrozenPane(wb, meta)
	writeJSON(w, map[string]any{"sheet": meta.Name, "rows": pane.Rows, "cols": pane.Cols, "source": "original"})
}
