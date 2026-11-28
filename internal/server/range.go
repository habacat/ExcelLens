package server

import (
	"context"
	"encoding/json"
	"errors"

	"io"
	"net/http"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/i18n"
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
	Portable       *portableClipboard `json:"portable,omitempty"`
	PortableNotice string             `json:"portableNotice,omitempty"`
	WorkbookKey    string             `json:"workbookKey,omitempty"`
	Book           string             `json:"book,omitempty"`
	Sheet          string             `json:"sheet,omitempty"`
	StartCol       int                `json:"startCol"`
	EndCol         int                `json:"endCol"`
	Filtered       bool               `json:"filtered"`
	Rows           []clipboardRow     `json:"rows"`
	Header         *clipboardRow      `json:"header,omitempty"`
}

func selectClipboardRows(idx *workbook.SheetIndex, filtered *FilterResult, row1, row2, visual1, visual2, excludedThrough int, views ...*sheetRowView) ([]int, []int, error) {
	numbers, err := sourceSelectionRows(idx, filtered, row1, row2, visual1, visual2, excludedThrough, clipboardMaxRows, views...)
	if errors.Is(err, errSelectionLimit) {
		return nil, nil, i18n.Error("service.range.upRowsCanBeCopied")
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
		writeMessage(w, http.StatusConflict, "service.range.noWorkbookOpen")
		return
	}
	if r.URL.Query().Has("workbookKey") && r.URL.Query().Get("workbookKey") != wb.Fingerprint ||
		r.URL.Query().Has("book") && r.URL.Query().Get("book") != sourceBookID(wb) {
		writeMessage(w, http.StatusConflict, "service.range.sourceChangedReselect")
		return
	}
	meta, ok := wb.FindSheet(r.URL.Query().Get("sheet"))
	if !ok {
		writeMessage(w, http.StatusNotFound, "service.range.specifiedWorksheetDoesNotExist")
		return
	}
	col1, row1, err1 := workbook.ParseCellRef(r.URL.Query().Get("start"))
	col2, row2, err2 := workbook.ParseCellRef(r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil {
		writeMessage(w, http.StatusBadRequest, "service.range.selectedConstituencyAddressInvalid")
		return
	}
	if col1 > col2 {
		col1, col2 = col2, col1
	}
	colCount := col2 - col1 + 1
	format := r.URL.Query().Get("format")
	if format != "tsv" && colCount > clipboardMaxCols {
		writeMessage(w, http.StatusBadRequest, "service.range.upColumnsCanBeCopied")
		return
	}
	if format != "" && format != "json" && format != "tsv" {
		writeMessage(w, http.StatusBadRequest, "service.range.copyFormatNotSupported")
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
			writeMessage(w, http.StatusNotFound, "service.filter.expired")
			return
		}
	}
	if filtered == nil && viewID == "" && (r.URL.Query().Has("visualStart") || r.URL.Query().Has("visualEnd")) {
		writeMessage(w, http.StatusBadRequest, "service.range.missingFilterResultIndicator")
		return
	}
	visual1, error1 := strconv.Atoi(r.URL.Query().Get("visualStart"))
	visual2, error2 := strconv.Atoi(r.URL.Query().Get("visualEnd"))
	through := 0
	if filterID != "" {
		if error1 != nil || error2 != nil {
			writeMessage(w, http.StatusBadRequest, "service.range.filterSelectionMustSpecifyVisible")
			return
		}
		var err error
		through, err = strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
		if err != nil || through < 0 || through > 30 {
			writeMessage(w, http.StatusBadRequest, "service.range.frozenRowParametersInvalid")
			return
		}
	}

	if viewID != "" && filterID == "" && r.URL.Query().Has("excludeThroughRow") {
		var err error
		through, err = strconv.Atoi(r.URL.Query().Get("excludeThroughRow"))
		if err != nil || through < 0 || through > 30 {
			writeMessage(w, 400, "service.range.frozenRowParametersInvalid")
			return
		}
	}
	if format == "tsv" {
		release, err := a.acquireQuerySlot(r.Context())
		if err != nil {
			writeMessage(w, http.StatusRequestTimeout, "service.range.copiedCanceled")
			return
		}
		defer release()
	}
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	if a.currentWorkbook() != wb {
		writeMessage(w, http.StatusConflict, "service.range.workbookSwitched")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, http.StatusConflict, "service.range.openWorksheetFirst")
		return
	}
	if row1 > workbook.MaxRowsForIndex(idx) || row2 > workbook.MaxRowsForIndex(idx) {
		writeMessage(w, http.StatusBadRequest, "service.range.copiedAddressExceedsSourceFormat")
		return
	}
	headerRow := 0
	if r.URL.Query().Has("headerRow") {
		var err error
		headerRow, err = strconv.Atoi(r.URL.Query().Get("headerRow"))
		if err != nil || headerRow < 1 || headerRow > workbook.MaxRowsForIndex(idx) || format != "json" {
			writeMessage(w, http.StatusBadRequest, "service.range.copyingHeaderRowsInvalid")
			return
		}
	}
	var visible *sheetRowView
	if viewID != "" {
		visible, err1 = a.rowViewSnapshot(viewID, wb, idx, meta.Path, filterID)
		if err1 != nil {
			writeFailure(w, 409, err1)
			return
		}
		defer visible.close()
	}
	if format == "tsv" {
		view, err := newSelectionRowView(idx, filtered, through, visible)
		if err != nil {
			writeFailure(w, 409, err)
			return
		}
		plan, err := view.plan(row1, row2, visual1, visual2)
		if err != nil {
			writeFailure(w, 400, err)
			return
		}
		writeClipboardTSV(w, r, wb, plan, col1, col2)
		return
	}
	numbers, _, err := selectClipboardRows(idx, filtered, row1, row2, visual1, visual2, through, visible)
	if err != nil {
		writeFailure(w, http.StatusBadRequest, err)
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
		writeMessage(w, http.StatusBadRequest, "service.range.copiedAreaIncludingTableHeader", i18n.Args{"clipboardMaxRows": clipboardMaxRows, "clipboardMaxCols": clipboardMaxCols, "clipboardMaxCells": clipboardMaxCells})
		return
	}
	readOptions := workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: format == "json" && r.URL.Query().Get("richText") != "0"}
	readOptions.ConditionalFormats = format == "json" && r.URL.Query().Get("conditional") != "0" && (readOptions.RichText || r.URL.Query().Get("conditional") == "1")
	if readOptions.ConditionalFormats {
		readOptions.ConditionalStatsReadOnly = true
		readOptions.ConditionalStatsStatus = a.conditionalStatsSheetStatus(meta.Path)
	}
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
				return i18n.Error("service.range.copiedTextExceedsMibReduce")
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
					return i18n.Error("service.range.copyingStylesExceedsThisCapacity")
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
			writeFailure(w, http.StatusRequestEntityTooLarge, err)
		} else {
			writeMessage(w, http.StatusInternalServerError, "service.range.readingSelectionFailed", i18n.Args{"detail": err})
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-ExcelLens-Range", strconv.Itoa(len(numbers)*colCount))
	if format == "json" {
		payload := clipboardPayload{WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb), Sheet: meta.Name, StartCol: col1, EndCol: col2, Filtered: filterID != "", Rows: make([]clipboardRow, 0, len(numbers))}
		rowAt := func(number int) clipboardRow {
			data := byRow[number]
			cells := data.Cells
			if cells == nil {
				cells = []workbook.CellData{}
			}
			return clipboardRow{Row: number, Style: data.Style, Cells: cells, PresentationNotice: data.PresentationNotice}
		}
		if headerRow > 0 {
			header := rowAt(headerRow)
			payload.Header = &header
		}
		for _, number := range numbers {
			payload.Rows = append(payload.Rows, rowAt(number))
		}
		if r.URL.Query().Get("portable") == "1" && formats.EditOOXML(wb.Format) && len(numbers) > 0 {
			clip, err := a.readerClipboard(r.Context(), wb, meta, idx, numbers, col1, colCount, byRow)
			if err != nil {
				payload.PortableNotice = "此选区只能复制显示内容：" + err.Error()
			} else {
				payload.Portable = clip
			}
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

// Buffer only the finished text so a budget or source error can never publish
// a partial selection. Rows arrive in the same visual order used by export.
func writeClipboardTSV(w http.ResponseWriter, r *http.Request, wb *workbook.Workbook, plan selectionRowPlan, first, last int) {
	columns := last - first + 1
	if plan.Len() > formats.ClipboardTSVRows || columns > formats.ClipboardTSVCols || plan.Len() > formats.ClipboardTSVCells/columns {
		writeMessage(w, http.StatusRequestEntityTooLarge, "service.range.selectionExceedsCopyCapacityPlain")
		return
	}
	var out strings.Builder
	rows := 0
	err := wb.VisitRowsByReferencesWithOptionsContext(r.Context(), plan.Index(), plan.Rows(), first, last, workbook.RowReadOptions{FormatValue: true}, func(row workbook.RowData) error {
		if rows > 0 {
			out.WriteByte('\n')
		}
		rows++
		cells := make(map[int]string, len(row.Cells))
		for _, cell := range row.Cells {
			cells[cell.Col] = cell.Display
		}
		for col := first; col <= last; col++ {
			if col > first {
				out.WriteByte('\t')
			}
			value := cells[col]
			size := len(value)
			if strings.ContainsAny(value, "\t\r\n\"") {
				size += 2 + strings.Count(value, "\"")
			}
			if size > formats.ClipboardTextBytes-out.Len() {
				return errSelectionLimit
			}
			out.WriteString(escapedClipboardCell(value))
		}
		return nil
	})
	if errors.Is(err, errSelectionLimit) {
		writeMessage(w, http.StatusRequestEntityTooLarge, "service.range.selectionTextExceedsMb")
		return
	}
	if err != nil {
		writeFailure(w, http.StatusConflict, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-ExcelLens-Range", strconv.Itoa(rows*columns))
	_, _ = io.WriteString(w, out.String())
}
