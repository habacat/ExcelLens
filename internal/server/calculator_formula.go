package server

import (
	"context"

	"fmt"
	"iter"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"excellens/internal/calculator"
	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/workbook"
)

func (a *App) handleCalculatorFunctions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if r.URL.Query().Get("scope") == "worksheet" {
		writeJSON(w, map[string]any{"items": calculator.WorksheetFunctions()})
		return
	}
	writeJSON(w, map[string]any{"items": calculator.Functions()})
}

// Range summaries and element-wise functions use the same source eligibility.
func formulaCellNumber(wb *workbook.Workbook, cell workbook.CellData) (*big.Rat, bool) {
	if cell.Type != "" && cell.Type != "n" {
		source, ok := formats.Lookup(wb.Format)
		if !ok || !source.TextAmounts {
			return nil, false
		}
	}
	return calcCellNumber(cell)
}

func formulaCellValue(wb *workbook.Workbook, cell workbook.CellData) (calculator.FormulaValue, error) {
	if cell.CacheMissing {
		return calculator.FormulaValue{}, i18n.Error("service.calculatorFormula.valueFormulaLacksSavedResults")
	}
	if cell.Type == "e" {
		return calculator.FormulaValue{}, fmt.Errorf("%s: %s", cell.Raw, cell.Ref)
	}
	if cell.Raw == "" {
		if cell.HasFormula {
			return calculator.TextValue(""), nil
		}
		return calculator.BlankValue(), nil
	}
	if cell.Type == "b" {
		return calculator.BoolValue(cell.Raw == "TRUE" || cell.Raw == "1"), nil
	}
	if cell.Type == "n" || cell.Type == "" {
		if n, ok := calculatorNumber(cell.Raw, true); ok {
			return calculator.NumberValue(n), nil
		}
	}
	v := calculator.TextValue(cell.Raw)
	if number, ok := formulaCellNumber(wb, cell); ok {
		v.NumericText = number
	}
	return v, nil
}

