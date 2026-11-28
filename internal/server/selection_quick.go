package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"math/big"
	"net/http"

	"excellens/internal/analysis"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

type selectionQuickRequest struct {
	analysisSelectionCoordinates
	WorkbookKey  string `json:"workbookKey"`
	Book         string `json:"book"`
	Sheet        string `json:"sheet"`
	FilterID     string `json:"filterId,omitempty"`
	Start        string `json:"start"`
	End          string `json:"end"`
	SkipFirstRow bool   `json:"skipFirstRow"`
	Minuend      int    `json:"minuend"`
	Subtrahend   int    `json:"subtrahend"`
}

// Number is the sum of the three sign categories. Every selected cell belongs
// to exactly one of Number/Blank/Other/Errors/Missing/Invalid.
type quickCounts struct {
	Number   int64 `json:"number"`
	Negative int64 `json:"negative"`
	Zero     int64 `json:"zero"`
	Positive int64 `json:"positive"`
	Blank    int64 `json:"blank"`
	Other    int64 `json:"other"`
	Errors   int64 `json:"errors"`
	Missing  int64 `json:"missing"`
	Invalid  int64 `json:"invalid"`
}

func (c *quickCounts) add(wb *workbook.Workbook, cell workbook.CellData) *big.Rat {
	switch {
	case cell.CacheMissing:
		c.Missing++
	case cell.Type == "e":
		c.Errors++
	case cell.Raw == "" && !cell.HasFormula:
		c.Blank++
	default:
		if n := sourceCellNumber(wb, cell); n != nil {
			c.Number++
			switch n.Sign() {
			case -1:
				c.Negative++
			case 0:
				c.Zero++
			case 1:
				c.Positive++
			}
			return n
		}
		if cell.Type == "n" || cell.Type == "" {
			c.Invalid++
		} else {
			c.Other++
		}
	}
	return nil
}

type quickColumn struct {
	Column   int                     `json:"column"`
	Counts   quickCounts             `json:"counts"`
	Summary  analysis.QuickSummary   `json:"summary"`
	Segments []analysis.QuickSegment `json:"segments"`
}

type quickDifference struct {
	Paired   int64                   `json:"paired"`
	Unpaired int64                   `json:"unpaired"`
	Summary  analysis.QuickSummary   `json:"summary"`
	Segments []analysis.QuickSegment `json:"segments"`
}

type selectionQuickResponse struct {
	selectionQuickRequest
	SelectedRows int              `json:"selectedRows"`
	Rows         int              `json:"rows"`
	FirstColumn  int              `json:"firstColumn"`
	LastColumn   int              `json:"lastColumn"`
	Columns      []quickColumn    `json:"columns"`
	Difference   *quickDifference `json:"difference,omitempty"`
}

var errQuickCapacity = i18n.Error("service.selectionQuick.selectedContentExceedsCapacityQuick")

