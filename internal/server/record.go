package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type recordRequest struct {
	WorkbookKey      string `json:"workbookKey"`
	Book             string `json:"book"`
	Sheet            string `json:"sheet"`
	ViewID           string `json:"viewId"`
	FilterID         string `json:"filterId,omitempty"`
	Row              int    `json:"row"`
	HeaderRow        int    `json:"headerRow"`
	FirstColumn      int    `json:"firstColumn"`
	Columns          int    `json:"columns"`
	PinnedThroughRow int    `json:"pinnedThroughRow"`
}

type recordField struct {
	Column int                `json:"column"`
	Header *workbook.CellData `json:"header,omitempty"`
	Cell   workbook.CellData  `json:"cell"`
}

type recordResponse struct {
	recordRequest
	Position     int           `json:"position"`
	TotalRecords int           `json:"totalRecords"`
	PreviousRow  int           `json:"previousRow"`
	NextRow      int           `json:"nextRow"`
	TotalColumns int           `json:"totalColumns"`
	RowStyle     int           `json:"rowStyle"`
	HeaderStyle  int           `json:"headerStyle"`
	RowIndexed   bool          `json:"rowIndexed"`
	Fields       []recordField `json:"fields"`
	Notices      []string      `json:"notices"`
}

var errRecordCapacity = i18n.Error("service.record.fieldContentOnThisPage")

