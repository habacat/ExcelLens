package editfile

import (
	"errors"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

func (s *calcRevision) current(a editdoc.Address) editdoc.Value {
	v := s.book.cells[a].value
	if patch, ok := s.patches[a]; ok {
		v = patch.Apply(v)
	}
	return v
}

var errCircular = errors.New("循环引用")

func (s *calcRevision) store(a editdoc.Address, cell CalculatedCell) bool {
	// Bound retained result text and the sparse response, including reused cells.
	s.resultBytes += len(cell.Formula.Raw) + len(cell.Value.Raw) + 128
	if s.resultBytes > formats.EditCalcProjectionBytes {
		return false
	}
	s.result.Cells[a] = cell
	return true
}

type deferredInput struct{ addr editdoc.Address }

func (*deferredInput) Error() string { return "计算前置公式" }

// Resolve long chains in bounded stack segments. On resumption, completed
// siblings are memoized. A reference back into a suspended segment is a cycle.
func (s *calcRevision) evaluate(a editdoc.Address) {
	stack := []editdoc.Address{a}
	for len(stack) > 0 {
		top := stack[len(stack)-1]
		delete(s.waiting, top)
		_, err := s.value(top)
		if next, ok := err.(*deferredInput); ok {
			s.waiting[top] = true
			stack = append(stack, next.addr)
		} else {
			stack = stack[:len(stack)-1]
		}
	}
}

func (s *calcRevision) value(a editdoc.Address) (calculator.FormulaValue, error) {
	if err := s.ctx.Err(); err != nil {
		return calculator.FormulaValue{}, err
	}
	s.result.Visits++
	if s.result.Visits > formats.EditCalcVisits || s.resultBytes > formats.EditCalcProjectionBytes {
		return calculator.FormulaValue{}, calculator.ErrWorksheetPending
	}
	for _, area := range s.result.Generated {
		if area.Contains(a) && (area.Pivot || a != area.Start) {
			return calculator.FormulaValue{}, calculator.ErrWorksheetPending
		}
	}
	if v, ok := s.typed[a]; ok {
		return v, nil
	}
	if cell, ok := s.result.Cells[a]; ok {
		switch cell.State {
		case "calculated":
			v, err := calcOperand(cell.Value, s.book.date1904)
			if err == nil {
				s.typed[a] = v
			}
			return v, err
		case "cycle":
			return calculator.FormulaValue{}, errCircular
		default:
			return calculator.FormulaValue{}, calculator.ErrWorksheetPending
		}
	}
	v := s.current(a)
	if v.Kind != "formula" || !s.dirty[a] {
		if v.Kind == "formula" {
			cache := s.book.cells[a].cache
			if cache == nil {
				return calculator.FormulaValue{}, calculator.ErrWorksheetPending
			}
			v = *cache
		}
		typed, err := calcOperand(v, s.book.date1904)
		if err == nil && typed.Kind != "blank" {
			s.typed[a] = typed
		}
		return typed, err
	}
	if s.busy[a] || s.waiting[a] {
		return calculator.FormulaValue{}, errCircular
	}
	if len(s.busy) > 256 {
		return calculator.FormulaValue{}, &deferredInput{a}
	}
	s.busy[a] = true
	defer delete(s.busy, a)
	form, ok := s.programs[a]
	if !ok {
		form = s.book.formulas[a]
	}
	result := CalculatedCell{Formula: v, State: "pending"}
	var barrier error
	resolve := func(sheet, start, end string) (calculator.RangeValue, error) {
		area := s.book.qualify(calculator.WorksheetBounds(sheet, start, end), a.Sheet)
		return s.rangeValue(area, func(err error) {
			if err != nil {
				barrier = err
			}
		}), nil
	}
	tag := s.book.cells[a].tag
	var operand calculator.FormulaValue
	err := calculator.ErrWorksheetPending
	if tag.kind != "array" || tag.area.single() {
		operand, err = form.Evaluate(s.ctx, resolve)
		if barrier != nil {
			err = barrier
		}
	}
	if _, deferred := err.(*deferredInput); deferred {
		return calculator.FormulaValue{}, err
	}
	if err == nil {
		result.Value, err = calcOutput(operand)
		if err == nil {
			result.State = "calculated"
			s.typed[a] = operand
		}
	}
	if errors.Is(err, errCircular) {
		result.State = "cycle"
	}
	if !s.store(a, result) {
		return calculator.FormulaValue{}, calculator.ErrWorksheetPending
	}
	return operand, err
}

func calcOperand(v editdoc.Value, date1904 bool) (calculator.FormulaValue, error) {
	switch v.Kind {
	case "", "blank", "missing":
		return calculator.BlankValue(), nil
	case "date":
		t, err := editdoc.ISODateTime(v.Raw)
		if err != nil {
			return calculator.FormulaValue{}, err
		}
		// Excel uses the wall-clock components of an ISO cell.
		days := calculator.ExcelDateSerial(t, date1904)
		seconds := float64(t.Hour()*3600+t.Minute()*60+t.Second()) + float64(t.Nanosecond())/1e9
		n, _ := new(big.Rat).SetString(strconv.FormatFloat(float64(days)+seconds/86400, 'g', -1, 64))
		return calculator.NumberValue(n), nil
	case "text":
		return calculator.TextValue(v.Raw), nil
	case "boolean":
		return calculator.BoolValue(v.Raw == "TRUE"), nil
	case "error":
		return calculator.FormulaValue{Kind: "error", Text: v.Raw}, nil
	case "number":
		n, err := strconv.ParseFloat(v.Raw, 64)
		if err != nil || math.IsInf(n, 0) {
			return calculator.FormulaValue{Kind: "error", Text: "#NUM!"}, nil
		}
		if math.Abs(n) < 0x1p-1022 {
			n = 0
		}
		r, _ := new(big.Rat).SetString(strconv.FormatFloat(n, 'g', -1, 64))
		return calculator.NumberValue(r), nil
	}
	return calculator.FormulaValue{}, calculator.ErrWorksheetPending
}

func calcOutput(v calculator.FormulaValue) (editdoc.Value, error) {
	out := editdoc.Value{Kind: v.Kind, Raw: v.Text}
	switch v.Kind {
	case "number":
		n, _ := v.Number.Float64()
		if math.IsInf(n, 0) || math.IsNaN(n) {
			return editdoc.Value{Kind: "error", Raw: "#NUM!"}, nil
		}
		format := byte('g')
		if magnitude := math.Abs(n); magnitude >= 1e-9 && magnitude < 1e11 {
			format = 'f'
		}
		out.Raw = strconv.FormatFloat(n, format, -1, 64)
	case "boolean":
		out.Raw = "FALSE"
		if v.Bool {
			out.Raw = "TRUE"
		}
	case "text", "error":
	default:
		return editdoc.Value{}, calculator.ErrWorksheetPending
	}
	return out, nil
}

// The coordinate list is cached only within this revision. Full columns visit
// stored cells and newly populated coordinates, preserving Excel's true shape.
func (s *calcRevision) sparse(r calculator.WorksheetReference) []editdoc.Address {
	if cached, ok := s.positions[r]; ok {
		return cached
	}
	var found []editdoc.Address
	for col := r.Left; col <= r.Right; col++ {
		k := calcColumn{r.Sheet, col}
		for _, rows := range [][]int{s.book.columns[k], s.added[k]} {
			start := sort.SearchInts(rows, r.Top)
			for _, row := range rows[start:] {
				if row > r.Bottom {
					break
				}
				found = append(found, editdoc.Address{Sheet: r.Sheet, Row: row, Col: col})
			}
		}
	}
	if r.Left != r.Right || len(s.added[calcColumn{r.Sheet, r.Left}]) > 0 {
		slices.SortFunc(found, compareCalcAddress)
	}
	if len(s.positions) < 8192 && s.positionCount+len(found) <= formats.EditCalcSourceCells {
		s.positions[r] = found
		s.positionCount += len(found)
	}
	return found
}

func (s *calcRevision) rangeValue(r calculator.WorksheetReference, failure func(error)) calculator.RangeValue {
	rows, cols := r.Bottom-r.Top+1, r.Right-r.Left+1
	get := func(a editdoc.Address) (calculator.FormulaValue, error) {
		v, err := s.value(a)
		if err != nil {
			failure(err)
		}
		return v, err
	}
	rv := calculator.RangeValue{Rows: rows, Cols: cols, FirstRow: r.Top, FirstCol: r.Left, Ordered: true}
	rv.At = func(i int) (calculator.FormulaValue, error) {
		return get(editdoc.Address{Sheet: r.Sheet, Row: r.Top + i/cols, Col: r.Left + i%cols})
	}
	visit := func(subtotal, skipHidden bool, fn func(int, calculator.FormulaValue) error) error {
		// A previously empty position inside a stale generated area may now
		// contain a value. Sparse iteration cannot assume it is still blank.
		for _, owner := range s.result.Generated {
			if intersectsGenerated(r, owner) {
				failure(calculator.ErrWorksheetPending)
				return calculator.ErrWorksheetPending
			}
		}
		for _, addr := range s.sparse(r) {
			if subtotal {
				if s.book.hidden[addr.Sheet][addr.Row] {
					if skipHidden {
						continue
					}
					for _, area := range s.book.filtered[addr.Sheet] {
						// Filtering hides the entire worksheet row, including
						// cells in columns to either side of the table.
						if area.firstRow <= addr.Row && addr.Row <= area.lastRow {
							failure(calculator.ErrWorksheetPending)
							return calculator.ErrWorksheetPending
						}
					}
				}
				if s.current(addr).Kind == "formula" {
					f, ok := s.programs[addr]
					if !ok {
						f = s.book.formulas[addr]
					}
					if f.Subtotal {
						continue
					}
				}
			}
			v, err := get(addr)
			if err != nil {
				return err
			}
			if v.Kind != "blank" {
				if err = fn((addr.Row-r.Top)*cols+addr.Col-r.Left, v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	rv.Visit = func(fn func(int, calculator.FormulaValue) error) error { return visit(false, false, fn) }
	rv.VisitSubtotal = func(hidden bool, fn func(int, calculator.FormulaValue) error) error { return visit(true, hidden, fn) }
	rv.Slice = func(row, col, height, width int) calculator.RangeValue {
		return s.rangeValue(calculator.WorksheetReference{Sheet: r.Sheet, Top: r.Top + row, Left: r.Left + col, Bottom: r.Top + row + height - 1, Right: r.Left + col + width - 1}, failure)
	}
	rv.Aggregate = func() (calculator.RangeValue, error) {
		total := calculator.RangeValue{Sum: new(big.Rat)}
		var sum float64
		err := rv.Visit(func(_ int, v calculator.FormulaValue) error {
			total.NonEmpty++
			if v.Kind == "error" && total.Fault == nil {
				total.Fault = errors.New(v.Text)
			}
			if v.Kind == "number" {
				n := v.Number
				total.Count++
				f, _ := n.Float64()
				sum += f
				if total.Min == nil || n.Cmp(total.Min) < 0 {
					total.Min = n
				}
				if total.Max == nil || n.Cmp(total.Max) > 0 {
					total.Max = n
				}
			}
			return nil
		})
		if total.Sum.SetFloat64(sum) == nil && total.Fault == nil {
			total.Fault = errors.New("#NUM!")
		}
		return total, err
	}
	if rows == 1 && cols == 1 {
		v, err := rv.At(0)
		rv.Scalar = &v
		rv.Fault = err
		if v.Kind == "error" {
			rv.Fault = errors.New(v.Text)
		}
	}
	return rv
}
