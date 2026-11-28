package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type cellExplanationRequest struct {
	WorkbookKey      string `json:"workbookKey"`
	Book             string `json:"book"`
	Sheet            string `json:"sheet"`
	ViewID           string `json:"viewId"`
	FilterID         string `json:"filterId,omitempty"`
	Ref              string `json:"ref"`
	PinnedThroughRow int    `json:"pinnedThroughRow"`
}

type cellNumericExplanation struct {
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
	Exact    string `json:"exact,omitempty"`
}

type cellExplanationResponse struct {
	cellExplanationRequest
	Cell               workbook.CellData      `json:"cell"`
	Numeric            cellNumericExplanation `json:"numeric"`
	FormulaState       string                 `json:"formulaState"`
	FormulaAvailable   bool                   `json:"formulaAvailable"`
	Filter             *filterSpec            `json:"filter,omitempty"`
	RowState           string                 `json:"rowState"`
	PresentationNotice string                 `json:"presentationNotice,omitempty"`
}

var errCellExplanationCapacity = i18n.Error("service.cellExplanation.cellInformationExceedsReadingCapacity")

// The ordinary GET /api/cell remains a source-coordinate lookup for references.
// Explanation is an explicit operation bound to the accepted visible snapshot.
func (a *App) handleCellExplanation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.CellExplanationRequestBytes))
	var req cellExplanationRequest
	if err != nil || decodeSavedConfig(data, &req) != nil || req.WorkbookKey == "" || req.Book == "" || req.Sheet == "" || req.ViewID == "" || req.PinnedThroughRow < 0 || req.PinnedThroughRow > 30 {
		writeMessage(w, 400, "service.cellExplanation.cellInformationParameterInvalidReselect")
		return
	}
	col, row, err := workbook.ParseCellRef(req.Ref)
	if err != nil || req.Ref != workbook.ColumnName(col)+strconv.Itoa(row) {
		writeMessage(w, 400, "service.cellExplanation.cellAddressInvalid")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.cellExplanation.cellSourceSwitchedReopenInformation")
		return
	}
	releaseCapture := a.lockCacheRead()
	defer releaseCapture()
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeMessage(w, 404, "service.cellExplanation.worksheetDoesNotExist")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, 409, "service.cellExplanation.completeWorksheetIndexingFirst")
		return
	}
	source, ok := formats.Lookup(wb.Format)
	if !ok || row > source.MaxRows || col > source.MaxColumns {
		writeMessage(w, 400, "service.cellExplanation.cellExceedsSourceFormatRange")
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
			return i18n.Error("service.cellExplanation.cellSourceOrViewChanged")
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
	result, err := readCellExplanation(r.Context(), wb, idx, req, row, col)
	if err == nil {
		result.RowState = cellExplanationRowState(idx, snapshot, req, row)
		if snapshot.filter != nil && snapshot.filter.Spec.active() {
			// The retained, immutable result owns these slices until encoding ends.
			result.Filter = &snapshot.filter.Spec
		}
		if !cellExplanationFits(result) {
			err = errCellExplanationCapacity
		}
	}
	if err == nil {
		data, err = json.Marshal(result)
		if len(data) > formats.CellExplanationBytes {
			err = errCellExplanationCapacity
		}
	}
	if changed := current(); changed != nil {
		if r.Context().Err() == nil {
			writeFailure(w, 409, changed)
		}
		return
	}
	if err != nil {
		status := 500
		if errors.Is(err, errCellExplanationCapacity) {
			status = 413
		}
		writeFailure(w, status, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func readCellExplanation(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, req cellExplanationRequest, row, col int) (*cellExplanationResponse, error) {
	if err := sourceRowsWithinBudget(idx, []int{row}, formats.CellExplanationSourceBytes); err != nil {
		if errors.Is(err, errSourceReadCapacity) {
			return nil, errors.Join(errCellExplanationCapacity, err)
		}
		return nil, err
	}
	rows, err := wb.ReadRowsByNumbersWithOptionsContext(ctx, idx, []int{row}, col, col, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true, ConditionalStatsReadOnly: true})
	if err != nil {
		return nil, err
	}
	result := &cellExplanationResponse{cellExplanationRequest: req, Cell: workbook.CellData{Ref: req.Ref, Col: col}, FormulaState: "none"}
	for _, source := range rows {
		result.PresentationNotice = source.PresentationNotice
		for _, cell := range source.Cells {
			if cell.Col == col {
				if source.Number != row || cell.Ref != req.Ref {
					return nil, i18n.Error("service.cellExplanation.cellSourceCoordinatesInconsistentRe")
				}
				result.Cell = cell
			}
		}
	}
	c := result.Cell
	result.Numeric = explainSourceCellNumber(wb, c)
	if c.HasFormula {
		result.FormulaState = "cached"
		if c.CacheMissing {
			result.FormulaState = "missing"
		}
		result.FormulaAvailable = strings.HasPrefix(c.Formula, "=") && len(c.Formula) > 1
	}
	return result, nil
}

func explainSourceCellNumber(wb *workbook.Workbook, c workbook.CellData) cellNumericExplanation {
	if value := sourceCellNumber(wb, c); value != nil {
		reason := "number"
		if c.Type != "n" && c.Type != "" {
			reason = "imported-amount"
		}
		return cellNumericExplanation{Eligible: true, Reason: reason, Exact: analysis.ExactDecimalString(value)}
	}
	reason := "unsupported-type"
	switch {
	case c.CacheMissing:
		reason = "missing-result"
	case c.Raw == "":
		reason = "blank"
	case c.Type == "n" || c.Type == "":
		reason = "invalid-number"
	case c.Type == "e":
		reason = "error"
	case c.Type == "b":
		reason = "boolean"
	case c.Type == "d":
		reason = "date"
	case c.Type == "s" || c.Type == "str" || c.Type == "inlineStr":
		reason = "source-text"
		if wb != nil {
			if source, ok := formats.Lookup(wb.Format); ok && source.TextAmounts {
				reason = "text-or-identifier"
			}
		}
	}
	return cellNumericExplanation{Reason: reason}
}

func cellExplanationRowState(idx *workbook.SheetIndex, snapshot selectionSnapshot, req cellExplanationRequest, row int) string {
	if row > idx.MaxRow {
		return "outside"
	}
	if !snapshot.visible.data.visibility.Visible(row) {
		return "hidden"
	}
	if snapshot.filter == nil {
		return "visible"
	}
	if row <= snapshot.filter.Spec.HeaderRow {
		return "header"
	}
	if row <= req.PinnedThroughRow {
		return "pinned"
	}
	if _, matched := snapshot.filter.position(idx, row); matched {
		return "matched"
	}
	return "filtered-out"
}

func cellExplanationFits(result *cellExplanationResponse) bool {
	remaining := formats.CellExplanationBytes - 4096
	texts := []string{result.Cell.Raw, result.Cell.Display, result.Cell.Formula, result.PresentationNotice, result.Numeric.Exact}
	if result.Cell.Conditional != nil && result.Cell.Conditional.Display != nil {
		texts = append(texts, *result.Cell.Conditional.Display)
	}
	if result.Filter != nil {
		for _, rule := range result.Filter.Rules {
			if !jsonTextFits(rule.Value, &remaining) {
				return false
			}
			for _, value := range rule.Values {
				if !jsonTextFits(value, &remaining) {
					return false
				}
			}
		}
	}
	for _, text := range texts {
		if !jsonTextFits(text, &remaining) {
			return false
		}
	}
	return true
}
