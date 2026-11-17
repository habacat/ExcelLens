package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// A clipboard selection is bounded independently of the size of the workbook.
// Filter selections use *visual* indexes: sort order, hidden source rows and
// filtered-out records must not leak into what the user copies.
const (
	clipboardMaxRows  = formats.ClipboardRows
	clipboardMaxCols  = formats.ClipboardColumns
	clipboardMaxCells = formats.ClipboardCells
)

type clipboardRow struct {
	Row                int                 `json:"row"`
	Style              int                 `json:"style,omitempty"`
	Cells              []workbook.CellData `json:"cells"`
	PresentationNotice string              `json:"presentationNotice,omitempty"`
}

type clipboardPayload struct {
	WorkbookKey string         `json:"workbookKey,omitempty"`
	Book        string         `json:"book,omitempty"`
	Sheet       string         `json:"sheet,omitempty"`
	StartCol    int            `json:"startCol"`
	EndCol      int            `json:"endCol"`
	Filtered    bool           `json:"filtered"`
	Rows        []clipboardRow `json:"rows"`
	Header      *clipboardRow  `json:"header,omitempty"`
}

func selectClipboardRows(idx *workbook.SheetIndex, filtered *FilterResult, row1, row2, visual1, visual2, excludedThrough int, views ...*sheetRowView) ([]int, []int, error) {
	numbers, err := sourceSelectionRows(idx, filtered, row1, row2, visual1, visual2, excludedThrough, clipboardMaxRows, views...)
	if errors.Is(err, errSelectionLimit) {
		return nil, nil, errors.New("每次最多复制250行")
	}
	if err != nil || filtered == nil {
		return numbers, nil, err
	}
	positions := make([]int, 0, len(numbers))
	for _, number := range numbers {
		pos := idx.FindRowOffset(number)
		if pos < idx.RowCount() && idx.RowAt(pos).Number == number {
			positions = append(positions, pos)
		}
	}
	return numbers, positions, nil
}

func escapedClipboardCell(value string) string {
	// Excel accepts quoted cells for literal tabs, line breaks and quotes.
	if strings.ContainsAny(value, "\t\r\n\"") {
		return "\"" + strings.ReplaceAll(value, "\"", "\"\"") + "\""
	}
	return value
}

