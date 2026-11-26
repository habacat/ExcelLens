package conditional

import (
	"math"
	"math/big"
	"sort"
	"strings"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

func (e *Engine) evalAggregate(op string, args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	if op == "COUNTBLANK" {
		data, refErr, notice, err := e.loadRange(args[0], anchorRow, anchorCol)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if refErr.Kind == "error" {
			return refErr, "", nil
		}
		blanks := data.BlankCount
		for _, item := range data.Cells {
			if !e.Spend(1) {
				return EvalValue{}, WorkNotice, nil
			}
			v, notice := e.CellValue(item.Cell)
			if notice != "" {
				return EvalValue{}, notice, nil
			}
			if v.Kind == "blank" || (v.Kind == "text" && v.Text == "") {
				blanks++
			}
		}
		return IntValue(blanks), "", nil
	}
	sum := new(big.Rat)
	prod := big.NewRat(1, 1)
	var minVal, maxVal *big.Rat
	var count, nonEmpty int64
	addNumber := func(r *big.Rat) bool {
		count++
		if (op == "SUM" || op == "AVERAGE") && sum != nil {
			if !excelformula.AddFloat(sum, r) {
				sum = nil
			}
		}
		if op == "PRODUCT" && prod != nil {
			prod.Mul(prod, r)
			if value, _ := prod.Float64(); math.IsInf(value, 0) {
				prod = nil // Excel reports #NUM! even if a later factor is zero.
			} else if prod.Num().BitLen() > formats.ConditionalArithBits || prod.Denom().BitLen() > formats.ConditionalArithBits {
				return false
			}
		}
		if op == "MIN" && (minVal == nil || r.Cmp(minVal) < 0) {
			minVal = new(big.Rat).Set(r)
		}
		if op == "MAX" && (maxVal == nil || r.Cmp(maxVal) > 0) {
			maxVal = new(big.Rat).Set(r)
		}
		return true
	}
	for _, arg := range args {
		if arg.Kind == Range {
			data, refErr, notice, err := e.loadRange(arg, anchorRow, anchorCol)
			if notice != "" || err != nil {
				return EvalValue{}, notice, err
			}
			if refErr.Kind == "error" {
				return refErr, "", nil
			}
			for _, item := range data.Cells {
				if !e.Spend(1) {
					return EvalValue{}, WorkNotice, nil
				}
				v, notice := e.CellValue(item.Cell)
				if notice != "" {
					return EvalValue{}, notice, nil
				}
				if op == "COUNTA" {
					if v.Kind != "blank" {
						nonEmpty++
					}
					continue
				}
				if v.Kind == "error" {
					if op == "COUNT" {
						continue
					}
					return v, "", nil
				}
				if v.Kind == "number" {
					r, ok := ValueToRat(v)
					if !ok {
						return ErrorValue("#NUM!"), "", nil
					}
					if !addNumber(r) {
						return EvalValue{}, "条件格式超过本次数值计算容量", nil
					}
				}
			}
			continue
		}
		v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if op == "COUNTA" {
			if v.Kind != "blank" {
				nonEmpty++
			}
			continue
		}
		if v.Kind == "error" {
			if op == "COUNT" {
				continue
			}
			return v, "", nil
		}
		if arg.Kind == ReferenceCell {
			// Single cell references in SUM/MIN/MAX/COUNT ignore text, booleans and blanks.
			if v.Kind == "number" {
				r, ok := ValueToRat(v)
				if !ok {
					return ErrorValue("#NUM!"), "", nil
				}
				if !addNumber(r) {
					return EvalValue{}, "条件格式超过本次数值计算容量", nil
				}
			}
			continue
		}
		// Direct scalar arguments coerce booleans and numeric text.
		if v.Kind == "blank" {
			if op != "COUNT" {
				if !addNumber(new(big.Rat)) {
					return EvalValue{}, "条件格式超过本次数值计算容量", nil
				}
			}
			continue
		}
		r, errVal := AsRat(v)
		if errVal != nil {
			if op == "COUNT" {
				continue
			}
			return *errVal, "", nil
		}
		if !addNumber(r) {
			return EvalValue{}, "条件格式超过本次数值计算容量", nil
		}
	}
	switch op {
	case "COUNT":
		return IntValue(count), "", nil
	case "COUNTA":
		return IntValue(nonEmpty), "", nil
	case "SUM":
		if sum == nil {
			return ErrorValue("#NUM!"), "", nil
		}
		return SumResult(sum), "", nil
	case "AVERAGE":
		if count == 0 {
			return ErrorValue("#DIV/0!"), "", nil
		}
		if sum == nil {
			return ErrorValue("#NUM!"), "", nil
		}
		return SumResult(sum.Quo(sum, big.NewRat(count, 1))), "", nil
	case "MIN":
		if minVal == nil {
			return IntValue(0), "", nil
		}
		return RatToValue(minVal), "", nil
	case "MAX":
		if maxVal == nil {
			return IntValue(0), "", nil
		}
		return RatToValue(maxVal), "", nil
	case "PRODUCT":
		if prod == nil {
			return ErrorValue("#NUM!"), "", nil
		}
		if count == 0 {
			return IntValue(0), "", nil
		}
		return RatToValue(prod), "", nil
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}

// Criterion matches a single cell value according to COUNTIF/SUMIF semantics.
type Criterion func(v EvalValue) (bool, string)

func (e *Engine) CompileCriterion(v EvalValue) Criterion {
	if v.Kind == "error" {
		return func(x EvalValue) (bool, string) {
			return x.Kind == "error" && strings.EqualFold(x.Text, v.Text), ""
		}
	}
	op := "="
	rhs := v
	if v.Kind == "blank" {
		rhs = IntValue(0)
	}
	if v.Kind == "text" {
		s := v.Text
		for _, prefix := range []string{"<=", ">=", "<>", "<", ">", "="} {
			if strings.HasPrefix(s, prefix) {
				op = prefix
				s = s[len(prefix):]
				break
			}
		}
		if s == "" {
			switch op {
			case "=":
				return func(x EvalValue) (bool, string) {
					return x.Kind == "blank" || (x.Kind == "text" && x.Text == ""), ""
				}
			case "<>":
				return func(x EvalValue) (bool, string) {
					return x.Kind != "blank" && !(x.Kind == "text" && x.Text == ""), ""
				}
			}
		}
		rhs = TextValue(s)
		if d, ok := ParseDecimal(strings.TrimSpace(s)); ok && s != "" {
			rhs = NumberValue(d)
		} else if strings.EqualFold(s, "TRUE") {
			rhs = BoolValue(true)
		} else if strings.EqualFold(s, "FALSE") {
			rhs = BoolValue(false)
		} else if strings.ContainsAny(s, "*?~") && (op == "=" || op == "<>") {
			tokens := excelformula.Wildcard(s, true)
			return func(x EvalValue) (bool, string) {
				matched := false
				if x.Kind == "text" {
					pos, complete := e.WildcardPosition(tokens, []rune(x.Text), false)
					if !complete {
						return false, WorkNotice
					}
					matched = pos >= 0
				}
				if op == "<>" {
					return !matched, ""
				}
				return matched, ""
			}
		}
	}
	return func(x EvalValue) (bool, string) {
		if x.Kind == "error" {
			matched := rhs.Kind == "text" && strings.EqualFold(x.Text, rhs.Text)
			return op == "=" && matched || op == "<>" && !matched, ""
		}
		if rhs.Kind == "number" {
			if x.Kind == "text" {
				if d, ok := ParseDecimal(strings.TrimSpace(x.Text)); ok {
					x = NumberValue(d)
				} else {
					return op == "<>", ""
				}
			}
			if x.Kind != "number" {
				return op == "<>", ""
			}
		} else if rhs.Kind == "text" {
			if x.Kind == "blank" {
				x = TextValue("")
			} else if x.Kind != "text" {
				return op == "<>", ""
			}
		} else if rhs.Kind == "boolean" {
			if x.Kind != "boolean" {
				return op == "<>", ""
			}
		}
		cmp, valid := CompareValues(x, rhs)
		return valid && excelformula.MatchesComparison(cmp, op), ""
	}
}

func (e *Engine) evalCountIf(args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	data, refErr, notice, err := e.loadRange(args[0], anchorRow, anchorCol)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if refErr.Kind == "error" {
		return refErr, "", nil
	}
	critVal, notice, err := e.Eval(args[1], anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	crit := e.CompileCriterion(critVal)
	var count int64
	if data.BlankCount > 0 {
		matched, notice := crit(BlankValue())
		if notice != "" {
			return EvalValue{}, notice, nil
		}
		if matched {
			count += data.BlankCount
		}
	}
	for _, item := range data.Cells {
		if !e.Spend(1) {
			return EvalValue{}, WorkNotice, nil
		}
		v, notice := e.CellValue(item.Cell)
		if notice != "" {
			return EvalValue{}, notice, nil
		}
		matched, notice := crit(v)
		if notice != "" {
			return EvalValue{}, notice, nil
		}
		if matched {
			count++
		}
	}
	return IntValue(count), "", nil
}

func (e *Engine) evalCountIfs(args []*Expression[EvalValue], anchorRow, anchorCol int, groupRanges []Area) (EvalValue, string, error) {
	pairs := len(args) / 2
	ranges := make([]RangeData, pairs)
	crits := make([]Criterion, pairs)
	retained := 0
	var rows, cols int
	for i := 0; i < pairs; i++ {
		data, refErr, notice, err := e.loadRange(args[2*i], anchorRow, anchorCol)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if refErr.Kind == "error" {
			return refErr, "", nil
		}
		h, w := data.R2-data.R1+1, data.C2-data.C1+1
		if i == 0 {
			rows, cols = h, w
		} else if h != rows || w != cols {
			return ErrorValue("#VALUE!"), "", nil
		}
		critVal, notice, err := e.Eval(args[2*i+1], anchorRow, anchorCol, groupRanges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		retained += len(data.Cells)
		if retained > formats.ConditionalRangeCells {
			return EvalValue{}, "条件格式多范围超过本次读取容量", nil
		}
		ranges[i] = data
		crits[i] = e.CompileCriterion(critVal)
	}
	return e.evalMultiCriteria(ranges, crits, nil)
}

func (e *Engine) evalSumIf(args []*Expression[EvalValue], anchorRow, anchorCol int, groupRanges []Area) (EvalValue, string, error) {
	critRange, refErr, notice, err := e.loadRange(args[0], anchorRow, anchorCol)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if refErr.Kind == "error" {
		return refErr, "", nil
	}
	critVal, notice, err := e.Eval(args[1], anchorRow, anchorCol, groupRanges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	crit := e.CompileCriterion(critVal)
	sumRange := critRange
	if len(args) == 3 {
		// Excel aligns sum_range from its top-left cell to match criteria_range dimensions.
		sr1, sc1, _, _, ok := e.ResolveRangeBounds(args[2], anchorRow, anchorCol)
		if !ok {
			return ErrorValue("#REF!"), "", nil
		}
		sr2 := sr1 + (critRange.R2 - critRange.R1)
		sc2 := sc1 + (critRange.C2 - critRange.C1)
		if sr2 > formats.ExcelRows || sc2 > formats.ExcelColumns {
			return ErrorValue("#REF!"), "", nil
		}
		sumExpr := &Expression[EvalValue]{
			Kind:   Range,
			Ref:    Reference{ForeignSheet: args[2].Ref.ForeignSheet, Row: sr1, Col: sc1, AbsoluteRow: true, AbsoluteCol: true},
			EndRef: Reference{Row: sr2, Col: sc2, AbsoluteRow: true, AbsoluteCol: true},
		}
		sumRange, refErr, notice, err = e.loadRange(sumExpr, anchorRow, anchorCol)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if refErr.Kind == "error" {
			return refErr, "", nil
		}
	}
	return e.evalMultiCriteria([]RangeData{critRange}, []Criterion{crit}, &sumRange)
}

func (e *Engine) evalSumIfs(args []*Expression[EvalValue], anchorRow, anchorCol int, groupRanges []Area) (EvalValue, string, error) {
	sumRange, refErr, notice, err := e.loadRange(args[0], anchorRow, anchorCol)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if refErr.Kind == "error" {
		return refErr, "", nil
	}
	rows, cols := sumRange.R2-sumRange.R1+1, sumRange.C2-sumRange.C1+1
	retained := len(sumRange.Cells)
	pairs := (len(args) - 1) / 2
	ranges := make([]RangeData, pairs)
	crits := make([]Criterion, pairs)
	for i := 0; i < pairs; i++ {
		data, refErr, notice, err := e.loadRange(args[1+2*i], anchorRow, anchorCol)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if refErr.Kind == "error" {
			return refErr, "", nil
		}
		if data.R2-data.R1+1 != rows || data.C2-data.C1+1 != cols {
			return ErrorValue("#VALUE!"), "", nil
		}
		critVal, notice, err := e.Eval(args[2+2*i], anchorRow, anchorCol, groupRanges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		retained += len(data.Cells)
		if retained > formats.ConditionalRangeCells {
			return EvalValue{}, "条件格式多范围超过本次读取容量", nil
		}
		ranges[i] = data
		crits[i] = e.CompileCriterion(critVal)
	}
	return e.evalMultiCriteria(ranges, crits, &sumRange)
}

// Sparse multi-criteria evaluation for COUNTIFS / SUMIF / SUMIFS. Occupied
// positions are traversed in deterministic row-major order so error propagation
// and budget exhaustion never depend on Go map iteration order.
func (e *Engine) evalMultiCriteria(ranges []RangeData, crits []Criterion, sumRange *RangeData) (EvalValue, string, error) {
	type relPos struct{ dr, dc int }
	maps := make([]map[relPos]*CellOperand, len(ranges))
	positions := make(map[relPos]struct{})
	for i, r := range ranges {
		m := make(map[relPos]*CellOperand, len(r.Cells))
		for j := range r.Cells {
			item := &r.Cells[j]
			p := relPos{item.Row - r.R1, item.Col - r.C1}
			m[p] = &item.Cell
			positions[p] = struct{}{}
		}
		maps[i] = m
	}
	var sumMap map[relPos]*CellOperand
	if sumRange != nil {
		sumMap = make(map[relPos]*CellOperand, len(sumRange.Cells))
		for j := range sumRange.Cells {
			item := &sumRange.Cells[j]
			p := relPos{item.Row - sumRange.R1, item.Col - sumRange.C1}
			sumMap[p] = &item.Cell
			positions[p] = struct{}{}
		}
	}
	ordered := make([]relPos, 0, len(positions))
	for p := range positions {
		ordered = append(ordered, p)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].dr != ordered[j].dr {
			return ordered[i].dr < ordered[j].dr
		}
		return ordered[i].dc < ordered[j].dc
	})
	rows := int64(ranges[0].R2 - ranges[0].R1 + 1)
	cols := int64(ranges[0].C2 - ranges[0].C1 + 1)
	allBlankCount := rows*cols - int64(len(ordered))
	var count int64
	sum := new(big.Rat)
	if sumRange == nil && allBlankCount > 0 {
		allMatch := true
		for _, crit := range crits {
			matches, notice := crit(BlankValue())
			if notice != "" {
				return EvalValue{}, notice, nil
			}
			if !matches {
				allMatch = false
				break
			}
		}
		if allMatch {
			count += allBlankCount
		}
	}
	for _, p := range ordered {
		if !e.Spend(len(ranges)) {
			return EvalValue{}, WorkNotice, nil
		}
		matched := true
		for i := range ranges {
			cell, ok := maps[i][p]
			var v EvalValue
			if ok {
				var notice string
				v, notice = e.CellValue(*cell)
				if notice != "" {
					return EvalValue{}, notice, nil
				}
			} else {
				v = BlankValue()
			}
			matches, notice := crits[i](v)
			if notice != "" {
				return EvalValue{}, notice, nil
			}
			if !matches {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if sumRange == nil {
			count++
		} else if cell, ok := sumMap[p]; ok {
			v, notice := e.CellValue(*cell)
			if notice != "" {
				return EvalValue{}, notice, nil
			}
			if v.Kind == "error" {
				return v, "", nil
			}
			if v.Kind == "number" {
				r, ok := ValueToRat(v)
				if !ok {
					return ErrorValue("#NUM!"), "", nil
				}
				if !excelformula.AddFloat(sum, r) {
					return ErrorValue("#NUM!"), "", nil
				}
			}
		}
	}
	if sumRange == nil {
		return IntValue(count), "", nil
	}
	return SumResult(sum), "", nil
}

func (e *Engine) evalMatch(args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	lookup, notice, err := e.Eval(args[0], anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if lookup.Kind == "error" {
		return lookup, "", nil
	}
	if lookup.Kind == "blank" {
		lookup = IntValue(0)
	}
	data, refErr, notice, err := e.loadRange(args[1], anchorRow, anchorCol)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if refErr.Kind == "error" {
		return refErr, "", nil
	}
	rows, cols := data.R2-data.R1+1, data.C2-data.C1+1
	if rows > 1 && cols > 1 {
		return ErrorValue("#N/A"), "", nil
	}
	matchType := int64(1)
	if len(args) == 3 {
		mv, notice, err := e.Eval(args[2], anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		mr, errVal := AsRat(mv)
		if errVal != nil {
			return *errVal, "", nil
		}
		matchType = int64(mr.Sign())
	}
	vertical := cols == 1
	indexOf := func(item RangeCell) int {
		if vertical {
			return item.Row - data.R1 + 1
		}
		return item.Col - data.C1 + 1
	}
	if matchType == 0 {
		var wild []excelformula.Token
		if lookup.Kind == "text" && strings.ContainsAny(lookup.Text, "*?~") {
			wild = excelformula.Wildcard(lookup.Text, false)
		}
		for _, item := range data.Cells {
			if !e.Spend(1) {
				return EvalValue{}, WorkNotice, nil
			}
			v, notice := e.CellValue(item.Cell)
			if notice != "" {
				return EvalValue{}, notice, nil
			}
			if v.Kind != lookup.Kind {
				continue
			}
			if wild != nil {
				pos, complete := e.WildcardPosition(wild, []rune(v.Text), false)
				if !complete {
					return EvalValue{}, WorkNotice, nil
				}
				if pos >= 0 {
					return IntValue(int64(indexOf(item))), "", nil
				}
				continue
			}
			if cmp, valid := CompareValues(v, lookup); valid && cmp == 0 {
				return IntValue(int64(indexOf(item))), "", nil
			}
		}
		return ErrorValue("#N/A"), "", nil
	}
	// Ordered match (1 ascending, -1 descending): scan occupied cells of matching type.
	best := 0
	for _, item := range data.Cells {
		if !e.Spend(1) {
			return EvalValue{}, WorkNotice, nil
		}
		v, notice := e.CellValue(item.Cell)
		if notice != "" {
			return EvalValue{}, notice, nil
		}
		if v.Kind != lookup.Kind {
			continue
		}
		cmp, valid := CompareValues(v, lookup)
		if !valid {
			continue
		}
		if matchType > 0 {
			if cmp <= 0 {
				best = indexOf(item)
			} else {
				break
			}
		} else {
			if cmp >= 0 {
				best = indexOf(item)
			} else {
				break
			}
		}
	}
	if best == 0 {
		return ErrorValue("#N/A"), "", nil
	}
	return IntValue(int64(best)), "", nil
}
