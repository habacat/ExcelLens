package conditional

import (
	"context"
	"errors"
	"math/big"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

const WorkNotice = "条件格式超过本次求值预算"

// DependencyUnavailableError reports a bounded read or missing source dependency
// that must stop rule evaluation honestly instead of fabricating a blank or zero.
type DependencyUnavailableError struct {
	Reason string
}

func (e *DependencyUnavailableError) Error() string { return e.Reason }

type RangeCell struct {
	Row, Col int
	Cell     CellOperand
}

type RangeData struct {
	R1, C1, R2, C2 int
	Cells          []RangeCell
	BlankCount     int64
}

// CellResolver returns available=false when a dependency could not be read.
// A proven blank cell returns an empty CellOperand and available=true.
type CellResolver func(row, col int) (cell CellOperand, available bool, err error)
type ForeignCellResolver func(sheet string, row, col int) (CellOperand, string, error)
type RangeResolver func(r1, c1, r2, c2 int) (RangeData, string, error)

// StatHandler executes an indexed or planned range statistic. Returning
// pending=true marks the current rule as awaiting background statistics.
type StatHandler func(node *Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (val EvalValue, notice string, pending bool, err error, handled bool)

// Engine evaluates compiled conditional expressions against workbook-provided
// cell, foreign-cell, range, and statistics resolvers with explicit work budgets.
type Engine struct {
	Ctx           context.Context
	Date1904      bool
	Work          int
	TextBytes     int
	Row, Col      int
	Cell          CellOperand
	current       EvalValue
	currentReady  bool
	currentNotice string
	Pending       bool
	Resolver      CellResolver
	ForeignCell   ForeignCellResolver
	RangeResolver RangeResolver
	StatHandler   StatHandler
	sliding       map[slidingKey]*slidingWindow
}

func NewEngine(ctx context.Context, date1904 bool) *Engine {
	return &Engine{
		Ctx:       ctx,
		Date1904:  date1904,
		Work:      formats.ConditionalEvalWork,
		TextBytes: formats.ConditionalEvalTextBytes,
	}
}

func (e *Engine) ResetBudget() {
	e.Work = formats.ConditionalEvalWork
	e.TextBytes = formats.ConditionalEvalTextBytes
}

func (e *Engine) ResetCell(row, col int, cell CellOperand, resolver CellResolver) {
	e.Row, e.Col, e.Cell, e.Resolver = row, col, cell, resolver
	e.currentReady = false
	e.currentNotice = ""
	e.Pending = false
}

func (e *Engine) Spend(n int) bool {
	if n < 0 || n > e.Work {
		e.Work = 0
		return false
	}
	e.Work -= n
	return true
}

func (e *Engine) CellValue(cell CellOperand) (EvalValue, string) {
	return DecodeOperand(cell, &e.TextBytes)
}

func (e *Engine) CurrentValue() (EvalValue, string) {
	if !e.currentReady {
		e.current, e.currentNotice = e.CellValue(e.Cell)
		e.currentReady = true
	}
	return e.current, e.currentNotice
}

// Eval evaluates an expression anchored at (anchorRow, anchorCol) with rule ranges.
func (e *Engine) Eval(node *Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	if err := e.Ctx.Err(); err != nil {
		return EvalValue{}, "", err
	}
	if !e.Spend(1) {
		return EvalValue{}, WorkNotice, nil
	}
	switch node.Kind {
	case Literal:
		return node.Value, "", nil
	case ReferenceCell:
		return e.evalRef(node.Ref, anchorRow, anchorCol)
	case Range:
		// A range in scalar context is invalid unless used inside a range-accepting function.
		return ErrorValue("#VALUE!"), "", nil
	case Unary:
		v, notice, err := e.Eval(node.Args[0], anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if v.Kind == "error" {
			return v, "", nil
		}
		if node.Op == "+" {
			// Excel unary + preserves text/value or returns number; for non-number text it's a no-op.
			return v, "", nil
		}
		r, errVal := AsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		return RatToValue(r.Neg(r)), "", nil
	case Percent:
		v, notice, err := e.Eval(node.Args[0], anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		r, errVal := AsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		r.Quo(r, big.NewRat(100, 1))
		return RatToValue(r), "", nil
	case Binary:
		return e.evalBinary(node, anchorRow, anchorCol, ranges)
	case Call:
		return e.evalCall(node, anchorRow, anchorCol, ranges)
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}

func ResolveRefCoords(ref Reference, row, col, anchorRow, anchorCol int) (int, int, bool) {
	r, c := ref.Row, ref.Col
	if !ref.AbsoluteRow {
		r += row - anchorRow
	}
	if !ref.AbsoluteCol {
		c += col - anchorCol
	}
	if r < 1 || r > formats.ExcelRows || c < 1 || c > formats.ExcelColumns {
		return 0, 0, false
	}
	return r, c, true
}

func ResolveRangeBounds(node *Expression[EvalValue], row, col, anchorRow, anchorCol int) (r1, c1, r2, c2 int, ok bool) {
	if node.Kind == ReferenceCell {
		r, c, valid := ResolveRefCoords(node.Ref, row, col, anchorRow, anchorCol)
		return r, c, r, c, valid
	}
	if node.Kind != Range {
		return 0, 0, 0, 0, false
	}
	a, b := node.Ref, node.EndRef
	if a.Row == 0 && b.Row == 0 {
		// Whole-column range: A:C
		c1, c2 = a.Col, b.Col
		if !a.AbsoluteCol {
			c1 += col - anchorCol
		}
		if !b.AbsoluteCol {
			c2 += col - anchorCol
		}
		if c1 < 1 || c1 > formats.ExcelColumns || c2 < 1 || c2 > formats.ExcelColumns {
			return 0, 0, 0, 0, false
		}
		if c1 > c2 {
			c1, c2 = c2, c1
		}
		return 1, c1, formats.ExcelRows, c2, true
	}
	if a.Col == 0 && b.Col == 0 {
		// Whole-row range: 2:5
		r1, r2 = a.Row, b.Row
		if !a.AbsoluteRow {
			r1 += row - anchorRow
		}
		if !b.AbsoluteRow {
			r2 += row - anchorRow
		}
		if r1 < 1 || r1 > formats.ExcelRows || r2 < 1 || r2 > formats.ExcelRows {
			return 0, 0, 0, 0, false
		}
		if r1 > r2 {
			r1, r2 = r2, r1
		}
		return r1, 1, r2, formats.ExcelColumns, true
	}
	var ok1, ok2 bool
	r1, c1, ok1 = ResolveRefCoords(a, row, col, anchorRow, anchorCol)
	r2, c2, ok2 = ResolveRefCoords(b, row, col, anchorRow, anchorCol)
	if !ok1 || !ok2 {
		return 0, 0, 0, 0, false
	}
	if r1 > r2 {
		r1, r2 = r2, r1
	}
	if c1 > c2 {
		c1, c2 = c2, c1
	}
	return r1, c1, r2, c2, true
}

func (e *Engine) ResolveRangeBounds(node *Expression[EvalValue], anchorRow, anchorCol int) (r1, c1, r2, c2 int, ok bool) {
	return ResolveRangeBounds(node, e.Row, e.Col, anchorRow, anchorCol)
}

func (e *Engine) readForeignCell(sheet string, row, col int) (CellOperand, string, error) {
	if e.ForeignCell == nil {
		return CellOperand{}, "定义名称引用的源单元格尚未读取", nil
	}
	return e.ForeignCell(sheet, row, col)
}

func (e *Engine) evalRef(ref Reference, anchorRow, anchorCol int) (EvalValue, string, error) {
	row, col, ok := ResolveRefCoords(ref, e.Row, e.Col, anchorRow, anchorCol)
	if !ok {
		return ErrorValue("#REF!"), "", nil
	}
	if ref.ForeignSheet != "" {
		cell, notice, err := e.readForeignCell(ref.ForeignSheet, row, col)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		v, notice := e.CellValue(cell)
		return v, notice, nil
	}
	if row == e.Row && col == e.Col {
		v, notice := e.CurrentValue()
		return v, notice, nil
	}
	if e.Resolver == nil {
		return EvalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
	}
	cell, available, err := e.Resolver(row, col)
	if err != nil {
		var unavailable *DependencyUnavailableError
		if errors.As(err, &unavailable) {
			return EvalValue{}, unavailable.Reason, nil
		}
		return EvalValue{}, "", err
	}
	if !available {
		return EvalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
	}
	v, notice := e.CellValue(cell)
	return v, notice, nil
}

func (e *Engine) loadRange(node *Expression[EvalValue], anchorRow, anchorCol int) (RangeData, EvalValue, string, error) {
	if node.Kind != ReferenceCell && node.Kind != Range {
		return RangeData{}, ErrorValue("#VALUE!"), "", nil
	}
	r1, c1, r2, c2, ok := e.ResolveRangeBounds(node, anchorRow, anchorCol)
	if !ok {
		return RangeData{}, ErrorValue("#REF!"), "", nil
	}
	if node.Ref.ForeignSheet != "" {
		if r1 != r2 || c1 != c2 {
			return RangeData{}, EvalValue{}, "跨表求和区域超出定义名称的单元格", nil
		}
		cell, notice, err := e.readForeignCell(node.Ref.ForeignSheet, r1, c1)
		if notice != "" || err != nil {
			return RangeData{}, EvalValue{}, notice, err
		}
		data := RangeData{R1: r1, C1: c1, R2: r2, C2: c2}
		if cell.Raw == "" && cell.Type == "" && !cell.CacheMissing {
			data.BlankCount = 1
		} else {
			data.Cells = []RangeCell{{Row: r1, Col: c1, Cell: cell}}
		}
		return data, EvalValue{}, "", nil
	}
	if e.RangeResolver != nil {
		data, notice, err := e.RangeResolver(r1, c1, r2, c2)
		if err != nil {
			var unavailable *DependencyUnavailableError
			if errors.As(err, &unavailable) {
				return RangeData{}, EvalValue{}, unavailable.Reason, nil
			}
			return RangeData{}, EvalValue{}, "", err
		}
		return data, EvalValue{}, notice, nil
	}
	total := int64(r2-r1+1) * int64(c2-c1+1)
	if total > int64(formats.ConditionalDependencyReads) {
		return RangeData{}, EvalValue{}, "条件格式引用范围超过本次读取容量，未扫描整表", nil
	}
	out := RangeData{R1: r1, C1: c1, R2: r2, C2: c2}
	for r := r1; r <= r2; r++ {
		for c := c1; c <= c2; c++ {
			if !e.Spend(1) {
				return RangeData{}, EvalValue{}, WorkNotice, nil
			}
			var cell CellOperand
			if r == e.Row && c == e.Col {
				cell = e.Cell
			} else {
				if e.Resolver == nil {
					return RangeData{}, EvalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
				}
				var available bool
				var err error
				cell, available, err = e.Resolver(r, c)
				if err != nil {
					var unavailable *DependencyUnavailableError
					if errors.As(err, &unavailable) {
						return RangeData{}, EvalValue{}, unavailable.Reason, nil
					}
					return RangeData{}, EvalValue{}, "", err
				}
				if !available {
					return RangeData{}, EvalValue{}, "引用的源单元格尚未读取，不能用空白或零替代", nil
				}
			}
			if !cell.CacheMissing && cell.Type == "" && cell.Raw == "" {
				out.BlankCount++
				continue
			}
			out.Cells = append(out.Cells, RangeCell{Row: r, Col: c, Cell: cell})
		}
	}
	return out, EvalValue{}, "", nil
}

func (e *Engine) evalBinary(node *Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	left, notice, err := e.Eval(node.Args[0], anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	right, notice, err := e.Eval(node.Args[1], anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if left.Kind == "error" {
		return left, "", nil
	}
	if right.Kind == "error" {
		return right, "", nil
	}
	switch node.Op {
	case "=", "<>", "<", ">", "<=", ">=":
		cmp, valid := CompareValues(left, right)
		if !valid {
			return ErrorValue("#VALUE!"), "", nil
		}
		return BoolValue(excelformula.MatchesComparison(cmp, node.Op)), "", nil
	case "&":
		a, okA := e.asText(left)
		if !okA {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		b, okB := e.asText(right)
		if !okB {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		if len(a)+len(b) > formats.ConditionalOperandBytes || len(a)+len(b) > e.TextBytes {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		e.TextBytes -= len(a) + len(b)
		return TextValue(a + b), "", nil
	case "+", "-", "*", "/", "^":
		ar, errA := AsRat(left)
		if errA != nil {
			return *errA, "", nil
		}
		br, errB := AsRat(right)
		if errB != nil {
			return *errB, "", nil
		}
		af, _ := ar.Float64()
		bf, _ := br.Float64()
		switch node.Op {
		case "+":
			return FloatResult(af + bf), "", nil
		case "-":
			return FloatResult(af - bf), "", nil
		case "*":
			return FloatResult(af * bf), "", nil
		case "/":
			if bf == 0 {
				return ErrorValue("#DIV/0!"), "", nil
			}
			return FloatResult(af / bf), "", nil
		case "^":
			return Power(ar, br), "", nil
		}
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}

func (e *Engine) asText(v EvalValue) (string, bool) {
	var s string
	switch v.Kind {
	case "text":
		s = v.Text
	case "blank":
		s = ""
	case "boolean":
		if v.Boolean {
			s = "TRUE"
		} else {
			s = "FALSE"
		}
	case "number":
		s = FormatDecimalText(v)
	case "error":
		s = v.Text
	}
	if len(s) > formats.ConditionalOperandBytes || len(s) > e.TextBytes {
		return "", false
	}
	return s, true
}