// The existing row-view identity binds the exact source/index and its order.
// A card has no independent full-table mapping, background job or row cache.
func (a *App) handleRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.RecordCardRequestBytes))
	var req recordRequest
	if err != nil || decodeSavedConfig(data, &req) != nil || req.ViewID == "" || req.Sheet == "" || req.Row < 1 || req.HeaderRow < 0 || req.FirstColumn < 1 || req.Columns < 1 || req.Columns > formats.RecordCardColumns || req.PinnedThroughRow < 0 || req.PinnedThroughRow > 30 {
		writeMessage(w, 400, "service.record.recordCardParametersInvalidReselect")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.record.recordSourceSwitchedReopenRecord")
		return
	}
	releaseCapture := a.lockCacheRead()
	defer releaseCapture()
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeMessage(w, 404, "service.record.recordCardWorksheetDoesNot")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, 409, "service.record.completeWorksheetIndexingFirst")
		return
	}
	source, supported := formats.Lookup(wb.Format)
	if !supported || req.Row > idx.MaxRow || req.HeaderRow > source.MaxRows || req.FirstColumn > idx.MaxCol || req.FirstColumn > source.MaxColumns || idx.MaxCol > source.MaxColumns {
		writeMessage(w, 400, "service.record.recordOrFieldExceedsUsed")
		return
	}
	snapshot, err := a.selectionSnapshot(wb, idx, meta.Path, req.FilterID, req.ViewID)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	defer snapshot.close()
	unpin := a.pinWorkbooks(wb)
	defer unpin()
	current := func() error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		a.mu.RLock()
		view := a.rowViews[req.ViewID]
		valid := a.wb == wb && !a.maintenance && view != nil && view.data == snapshot.visible.data && wb.GetCachedIndex(meta.Path) == idx
		a.mu.RUnlock()
		if !valid {
			return i18n.Error("service.record.sourceOrVisibleRowView")
		}
		return wb.CheckSourceUnchanged()
	}
	releaseCapture()
	releaseQuery, err := a.lockQueryRead(r.Context())
	if err != nil {
		return
	}
	defer releaseQuery()
	if err := current(); err != nil {
		if r.Context().Err() == nil {
			writeFailure(w, 409, err)
		}
		return
	}
	view, err := newSelectionRowView(idx, snapshot.filter, req.PinnedThroughRow, snapshot.visible)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	position, total, previous, next, err := recordNeighbors(view, req.Row)
	if err != nil {
		writeFailure(w, 409, err)
		return
	}
	result, err := readRecord(r.Context(), wb, idx, req)
	if err != nil {
		if r.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		status := 500
		if errors.Is(err, errRecordCapacity) {
			status = 413
		}
		if changed := current(); changed != nil {
			err, status = changed, 409
		}
		writeFailure(w, status, err)
		return
	}
	result.Position, result.TotalRecords, result.PreviousRow, result.NextRow = position, total, previous, next
	data, err = json.Marshal(result)
	if err != nil || len(data) > formats.RecordCardPageBytes {
		writeFailure(w, 413, errRecordCapacity)
		return
	}
	if err := current(); err != nil {
		if r.Context().Err() == nil {
			writeFailure(w, 409, err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// At most 30 frozen source rows precede the existing visible body. The body
// projection already excludes hidden rows and removes pinned duplicates.
func recordNeighbors(view selectionRowView, row int) (position, total, previous, next int, err error) {
	if row < 1 || row > view.idx.MaxRow {
		err = i18n.Error("service.record.recordsExceedCurrentlyUsedRow")
		return
	}
	through := min(view.through, view.idx.MaxRow)
	prefix := view.sourceRangeCount(1, through)
	total = prefix + view.count()
	if row <= through {
		if view.visible != nil && !view.visible.data.visibility.Visible(row) {
			err = i18n.Error("service.record.thisRecordHiddenSelectVisible")
			return
		}
		position = view.sourceRangeCount(1, row)
	} else {
		at, found := view.locate(row)
		if !found {
			err = i18n.Error("service.record.thisRecordNotPresentCurrent")
			return
		}
		position = prefix + at + 1
	}
	rowAt := func(at int) (int, error) {
		if at <= prefix {
			return view.appendSourceRange(nil, 1, prefix)[at-1], nil
		}
		rows, e := view.numbersPage(at-prefix-1, 1)
		if e != nil || len(rows) != 1 {
			return 0, i18n.Error("service.record.recordRowOrderInvalidReopen")
		}
		return rows[0], nil
	}
	if position > 1 {
		previous, err = rowAt(position - 1)
	}
	if err == nil && position < total {
		next, err = rowAt(position + 1)
	}
	return
}

func readRecord(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, req recordRequest) (*recordResponse, error) {
	numbers := []int{req.Row}
	if req.HeaderRow > 0 && req.HeaderRow != req.Row {
		numbers = append(numbers, req.HeaderRow)
	}
	// Bound the two source row buffers before the common reader allocates them.
	// Shared strings remain borrowed; their returned text is budgeted separately.
	if err := sourceRowsWithinBudget(idx, numbers, formats.RecordCardSourceBytes); err != nil {
		if errors.Is(err, errSourceReadCapacity) {
			return nil, errors.Join(errRecordCapacity, err)
		}
		return nil, err
	}
	last := min(idx.MaxCol, req.FirstColumn+req.Columns-1)
	rows, err := wb.ReadRowsByNumbersWithOptionsContext(ctx, idx, numbers, req.FirstColumn, last, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true})
	if err != nil {
		return nil, err
	}
	byRow := make(map[int]workbook.RowData, len(rows))
	textBudget := formats.RecordCardPageBytes
	for _, row := range rows {
		for _, cell := range row.Cells {
			texts := []string{cell.Ref, cell.Raw, cell.Display, cell.Formula}
			if cell.Conditional != nil && cell.Conditional.Display != nil {
				texts = append(texts, *cell.Conditional.Display)
			}
			for _, text := range texts {
				if !jsonTextFits(text, &textBudget) {
					return nil, errRecordCapacity
				}
			}
		}
		byRow[row.Number] = row
	}
	result := &recordResponse{recordRequest: req, TotalColumns: idx.MaxCol, Fields: make([]recordField, 0, last-req.FirstColumn+1), Notices: []string{}, RowStyle: byRow[req.Row].Style, HeaderStyle: byRow[req.HeaderRow].Style}
	result.Columns = last - req.FirstColumn + 1
	at := idx.FindRowOffset(req.Row)
	result.RowIndexed = at < idx.RowCount() && idx.RowAt(at).Number == req.Row
	for _, row := range rows {
		if row.PresentationNotice != "" {
			result.Notices = append(result.Notices, "源行"+strconv.Itoa(row.Number)+"："+row.PresentationNotice)
		}
	}
	find := func(row, col int) workbook.CellData {
		for _, cell := range byRow[row].Cells {
			if cell.Col == col {
				return cell
			}
		}
		return workbook.CellData{Ref: workbook.ColumnName(col) + strconv.Itoa(row), Col: col, Raw: "", Display: ""}
	}
	remaining := formats.RecordCardPageBytes - 4096
	for col := req.FirstColumn; col <= last; col++ {
		field := recordField{Column: col, Cell: find(req.Row, col)}
		if req.HeaderRow > 0 {
			header := find(req.HeaderRow, col)
			field.Header = &header
		}
		encoded, err := json.Marshal(field)
		if err != nil || len(encoded)+1 > remaining {
			return nil, errRecordCapacity
		}
		remaining -= len(encoded) + 1
		result.Fields = append(result.Fields, field)
	}
	return result, nil
}