func (a *App) handleSelectionQuick(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, formats.SelectionQuickRequestBytes))
	var req selectionQuickRequest
	if err != nil || decodeSavedConfig(data, &req) != nil || req.ViewID == "" || req.ExcludeThroughRow < 0 || req.ExcludeThroughRow > 30 {
		writeMessage(w, 400, "service.selectionQuick.quickAnalysisParametersInvalidReselect")
		return
	}
	wb := a.currentWorkbook()
	if wb == nil || req.WorkbookKey != wb.Fingerprint || req.Book != sourceBookID(wb) {
		writeMessage(w, 409, "service.selectionQuick.sourceSelectionSwitchedReopenQuick")
		return
	}
	// Queue before acquiring the cache lock; cancellation while waiting needs
	// no source IO or source-sized allocation.
	release, err := a.lockQueryRead(r.Context())
	if err != nil {
		return
	}
	defer release()
	meta, ok := wb.FindSheet(req.Sheet)
	if !ok {
		writeMessage(w, 404, "service.selectionQuick.quickAnalysisWorksheetDoesNot")
		return
	}
	idx := wb.GetCachedIndex(meta.Path)
	if idx == nil {
		writeMessage(w, 409, "service.selectionQuick.completeWorksheetIndexingFirst")
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
		valid := a.wb == wb && !a.maintenance && wb.GetCachedIndex(meta.Path) == idx && view != nil && view.data == snapshot.visible.data
		a.mu.RUnlock()
		if !valid {
			return i18n.Error("service.selectionQuick.sourceSelectionOrVisibleRow")
		}
		return wb.CheckSourceUnchanged()
	}
	if err = current(); err != nil {
		if r.Context().Err() == nil {
			writeFailure(w, 409, err)
		}
		return
	}
	plan, first, last, err := quickSelectionPlan(idx, snapshot, req)
	if err != nil {
		writeFailure(w, 400, err)
		return
	}
	result, err := readSelectionQuick(r.Context(), wb, plan, first, last, req)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		status := 500
		if errors.Is(err, errQuickCapacity) {
			status = 413
		}
		if changed := current(); changed != nil {
			err, status = changed, 409
		}
		writeFailure(w, status, err)
		return
	}
	data, err = json.Marshal(result)
	if err != nil || len(data) > formats.SelectionQuickResultBytes {
		writeFailure(w, 413, errQuickCapacity)
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

func quickSelectionPlan(idx *workbook.SheetIndex, snapshot selectionSnapshot, req selectionQuickRequest) (selectionRowPlan, int, int, error) {
	c1, r1, e1 := workbook.ParseCellRef(req.Start)
	c2, r2, e2 := workbook.ParseCellRef(req.End)
	first, last := min(c1, c2), max(c1, c2)
	if e1 != nil || e2 != nil || first < 1 || last > idx.MaxCol || last-first+1 > formats.SelectionQuickColumns || r1 > idx.MaxRow || r2 > idx.MaxRow {
		return selectionRowPlan{}, 0, 0, i18n.Error("service.selectionQuick.selectContinuousSelectionThatUses")
	}
	if (req.Minuend != 0 || req.Subtrahend != 0) && (req.Minuend < first || req.Minuend > last || req.Subtrahend < first || req.Subtrahend > last || req.Minuend == req.Subtrahend) {
		return selectionRowPlan{}, 0, 0, i18n.Error("service.selectionQuick.differenceSelectTwoDifferentSource")
	}
	view, err := newSelectionRowView(idx, snapshot.filter, req.ExcludeThroughRow, snapshot.visible)
	if err != nil {
		return selectionRowPlan{}, 0, 0, err
	}
	plan, err := view.plan(r1, r2, req.VisualStart, req.VisualEnd)
	if err != nil {
		return selectionRowPlan{}, 0, 0, err
	}
	if plan.Len() < 1 || int64(plan.Len())*int64(last-first+1) > formats.SelectionQuickCells {
		return selectionRowPlan{}, 0, 0, i18n.Error("service.selectionQuick.maximumAnalysisCapacityCellsReduce")
	}
	return plan, first, last, nil
}

func quickSourceRows(plan selectionRowPlan, skipFirst bool) iter.Seq[workbook.SourceRowReference] {
	return func(yield func(workbook.SourceRowReference) bool) {
		skip := skipFirst
		for row := range plan.Rows() {
			if skip {
				skip = false
				continue
			}
			if !yield(row) {
				return
			}
		}
	}
}

func readSelectionQuick(ctx context.Context, wb *workbook.Workbook, plan selectionRowPlan, first, last int, req selectionQuickRequest) (*selectionQuickResponse, error) {
	idx := plan.Index()
	// Check row buffers before the shared reader allocates any of them. This
	// scans only the selected plan and never materializes a full row-number map.
	var sourceBytes int64
	for ref := range quickSourceRows(plan, req.SkipFirstRow) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref.Position < 0 {
			continue
		}
		offset := idx.RowAt(ref.Position)
		if offset.Start < 0 || offset.End < offset.Start || offset.End > idx.XMLSize {
			return nil, i18n.Error("service.selectionQuick.quickAnalysisSourceRowCache")
		}
		length := offset.End - offset.Start
		if length > formats.SelectionQuickSourceBytes-sourceBytes {
			return nil, errQuickCapacity
		}
		sourceBytes += length
	}
	rows := plan.Len()
	if req.SkipFirstRow {
		rows--
	}
	result := &selectionQuickResponse{selectionQuickRequest: req, SelectedRows: plan.Len(), Rows: rows, FirstColumn: first, LastColumn: last, Columns: make([]quickColumn, last-first+1)}
	series := make([]*analysis.QuickSeries, len(result.Columns))
	for i := range series {
		series[i], _ = analysis.NewQuickSeries(rows, formats.SelectionQuickSegments)
		result.Columns[i].Column = first + i
	}
	var difference *analysis.QuickSeries
	if req.Minuend != 0 {
		difference, _ = analysis.NewQuickSeries(rows, formats.SelectionQuickSegments)
		result.Difference = &quickDifference{}
	}
	numbers := make([]*big.Rat, len(series))
	cells := make([]workbook.CellData, len(series))
	textRemaining := int64(formats.SelectionQuickTextBytes)
	err := wb.VisitRowsByReferencesWithOptionsContext(ctx, idx, quickSourceRows(plan, req.SkipFirstRow), first, last, workbook.RowReadOptions{}, func(row workbook.RowData) error {
		clear(cells)
		for _, cell := range row.Cells {
			if int64(len(cell.Raw)) > textRemaining {
				return errQuickCapacity
			}
			textRemaining -= int64(len(cell.Raw))
			cells[cell.Col-first] = cell
		}
		for i, cell := range cells {
			numbers[i] = result.Columns[i].Counts.add(wb, cell)
			if err := series[i].Add(row.Number, numbers[i]); err != nil {
				return err
			}
		}
		if difference != nil {
			left, right := numbers[req.Minuend-first], numbers[req.Subtrahend-first]
			var delta *big.Rat
			if left != nil && right != nil {
				delta = new(big.Rat).Sub(left, right)
				result.Difference.Paired++
			} else {
				result.Difference.Unpaired++
			}
			return difference.Add(row.Number, delta)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range series {
		result.Columns[i].Summary, result.Columns[i].Segments, err = series[i].Result()
		if err != nil {
			return nil, err
		}
	}
	if difference != nil {
		result.Difference.Summary, result.Difference.Segments, err = difference.Result()
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}