func (a *App) handleRange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	wb := a.currentWorkbook()
	if wb == nil {
		writeError(w, http.StatusConflict, "尚未打开工作簿")
		return
	}
	if r.URL.Query().Has("workbookKey") && r.URL.Query().Get("workbookKey") != wb.Fingerprint ||
		r.URL.Query().Has("book") && r.URL.Query().Get("book") != sourceBookID(wb) {
		writeError(w, http.StatusConflict, "复制来源已变化，请重新选择")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeError(w, http.StatusNotFound, "指定工作表不存在")
		return
	}
	col1, row1, err1 := workbook.ParseCellRef(r.URL.Query().Get("start"))
	col2, row2, err2 := workbook.ParseCellRef(r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil {
		writeError(w, http.StatusBadRequest, "选区地址无效")
		return
	}
	if col1 > col2 {
		col1, col2 = col2, col1
	}
	colCount := col2 - col1 + 1
	if colCount > clipboardMaxCols {
		writeError(w, http.StatusBadRequest, "一次最多复制50列；请缩小选区")
		return
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" {
		writeError(w, http.StatusBadRequest, "复制格式不受支持")
		return
	}
	filterID := strings.TrimSpace(r.URL.Query().Get("filterId"))
	viewID := r.URL.Query().Get("viewId")
	var filtered *FilterResult
	if filterID != "" && viewID == "" {
		filtered = a.filterSnapshot(filterID)
		defer filtered.close()
		valid := a.currentWorkbook() == wb && filtered != nil && filtered.SheetPath == meta.Path
		if !valid {
			writeError(w, http.StatusNotFound, "筛选结果已失效，请重新筛选")
			return
		}
	}
	if filtered == nil && viewID == "" && (r.URL.Query().Has("visualStart") || r.URL.Query().Has("visualEnd")) {
		writeError(w, http.StatusBadRequest, "缺少筛选结果标识")
		return
	}
	visual1, error1 := strconv.Atoi(r.URL.Query().Get("visualStart"))
	visual2, error2 := strconv.Atoi(r.URL.Query().Get("visualEnd"))
	through := 0
	if filterID != "" {
		if error1 != nil || error2 != nil {
			writeError(w, http.StatusBadRequest, "筛选选区必须指定可视行位置")
			return
		}
		var err error
		through, err = strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
		if err != nil || through < 0 || through > 30 {
			writeError(w, http.StatusBadRequest, "冻结行参数无效")
			return
		}
	}

	if viewID != "" && filterID == "" && r.URL.Query().Has("excludeThroughRow") {
		var err error
		through, err = strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
		if err != nil || through < 0 || through > 30 {
			writeError(w, 400, "冻结行参数无效")
			return
		}
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if a.currentWorkbook() != wb {
		writeError(w, http.StatusConflict, "工作簿已切换")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeError(w, http.StatusConflict, "请先打开工作表")
		return
	}
	if row1 > workbook.MaxRowsForIndex(idx) || row2 > workbook.MaxRowsForIndex(idx) {
		writeError(w, http.StatusBadRequest, "复制地址超出源格式范围")
		return
	}
	headerRow := 0
	if r.URL.Query().Has("headerRow") {
		var err error
		headerRow, err = strconv.Atoi(r.URL.Query().Get("headerRow"))
		if err != nil || headerRow < 1 || headerRow > workbook.MaxRowsForIndex(idx) || format != "json" {
			writeError(w, http.StatusBadRequest, "复制表头行无效")
			return
		}
	}
	var visible *sheetRowView
	if viewID != "" {
		visible, err1 = a.rowViewSnapshot(viewID, wb, idx, meta.Path, filterID)
		if err1 != nil {
			writeError(w, 409, err1.Error())
			return
		}
		defer visible.close()
	}
	numbers, _, err := selectClipboardRows(idx, filtered, row1, row2, visual1, visual2, through, visible)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	readNumbers := append([]int(nil), numbers...)
	if headerRow > 0 {
		found := false
		for _, number := range numbers {
			found = found || number == headerRow
		}
		if !found {
			readNumbers = append(readNumbers, headerRow)
		}
	}
	if len(readNumbers) > clipboardMaxRows || len(readNumbers)*colCount > clipboardMaxCells {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("复制区域（含表头）最多%d行、%d列、%d格，请缩小选区", clipboardMaxRows, clipboardMaxCols, clipboardMaxCells))
		return
	}
	readOptions := workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: format == "json" && r.URL.Query().Get("richText") != "0"}
	readOptions.ConditionalFormats = format == "json" && r.URL.Query().Get("conditional") != "0" && (readOptions.RichText || r.URL.Query().Get("conditional") == "1")
	byRow := make(map[int]workbook.RowData, len(readNumbers))
	textBytes, styleBytes := 0, 0
	tooLarge := false
	err = wb.VisitRowsByNumbersWithOptionsContext(r.Context(), idx, func(yield func(int) bool) {
		for _, number := range readNumbers {
			if !yield(number) {
				return
			}
		}
	}, col1, col2, readOptions, func(row workbook.RowData) error {
		for _, cell := range row.Cells {
			textBytes += len(cell.Raw) + len(cell.Display) + len(cell.Formula)
			if cell.Conditional != nil && cell.Conditional.Display != nil {
				textBytes += len(*cell.Conditional.Display)
			}
			if textBytes > formats.ClipboardTextBytes {
				tooLarge = true
				return errors.New("复制文字超过4MiB，请缩小选区或使用导出")
			}
			if cell.RichText != nil || cell.Conditional != nil {
				encoded, err := json.Marshal(struct {
					Rich        *workbook.RichText
					Conditional *workbook.ConditionalPresentation
				}{cell.RichText, cell.Conditional})
				if err != nil {
					return err
				}
				styleBytes += len(encoded)
				if styleBytes > formats.RichReadBytes {
					tooLarge = true
					return errors.New("复制样式超过本次容量，请缩小选区或关闭原始格式")
				}
			}
		}
		byRow[row.Number] = row
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if tooLarge {
			writeError(w, http.StatusRequestEntityTooLarge, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "读取选区失败："+err.Error())
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-ExcelLens-Range", strconv.Itoa(len(numbers)*colCount))
	if format == "json" {
		payload := clipboardPayload{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Sheet: meta.Name, StartCol: col1, EndCol: col2, Filtered: filterID != "", Rows: make([]clipboardRow, 0, len(numbers))}
		if headerRow > 0 {
			data := byRow[headerRow]
			payload.Header = &clipboardRow{Row: headerRow, Style: data.Style, Cells: data.Cells, PresentationNotice: data.PresentationNotice}
		}
		for _, number := range numbers {
			data := byRow[number]
			payload.Rows = append(payload.Rows, clipboardRow{Row: number, Style: data.Style, Cells: data.Cells, PresentationNotice: data.PresentationNotice})
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(payload)
		return
	}
	mode := r.URL.Query().Get("mode")
	var out strings.Builder
	for i, number := range numbers {
		if i > 0 {
			out.WriteByte('\n')
		}
		columns := make(map[int]workbook.CellData, len(byRow[number].Cells))
		for _, cell := range byRow[number].Cells {
			columns[cell.Col] = cell
		}
		for col := col1; col <= col2; col++ {
			if col > col1 {
				out.WriteByte('\t')
			}
			cell := columns[col]
			value := cell.Display
			if mode == "formula" && cell.HasFormula {
				value = cell.Formula
			}
			out.WriteString(escapedClipboardCell(value))
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(out.String()))
}
