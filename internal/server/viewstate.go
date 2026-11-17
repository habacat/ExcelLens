package server

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"excellens/internal/workbook"
)

// A separate view-state file is stored beside each workbook's fingerprinted
// index. Nothing is written into the workbook itself.
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

func viewPath(wb *workbook.Workbook) string { return filepath.Join(wb.CacheDir, "view.json") }

func readViews(wb *workbook.Workbook) savedViews {
	v := savedViews{Version: 1, Sheets: make(map[string]freezeSettings)}
	b, err := os.ReadFile(viewPath(wb))
	if err != nil || len(b) > 1<<20 {
		return v
	}
	var stored savedViews
	if json.Unmarshal(b, &stored) != nil || stored.Version != 1 || stored.Sheets == nil {
		return v
	}
	for sheet, p := range stored.Sheets {
		if validateFreeze(p) {
			v.Sheets[sheet] = p
		}
	}
	return v
}

func writeViews(wb *workbook.Workbook, v savedViews) error {
	path := viewPath(wb)
	f, err := os.CreateTemp(wb.CacheDir, ".view-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(v); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return err
		}
		return os.Rename(f.Name(), path)
	}
	return nil
}

func nativeFrozenPane(wb *workbook.Workbook, meta workbook.SheetMeta) freezeSettings {
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
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
	if r.Method == http.MethodPost {
		views := readViews(wb)
		views.Sheets[meta.Name] = requested
		if err := writeViews(wb, views); err != nil {
			writeError(w, http.StatusInternalServerError, "保存冻结设置失败："+err.Error())
			return
		}
		writeJSON(w, map[string]any{"sheet": meta.Name, "rows": requested.Rows, "cols": requested.Cols, "source": "saved"})
		return
	}
	views := readViews(wb)
	if pane, found := views.Sheets[meta.Name]; found {
		writeJSON(w, map[string]any{"sheet": meta.Name, "rows": pane.Rows, "cols": pane.Cols, "source": "saved"})
		return
	}
	pane := nativeFrozenPane(wb, meta)
	writeJSON(w, map[string]any{"sheet": meta.Name, "rows": pane.Rows, "cols": pane.Cols, "source": "original"})
}