func visitFormulaRange(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, c1, r1, c2, r2 int, emit func(int, calculator.FormulaValue) error) error {
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	select {
	case calculatorScanSlots <- struct{}{}:
		defer func() { <-calculatorScanSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	first, last := idx.FindRowOffset(r1), idx.FindRowOffset(r2+1)
	positions := iter.Seq[int](func(yield func(int) bool) {
		for i := first; i < last; i++ {
			if !yield(i) {
				return
			}
		}
	})
	return wb.VisitRowsContext(ctx, idx, positions, c1, c2, func(row workbook.RowData) error {
		for _, cell := range row.Cells {
			v, err := formulaCellValue(wb, cell)
			if err != nil {
				v = calculator.FormulaValue{Kind: "error", Text: err.Error()}
			}
			if err = emit((row.Number-r1)*(c2-c1+1)+cell.Col-c1, v); err != nil {
				return err
			}
		}
		return nil
	})
}
func formulaRangeMetadata(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, c1, r1, c2, r2 int, value calculator.RangeValue) calculator.RangeValue {
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	value.Rows, value.Cols, value.FirstRow, value.FirstCol = r2-r1+1, c2-c1+1, r1, c1
	// A single-column index scan emits one position per ordered source row.
	// Do not assume arbitrary cell order within a wide XML row.
	value.Ordered = c1 == c2
	value.Visit = func(emit func(int, calculator.FormulaValue) error) error {
		return visitFormulaRange(ctx, wb, idx, c1, r1, c2, r2, emit)
	}
	value.At = func(i int) (calculator.FormulaValue, error) {
		if i < 0 || i >= value.Rows*value.Cols {
			return calculator.FormulaValue{}, i18n.Error("service.calculatorFormula.refSubscriptOutRange")
		}
		rr, cc := r1+i/value.Cols, c1+i%value.Cols
		position := idx.FindRowOffset(rr)
		if position >= idx.RowCount() || idx.RowAt(position).Number != rr {
			return calculator.BlankValue(), nil
		}
		rows, err := wb.ReadRowsContext(ctx, idx, position, 1, cc, cc)
		if err != nil {
			return calculator.FormulaValue{}, err
		}
		for _, cell := range rows[0].Cells {
			if cell.Col == cc {
				return formulaCellValue(wb, cell)
			}
		}
		return calculator.BlankValue(), nil
	}
	return value
}

func loadFormulaRange(ctx context.Context, wb *workbook.Workbook, idx *workbook.SheetIndex, c1, r1, c2, r2 int) ([]calculator.FormulaValue, error) {
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	size := int64(c2-c1+1) * int64(r2-r1+1)
	if size > 8192 {
		return nil, i18n.Error("service.calculatorFormula.textRangeNumExceedsCells")
	}
	select {
	case calculatorScanSlots <- struct{}{}:
		defer func() { <-calculatorScanSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([]calculator.FormulaValue, int(size))
	for i := range out {
		out[i] = calculator.BlankValue()
	}
	first, last := idx.FindRowOffset(r1), idx.FindRowOffset(r2+1)
	positions := iter.Seq[int](func(yield func(int) bool) {
		for i := first; i < last; i++ {
			if !yield(i) {
				return
			}
		}
	})
	textBytes := 0
	err := wb.VisitRowsContext(ctx, idx, positions, c1, c2, func(row workbook.RowData) error {
		for _, cell := range row.Cells {
			value, err := formulaCellValue(wb, cell)
			if err != nil {
				return err
			}
			textBytes += len(value.Text)
			if textBytes > 64<<10 {
				return i18n.Error("service.calculatorFormula.numTextRangeTooLarge")
			}
			out[(row.Number-r1)*(c2-c1+1)+cell.Col-c1] = value
		}
		return nil
	})
	return out, err
}

func (a *App) handleCalculatorFormula(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req struct {
		Expression  string `json:"expression"`
		WorkbookKey string `json:"workbookKey"`
		Sheet       string `json:"sheet"`
	}
	if decodeCalculator(w, r, &req) != nil {
		writeMessage(w, 400, "service.calculatorFormula.invalidFormula")
		return
	}
	wb := a.currentWorkbook()
	a.cacheMu.RLock()
	defer a.cacheMu.RUnlock()
	resolved := false
	var scanCells int64
	ctx := calculator.WithDateSystem(r.Context(), wb != nil && wb.Date1904)
	result, err := calculator.Formula(ctx, req.Expression, func(sheet, start, end string) (calculator.RangeValue, error) {
		if wb == nil || req.WorkbookKey == "" || wb.Fingerprint != req.WorkbookKey || a.currentWorkbook() != wb {
			return calculator.RangeValue{}, i18n.Error("service.calculatorFormula.workbookSwitched")
		}
		resolved = true
		if sheet == "" {
			sheet = req.Sheet
		}
		meta, ok := wb.FindSheet(sheet)
		if !ok {
			for _, candidate := range wb.Sheets {
				if strings.EqualFold(candidate.Name, sheet) {
					meta = candidate
					ok = true
					break
				}
			}
		}
		if !ok {
			return calculator.RangeValue{}, i18n.Error("service.calculatorFormula.worksheetDoesNotExist", i18n.Args{"sheet": sheet})
		}
		idx := wb.GetCachedIndex(meta.Path)
		if idx == nil {
			select {
			case calculatorScanSlots <- struct{}{}:
			case <-r.Context().Done():
				return calculator.RangeValue{}, r.Context().Err()
			}
			var err error
			idx, err = wb.BuildIndex(meta, nil, r.Context().Done())
			<-calculatorScanSlots
			if err != nil {
				return calculator.RangeValue{}, err
			}
		}
		col, row, err := workbook.ParseCellRef(start)
		if err != nil && end != "" {
			if c1, _, cerr := workbook.ParseCellRef(start + "1"); cerr == nil {
				if c2, _, cerr := workbook.ParseCellRef(end + "1"); cerr == nil {
					start, end = workbook.ColumnName(c1)+"1", workbook.ColumnName(c2)+strconv.Itoa(max(1, idx.MaxRow))
					col, row, err = workbook.ParseCellRef(start)
				}
			}
			if err != nil {
				r1, e1 := strconv.Atoi(start)
				r2, e2 := strconv.Atoi(end)
				if e1 == nil && e2 == nil && r1 > 0 && r2 > 0 {
					start, end = "A"+strconv.Itoa(r1), workbook.ColumnName(max(1, idx.MaxCol))+strconv.Itoa(r2)
					col, row, err = workbook.ParseCellRef(start)
				}
			}
		}
		if err != nil || col > formats.ExcelColumns || row > workbook.MaxRowsForIndex(idx) {
			return calculator.RangeValue{}, i18n.Error("service.calculatorFormula.referenceOutRange")
		}
		if end == "" {
			finish := func(value calculator.RangeValue) (calculator.RangeValue, error) {
				return formulaRangeMetadata(r.Context(), wb, idx, col, row, col, row, value), nil
			}
			position := idx.FindRowOffset(row)
			value := new(big.Rat)
			count := int64(0)
			nonempty := int64(0)
			scalar := calculator.BlankValue()
			if position < idx.RowCount() && idx.RowAt(position).Number == row {
				rows, err := wb.ReadRowsContext(r.Context(), idx, position, 1, col, col)
				if err != nil {
					return calculator.RangeValue{}, err
				}
				for _, cell := range rows[0].Cells {
					if cell.Col != col {
						continue
					}
					scalar, err = formulaCellValue(wb, cell)
					if err != nil {
						return finish(calculator.RangeValue{Sum: value, NonEmpty: 1, Fault: err})
					}
					if cell.HasFormula || cell.Raw != "" {
						nonempty = 1
					}
					if cell.Raw == "" {
						break
					}
					v, ok := formulaCellNumber(wb, cell)
					if !ok {
						return finish(calculator.RangeValue{Sum: value, Count: 0, NonEmpty: nonempty, Text: scalar.Kind == "text", Scalar: &scalar})
					}
					value = v
					count = 1
				}
			}
			return finish(calculator.RangeValue{Sum: value, Min: value, Max: value, Count: count, NonEmpty: nonempty, Scalar: &scalar})
		}
		endCol, endRow, err := workbook.ParseCellRef(end)
		if err != nil {
			return calculator.RangeValue{}, i18n.Error("service.calculatorFormula.invalidReference")
		}
		scanCells += int64(max(row, endRow)-min(row, endRow)+1) * int64(max(col, endCol)-min(col, endCol)+1)
		if scanCells > 4000000 {
			return calculator.RangeValue{}, i18n.Error("service.calculatorFormula.formulaReferenceExceedsMillionCells")
		}
		var once sync.Once
		var summary calculator.RangeValue
		var summaryErr error
		aggregate := func() (calculator.RangeValue, error) {
			once.Do(func() {
				select {
				case calculatorScanSlots <- struct{}{}:
					defer func() { <-calculatorScanSlots }()
				case <-r.Context().Done():
					summaryErr = r.Context().Err()
					return
				}
				c := calculatorCollector{sum: new(big.Rat), trackExtrema: true, formulaBook: wb}
				summaryErr = scanCalculatorSelection(r.Context(), wb, idx, nil, selectionStatRequest{Ranges: []selectionStatRect{{Start: start, End: end}}}, &c)
				fault := c.firstFault
				summary = calculator.RangeValue{Sum: c.sum, Min: c.minimum, Max: c.maximum, Count: c.number, NonEmpty: c.nonEmpty, Fault: fault}
			})
			return summary, summaryErr
		}
		return formulaRangeMetadata(r.Context(), wb, idx, col, row, endCol, endRow, calculator.RangeValue{Aggregate: aggregate, Load: func() ([]calculator.FormulaValue, error) {
			return loadFormulaRange(r.Context(), wb, idx, col, row, endCol, endRow)
		}}), nil
	})
	if err != nil {
		if r.Context().Err() == nil {
			switch i18n.ErrorKey(err) {
			case "service.calculatorFormula.workbookSwitched", "service.calculatorFormula.worksheetDoesNotExist",
				"service.calculatorFormula.referenceOutRange", "service.calculatorFormula.invalidReference",
				"service.calculatorFormula.formulaReferenceExceedsMillionCells":
				err = i18n.Wrap("service.calculatorFormula.referenceError", err)
			}
			writeFailure(w, 422, err)
		}
		return
	}
	if resolved && a.currentWorkbook() != wb {
		writeFailure(w, 409, i18n.Wrap("service.calculatorFormula.referenceError", i18n.Error("service.calculatorFormula.workbookSwitched")))
		return
	}
	writeJSON(w, struct {
		calculator.Result
		Suggestions []conversionSuggestion `json:"suggestions,omitempty"`
	}{result, calculationSuggestions(result, responseLocale(w))})
}
