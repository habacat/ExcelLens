package workbook

import (
	"math"
	"math/big"
	"sort"
	"strings"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

func (e *ConditionalEvaluator) evalAggregate(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	if op == "COUNTBLANK" {
		data, refErr, notice, err := e.loadRange(args[0], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if refErr.kind == "error" {
			return refErr, "", nil
		}
		blanks := data.blankCount
		for _, item := range data.cells {
			if !e.spend(1) {
				return conditionalValue{}, "条件格式超过本次求值预算", nil
			}
			v, notice := e.value(item.cell)
			if notice != "" {
				return conditionalValue{}, notice, nil
			}
			if v.kind == "blank" || (v.kind == "text" && v.text == "") {
				blanks++
			}
		}
		return conditionalIntVal(blanks), "", nil
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
		if arg.Kind == cfExprRange {
			data, refErr, notice, err := e.loadRange(arg, group)
			if notice != "" || err != nil {
				return conditionalValue{}, notice, err
			}
			if refErr.kind == "error" {
				return refErr, "", nil
			}
			for _, item := range data.cells {
				if !e.spend(1) {
					return conditionalValue{}, "条件格式超过本次求值预算", nil
				}
				v, notice := e.value(item.cell)
				if notice != "" {
					return conditionalValue{}, notice, nil
				}
				if op == "COUNTA" {
					if v.kind != "blank" {
						nonEmpty++
					}
					continue
				}
				if v.kind == "error" {
					if op == "COUNT" {
						continue
					}
					return v, "", nil
				}
				if v.kind == "number" {
					r, ok := valueToRat(v)
					if !ok {
						return conditionalErrorVal("#NUM!"), "", nil
					}
					if !addNumber(r) {
						return conditionalValue{}, "条件格式超过本次数值计算容量", nil
					}
				}
			}
			continue
		}
		v, notice, err := e.evalExpr(arg, group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if op == "COUNTA" {
			if v.kind != "blank" {
				nonEmpty++
			}
			continue
		}
		if v.kind == "error" {
			if op == "COUNT" {
				continue
			}
			return v, "", nil
		}
		if arg.Kind == cfExprRef {
			// Single cell references in SUM/MIN/MAX/COUNT ignore text, booleans and blanks.
			if v.kind == "number" {
				r, ok := valueToRat(v)
				if !ok {
					return conditionalErrorVal("#NUM!"), "", nil
				}
				if !addNumber(r) {
					return conditionalValue{}, "条件格式超过本次数值计算容量", nil
				}
			}
			continue
		}
		// Direct scalar arguments coerce booleans and numeric text.
		if v.kind == "blank" {
			if op != "COUNT" {
				if !addNumber(new(big.Rat)) {
					return conditionalValue{}, "条件格式超过本次数值计算容量", nil
				}
			}
			continue
		}
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			if op == "COUNT" {
				continue
			}
			return *errVal, "", nil
		}
		if !addNumber(r) {
			return conditionalValue{}, "条件格式超过本次数值计算容量", nil
		}
	}
	switch op {
	case "COUNT":
		return conditionalIntVal(count), "", nil
	case "COUNTA":
		return conditionalIntVal(nonEmpty), "", nil
	case "SUM":
		if sum == nil {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return conditionalSumResult(sum), "", nil
	case "AVERAGE":
		if count == 0 {
			return conditionalErrorVal("#DIV/0!"), "", nil
		}
		if sum == nil {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return conditionalSumResult(sum.Quo(sum, big.NewRat(count, 1))), "", nil
	case "MIN":
		if minVal == nil {
			return conditionalIntVal(0), "", nil
		}
		return ratToConditionalValue(minVal), "", nil
	case "MAX":
		if maxVal == nil {
			return conditionalIntVal(0), "", nil
		}
		return ratToConditionalValue(maxVal), "", nil
	case "PRODUCT":
		if prod == nil {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		if count == 0 {
			return conditionalIntVal(0), "", nil
		}
		return ratToConditionalValue(prod), "", nil
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

type cfCriterion func(v conditionalValue) (bool, string)

func (e *ConditionalEvaluator) compileCFCriterion(v conditionalValue) cfCriterion {
	if v.kind == "error" {
		return func(x conditionalValue) (bool, string) {
			return x.kind == "error" && strings.EqualFold(x.text, v.text), ""
		}
	}
	op := "="
	rhs := v
	if v.kind == "blank" {
		rhs = conditionalIntVal(0)
	}
	if v.kind == "text" {
		s := v.text
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
				return func(x conditionalValue) (bool, string) {
					return x.kind == "blank" || (x.kind == "text" && x.text == ""), ""
				}
			case "<>":
				return func(x conditionalValue) (bool, string) {
					return x.kind != "blank" && !(x.kind == "text" && x.text == ""), ""
				}
			}
		}
		rhs = conditionalText(s)
		if d, ok := parseConditionalDecimal(strings.TrimSpace(s)); ok && s != "" {
			rhs = conditionalNumberVal(d)
		} else if strings.EqualFold(s, "TRUE") {
			rhs = conditionalBoolVal(true)
		} else if strings.EqualFold(s, "FALSE") {
			rhs = conditionalBoolVal(false)
		} else if strings.ContainsAny(s, "*?~") && (op == "=" || op == "<>") {
			tokens := excelformula.Wildcard(s, true)
			return func(x conditionalValue) (bool, string) {
				matched := false
				if x.kind == "text" {
					pos, complete := e.wildcardPosition(tokens, []rune(x.text), false)
					if !complete {
						return false, conditionalWorkNotice
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
	return func(x conditionalValue) (bool, string) {
		if x.kind == "error" {
			matched := rhs.kind == "text" && strings.EqualFold(x.text, rhs.text)
			return op == "=" && matched || op == "<>" && !matched, ""
		}
		if rhs.kind == "number" {
			if x.kind == "text" {
				if d, ok := parseConditionalDecimal(strings.TrimSpace(x.text)); ok {
					x = conditionalNumberVal(d)
				} else {
					return op == "<>", ""
				}
			}
			if x.kind != "number" {
				return op == "<>", ""
			}
		} else if rhs.kind == "text" {
			if x.kind == "blank" {
				x = conditionalText("")
			} else if x.kind != "text" {
				return op == "<>", ""
			}
		} else if rhs.kind == "boolean" {
			if x.kind != "boolean" {
				return op == "<>", ""
			}
		}
		cmp, valid := compareConditionalValues(x, rhs)
		return valid && matchConditionalComparison(cmp, op), ""
	}
}

func (e *ConditionalEvaluator) evalCountIf(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	data, refErr, notice, err := e.loadRange(args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if refErr.kind == "error" {
		return refErr, "", nil
	}
	critVal, notice, err := e.evalExpr(args[1], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	crit := e.compileCFCriterion(critVal)
	var count int64
	if data.blankCount > 0 {
		matched, notice := crit(conditionalBlankVal())
		if notice != "" {
			return conditionalValue{}, notice, nil
		}
		if matched {
			count += data.blankCount
		}
	}
	for _, item := range data.cells {
		if !e.spend(1) {
			return conditionalValue{}, "条件格式超过本次求值预算", nil
		}
		v, notice := e.value(item.cell)
		if notice != "" {
			return conditionalValue{}, notice, nil
		}
		matched, notice := crit(v)
		if notice != "" {
			return conditionalValue{}, notice, nil
		}
		if matched {
			count++
		}
	}
	return conditionalIntVal(count), "", nil
}

func (e *ConditionalEvaluator) evalCountIfs(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	pairs := len(args) / 2
	ranges := make([]conditionalRangeData, pairs)
	crits := make([]cfCriterion, pairs)
	retained := 0
	var rows, cols int
	for i := 0; i < pairs; i++ {
		data, refErr, notice, err := e.loadRange(args[2*i], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if refErr.kind == "error" {
			return refErr, "", nil
		}
		h, w := data.r2-data.r1+1, data.c2-data.c1+1
		if i == 0 {
			rows, cols = h, w
		} else if h != rows || w != cols {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		critVal, notice, err := e.evalExpr(args[2*i+1], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		retained += len(data.cells)
		if retained > formats.ConditionalRangeCells {
			return conditionalValue{}, "条件格式多范围超过本次读取容量", nil
		}
		ranges[i] = data
		crits[i] = e.compileCFCriterion(critVal)
	}
	return e.evalMultiCriteria(ranges, crits, nil)
}

func (e *ConditionalEvaluator) evalSumIf(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	critRange, refErr, notice, err := e.loadRange(args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if refErr.kind == "error" {
		return refErr, "", nil
	}
	critVal, notice, err := e.evalExpr(args[1], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	crit := e.compileCFCriterion(critVal)
	sumRange := critRange
	if len(args) == 3 {
		// Excel aligns sum_range from its top-left cell to match criteria_range dimensions.
		sr1, sc1, _, _, ok := e.resolveRangeBounds(args[2], group)
		if !ok {
			return conditionalErrorVal("#REF!"), "", nil
		}
		sr2 := sr1 + (critRange.r2 - critRange.r1)
		sc2 := sc1 + (critRange.c2 - critRange.c1)
		if sr2 > formats.ExcelRows || sc2 > formats.ExcelColumns {
			return conditionalErrorVal("#REF!"), "", nil
		}
		sumExpr := &conditionalExpr{
			Kind:   cfExprRange,
			Ref:    conditionalReference{ForeignSheet: args[2].Ref.ForeignSheet, Row: sr1, Col: sc1, AbsoluteRow: true, AbsoluteCol: true},
			EndRef: conditionalReference{Row: sr2, Col: sc2, AbsoluteRow: true, AbsoluteCol: true},
		}
		sumRange, refErr, notice, err = e.loadRange(sumExpr, group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if refErr.kind == "error" {
			return refErr, "", nil
		}
	}
	return e.evalMultiCriteria([]conditionalRangeData{critRange}, []cfCriterion{crit}, &sumRange)
}

func (e *ConditionalEvaluator) evalSumIfs(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	sumRange, refErr, notice, err := e.loadRange(args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if refErr.kind == "error" {
		return refErr, "", nil
	}
	rows, cols := sumRange.r2-sumRange.r1+1, sumRange.c2-sumRange.c1+1
	retained := len(sumRange.cells)
	pairs := (len(args) - 1) / 2
	ranges := make([]conditionalRangeData, pairs)
	crits := make([]cfCriterion, pairs)
	for i := 0; i < pairs; i++ {
		data, refErr, notice, err := e.loadRange(args[1+2*i], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if refErr.kind == "error" {
			return refErr, "", nil
		}
		if data.r2-data.r1+1 != rows || data.c2-data.c1+1 != cols {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		critVal, notice, err := e.evalExpr(args[2+2*i], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		retained += len(data.cells)
		if retained > formats.ConditionalRangeCells {
			return conditionalValue{}, "条件格式多范围超过本次读取容量", nil
		}
		ranges[i] = data
		crits[i] = e.compileCFCriterion(critVal)
	}
	return e.evalMultiCriteria(ranges, crits, &sumRange)
}

// Sparse multi-criteria evaluation for COUNTIFS / SUMIF / SUMIFS. Occupied
// positions are traversed in deterministic row-major order so error propagation
// and budget exhaustion never depend on Go map iteration order.
func (e *ConditionalEvaluator) evalMultiCriteria(ranges []conditionalRangeData, crits []cfCriterion, sumRange *conditionalRangeData) (conditionalValue, string, error) {
	type relPos struct{ dr, dc int }
	maps := make([]map[relPos]*CellData, len(ranges))
	positions := make(map[relPos]struct{})
	for i, r := range ranges {
		m := make(map[relPos]*CellData, len(r.cells))
		for j := range r.cells {
			item := &r.cells[j]
			p := relPos{item.row - r.r1, item.col - r.c1}
			m[p] = &item.cell
			positions[p] = struct{}{}
		}
		maps[i] = m
	}
	var sumMap map[relPos]*CellData
	if sumRange != nil {
		sumMap = make(map[relPos]*CellData, len(sumRange.cells))
		for j := range sumRange.cells {
			item := &sumRange.cells[j]
			p := relPos{item.row - sumRange.r1, item.col - sumRange.c1}
			sumMap[p] = &item.cell
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
	rows := int64(ranges[0].r2 - ranges[0].r1 + 1)
	cols := int64(ranges[0].c2 - ranges[0].c1 + 1)
	allBlankCount := rows*cols - int64(len(ordered))
	var count int64
	sum := new(big.Rat)
	if sumRange == nil && allBlankCount > 0 {
		allMatch := true
		for _, crit := range crits {
			matches, notice := crit(conditionalBlankVal())
			if notice != "" {
				return conditionalValue{}, notice, nil
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
		if !e.spend(len(ranges)) {
			return conditionalValue{}, "条件格式超过本次求值预算", nil
		}
		matched := true
		for i := range ranges {
			cell, ok := maps[i][p]
			var v conditionalValue
			if ok {
				var notice string
				v, notice = e.value(*cell)
				if notice != "" {
					return conditionalValue{}, notice, nil
				}
			} else {
				v = conditionalBlankVal()
			}
			matches, notice := crits[i](v)
			if notice != "" {
				return conditionalValue{}, notice, nil
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
			v, notice := e.value(*cell)
			if notice != "" {
				return conditionalValue{}, notice, nil
			}
			if v.kind == "error" {
				return v, "", nil
			}
			if v.kind == "number" {
				r, ok := valueToRat(v)
				if !ok {
					return conditionalErrorVal("#NUM!"), "", nil
				}
				if !excelformula.AddFloat(sum, r) {
					return conditionalErrorVal("#NUM!"), "", nil
				}
			}
		}
	}
	if sumRange == nil {
		return conditionalIntVal(count), "", nil
	}
	return conditionalSumResult(sum), "", nil
}

func (e *ConditionalEvaluator) evalMatch(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	lookup, notice, err := e.evalExpr(args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if lookup.kind == "error" {
		return lookup, "", nil
	}
	if lookup.kind == "blank" {
		lookup = conditionalIntVal(0)
	}
	data, refErr, notice, err := e.loadRange(args[1], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if refErr.kind == "error" {
		return refErr, "", nil
	}
	rows, cols := data.r2-data.r1+1, data.c2-data.c1+1
	if rows > 1 && cols > 1 {
		return conditionalErrorVal("#N/A"), "", nil
	}
	matchType := int64(1)
	if len(args) == 3 {
		mv, notice, err := e.evalExpr(args[2], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		mr, errVal := conditionalAsRat(mv)
		if errVal != nil {
			return *errVal, "", nil
		}
		matchType = int64(mr.Sign())
	}
	vertical := cols == 1
	indexOf := func(item conditionalRangeCell) int {
		if vertical {
			return item.row - data.r1 + 1
		}
		return item.col - data.c1 + 1
	}
	if matchType == 0 {
		var wild []excelformula.Token
		if lookup.kind == "text" && strings.ContainsAny(lookup.text, "*?~") {
			wild = excelformula.Wildcard(lookup.text, false)
		}
		for _, item := range data.cells {
			if !e.spend(1) {
				return conditionalValue{}, "条件格式超过本次求值预算", nil
			}
			v, notice := e.value(item.cell)
			if notice != "" {
				return conditionalValue{}, notice, nil
			}
			if v.kind != lookup.kind {
				continue
			}
			if wild != nil {
				pos, complete := e.wildcardPosition(wild, []rune(v.text), false)
				if !complete {
					return conditionalValue{}, conditionalWorkNotice, nil
				}
				if pos >= 0 {
					return conditionalIntVal(int64(indexOf(item))), "", nil
				}
				continue
			}
			if cmp, valid := compareConditionalValues(v, lookup); valid && cmp == 0 {
				return conditionalIntVal(int64(indexOf(item))), "", nil
			}
		}
		return conditionalErrorVal("#N/A"), "", nil
	}
	// Ordered match (1 ascending, -1 descending): scan occupied cells of matching type.
	best := 0
	for _, item := range data.cells {
		if !e.spend(1) {
			return conditionalValue{}, "条件格式超过本次求值预算", nil
		}
		v, notice := e.value(item.cell)
		if notice != "" {
			return conditionalValue{}, notice, nil
		}
		if v.kind != lookup.kind {
			continue
		}
		cmp, valid := compareConditionalValues(v, lookup)
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
		return conditionalErrorVal("#N/A"), "", nil
	}
	return conditionalIntVal(int64(best)), "", nil
}
