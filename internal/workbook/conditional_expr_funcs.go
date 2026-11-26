package workbook

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

func (e *ConditionalEvaluator) evalCall(node *conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	var indexIssue string
	if e.indexed != nil {
		switch node.Op {
		case "COUNTIF", "COUNTIFS", "SUMIF", "SUMIFS", "MATCH", "SUM":
			v, notice, err, handled := e.indexed(node, group)
			if handled || err != nil {
				return v, notice, err
			}
			indexIssue = notice
		}
	}
	v, notice, err := e.evalBuiltin(node, group)
	if indexIssue != "" && notice != "" {
		notice = indexIssue + "；" + notice
	}
	return v, notice, err
}

func (e *ConditionalEvaluator) evalBuiltin(node *conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	switch node.Op {
	case "TRUE":
		return conditionalBoolVal(true), "", nil
	case "FALSE":
		return conditionalBoolVal(false), "", nil
	case "NA":
		return conditionalErrorVal("#N/A"), "", nil
	case "IF":
		return e.evalIf(node.Args, group)
	case "IFS":
		return e.evalIfs(node.Args, group)
	case "IFERROR", "IFNA":
		return e.evalIfError(node.Op, node.Args, group)
	case "AND", "OR", "XOR":
		return e.evalLogical(node.Op, node.Args, group)
	case "NOT":
		v, notice, err := e.evalExpr(node.Args[0], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		b, errVal := conditionalAsBool(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		return conditionalBoolVal(!b), "", nil
	case "ISBLANK", "ISNUMBER", "ISTEXT", "ISNONTEXT", "ISLOGICAL", "ISERROR", "ISERR", "ISNA", "ISEVEN", "ISODD", "N", "T":
		return e.evalInfo(node.Op, node.Args[0], group)
	case "ROW", "COLUMN", "ROWS", "COLUMNS":
		return e.evalPosition(node.Op, node.Args, group)
	case "ABS", "SIGN", "INT", "TRUNC", "ROUND", "ROUNDUP", "ROUNDDOWN", "MOD", "QUOTIENT", "POWER", "SQRT":
		return e.evalMathScalar(node.Op, node.Args, group)
	case "SUM", "AVERAGE", "MIN", "MAX", "COUNT", "COUNTA", "COUNTBLANK", "PRODUCT":
		return e.evalAggregate(node.Op, node.Args, group)
	case "LEN", "TRIM", "CLEAN", "LEFT", "RIGHT", "MID", "UPPER", "LOWER", "EXACT", "FIND", "SEARCH", "SUBSTITUTE", "REPLACE", "REPT", "CONCAT", "CONCATENATE", "VALUE":
		return e.evalTextFunc(node.Op, node.Args, group)
	case "COUNTIF":
		return e.evalCountIf(node.Args, group)
	case "COUNTIFS":
		return e.evalCountIfs(node.Args, group)
	case "SUMIF":
		return e.evalSumIf(node.Args, group)
	case "SUMIFS":
		return e.evalSumIfs(node.Args, group)
	case "MATCH":
		return e.evalMatch(node.Args, group)
	case "DATE", "YEAR", "MONTH", "DAY", "WEEKDAY":
		return e.evalDateFunc(node.Op, node.Args, group)
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

func (e *ConditionalEvaluator) evalIf(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	cond, notice, err := e.evalExpr(args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	b, errVal := conditionalAsBool(cond)
	if errVal != nil {
		return *errVal, "", nil
	}
	if b {
		return e.evalExpr(args[1], group)
	}
	if len(args) == 3 {
		return e.evalExpr(args[2], group)
	}
	return conditionalBoolVal(false), "", nil
}

func (e *ConditionalEvaluator) evalIfs(args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	for i := 0; i < len(args); i += 2 {
		cond, notice, err := e.evalExpr(args[i], group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		b, errVal := conditionalAsBool(cond)
		if errVal != nil {
			return *errVal, "", nil
		}
		if b {
			return e.evalExpr(args[i+1], group)
		}
	}
	return conditionalErrorVal("#N/A"), "", nil
}

func (e *ConditionalEvaluator) evalIfError(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	v, notice, err := e.evalExpr(args[0], group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	if v.kind == "error" && (op == "IFERROR" || strings.EqualFold(v.text, "#N/A")) {
		return e.evalExpr(args[1], group)
	}
	return v, "", nil
}

func (e *ConditionalEvaluator) evalLogical(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	found := false
	result := op == "AND"
	xorCount := 0
	apply := func(b bool) {
		found = true
		switch op {
		case "AND":
			result = result && b
		case "OR":
			result = result || b
		case "XOR":
			if b {
				xorCount++
			}
		}
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
				switch v.kind {
				case "error":
					return v, "", nil
				case "boolean":
					apply(v.boolean)
				case "number":
					apply(conditionalValueIsNonZero(v))
				}
			}
			continue
		}
		v, notice, err := e.evalExpr(arg, group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		switch v.kind {
		case "error":
			return v, "", nil
		case "boolean":
			apply(v.boolean)
		case "number":
			apply(conditionalValueIsNonZero(v))
		case "blank":
			if arg.Kind != cfExprRef {
				apply(false)
			}
		case "text":
			if arg.Kind == cfExprRef {
				// Direct cell reference holding text is ignored in AND/OR/XOR in Excel.
				continue
			}
			switch strings.ToUpper(v.text) {
			case "TRUE":
				apply(true)
			case "FALSE":
				apply(false)
			default:
				return conditionalErrorVal("#VALUE!"), "", nil
			}
		}
	}
	if !found {
		return conditionalErrorVal("#VALUE!"), "", nil
	}
	if op == "XOR" {
		return conditionalBoolVal(xorCount%2 == 1), "", nil
	}
	return conditionalBoolVal(result), "", nil
}

func (e *ConditionalEvaluator) evalInfo(op string, arg *conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	v, notice, err := e.evalExpr(arg, group)
	if notice != "" || err != nil {
		return conditionalValue{}, notice, err
	}
	switch op {
	case "ISBLANK":
		return conditionalBoolVal(v.kind == "blank"), "", nil
	case "ISNUMBER":
		return conditionalBoolVal(v.kind == "number"), "", nil
	case "ISTEXT":
		return conditionalBoolVal(v.kind == "text"), "", nil
	case "ISNONTEXT":
		return conditionalBoolVal(v.kind != "text"), "", nil
	case "ISLOGICAL":
		return conditionalBoolVal(v.kind == "boolean"), "", nil
	case "ISERROR":
		return conditionalBoolVal(v.kind == "error"), "", nil
	case "ISERR":
		return conditionalBoolVal(v.kind == "error" && !strings.EqualFold(v.text, "#N/A")), "", nil
	case "ISNA":
		return conditionalBoolVal(v.kind == "error" && strings.EqualFold(v.text, "#N/A")), "", nil
	case "N":
		switch v.kind {
		case "error":
			return v, "", nil
		case "number":
			return v, "", nil
		case "boolean":
			if v.boolean {
				return conditionalIntVal(1), "", nil
			}
			return conditionalIntVal(0), "", nil
		default:
			return conditionalIntVal(0), "", nil
		}
	case "T":
		if v.kind == "error" || v.kind == "text" {
			return v, "", nil
		}
		return conditionalText(""), "", nil
	case "ISEVEN", "ISODD":
		if v.kind == "error" {
			return v, "", nil
		}
		if v.kind == "boolean" {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		q := new(big.Int).Quo(r.Num(), r.Denom())
		even := q.Bit(0) == 0
		if op == "ISODD" {
			even = !even
		}
		return conditionalBoolVal(even), "", nil
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

func (e *ConditionalEvaluator) evalPosition(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	switch op {
	case "ROW":
		if len(args) == 0 {
			return conditionalIntVal(int64(e.row)), "", nil
		}
		r1, _, _, _, ok := e.resolveRangeBounds(args[0], group)
		if !ok {
			return conditionalErrorVal("#REF!"), "", nil
		}
		return conditionalIntVal(int64(r1)), "", nil
	case "COLUMN":
		if len(args) == 0 {
			return conditionalIntVal(int64(e.col)), "", nil
		}
		_, c1, _, _, ok := e.resolveRangeBounds(args[0], group)
		if !ok {
			return conditionalErrorVal("#REF!"), "", nil
		}
		return conditionalIntVal(int64(c1)), "", nil
	case "ROWS":
		r1, _, r2, _, ok := e.resolveRangeBounds(args[0], group)
		if !ok {
			return conditionalErrorVal("#REF!"), "", nil
		}
		return conditionalIntVal(int64(r2 - r1 + 1)), "", nil
	case "COLUMNS":
		_, c1, _, c2, ok := e.resolveRangeBounds(args[0], group)
		if !ok {
			return conditionalErrorVal("#REF!"), "", nil
		}
		return conditionalIntVal(int64(c2 - c1 + 1)), "", nil
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

func (e *ConditionalEvaluator) evalMathScalar(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	vals := make([]*big.Rat, len(args))
	for i, arg := range args {
		v, notice, err := e.evalExpr(arg, group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		vals[i] = r
	}
	switch op {
	case "ABS":
		return ratToConditionalValue(vals[0].Abs(vals[0])), "", nil
	case "SIGN":
		return conditionalIntVal(int64(vals[0].Sign())), "", nil
	case "INT":
		return ratToConditionalValue(ratFloor(vals[0])), "", nil
	case "TRUNC":
		digits := int64(0)
		if len(vals) == 2 {
			d, ok := ratTruncInt64(vals[1])
			if !ok {
				return conditionalErrorVal("#NUM!"), "", nil
			}
			digits = d
		}
		return ratRound(vals[0], digits, "down"), "", nil
	case "ROUND":
		d, ok := ratTruncInt64(vals[1])
		if !ok {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return ratRound(vals[0], d, "half"), "", nil
	case "ROUNDUP":
		d, ok := ratTruncInt64(vals[1])
		if !ok {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return ratRound(vals[0], d, "up"), "", nil
	case "ROUNDDOWN":
		d, ok := ratTruncInt64(vals[1])
		if !ok {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return ratRound(vals[0], d, "down"), "", nil
	case "MOD":
		if vals[1].Sign() == 0 {
			return conditionalErrorVal("#DIV/0!"), "", nil
		}
		// Excel MOD(n, d) = n - d * INT(n / d)
		q := new(big.Rat).Quo(vals[0], vals[1])
		fl := ratFloor(q)
		res := new(big.Rat).Sub(vals[0], new(big.Rat).Mul(vals[1], fl))
		return ratToConditionalValue(res), "", nil
	case "QUOTIENT":
		if vals[1].Sign() == 0 {
			return conditionalErrorVal("#DIV/0!"), "", nil
		}
		q := new(big.Rat).Quo(vals[0], vals[1])
		i := new(big.Int).Quo(q.Num(), q.Denom())
		return ratToConditionalValue(new(big.Rat).SetInt(i)), "", nil
	case "POWER":
		return conditionalPower(vals[0], vals[1]), "", nil
	case "SQRT":
		if vals[0].Sign() < 0 {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		f, _ := vals[0].Float64()
		d, ok := parseConditionalDecimal(strconv.FormatFloat(math.Sqrt(f), 'g', 15, 64))
		if !ok {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return conditionalNumberVal(d), "", nil
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

func ratFloor(r *big.Rat) *big.Rat {
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if rem.Sign() < 0 {
		q.Sub(q, big.NewInt(1))
	}
	return new(big.Rat).SetInt(q)
}

func ratTruncInt64(r *big.Rat) (int64, bool) {
	q := new(big.Int).Quo(r.Num(), r.Denom())
	if !q.IsInt64() {
		return 0, false
	}
	v := q.Int64()
	if v < -formats.ConditionalArithExponent || v > formats.ConditionalArithExponent {
		return 0, false
	}
	return v, true
}

func ratRound(r *big.Rat, digits int64, mode string) conditionalValue {
	return ratToConditionalValue(excelformula.Round(r, digits, mode))
}
func (e *ConditionalEvaluator) evalTextFunc(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	if op == "CONCAT" || op == "CONCATENATE" {
		var b strings.Builder
		for _, arg := range args {
			if op == "CONCAT" && arg.Kind == cfExprRange {
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
					if v.kind == "error" {
						return v, "", nil
					}
					s, ok := e.asText(v)
					if !ok || b.Len()+len(s) > formats.ConditionalOperandBytes || b.Len()+len(s) > e.textBytes {
						return conditionalValue{}, "条件操作数超过本次文本容量", nil
					}
					b.WriteString(s)
				}
				continue
			}
			v, notice, err := e.evalExpr(arg, group)
			if notice != "" || err != nil {
				return conditionalValue{}, notice, err
			}
			if v.kind == "error" {
				return v, "", nil
			}
			s, ok := e.asText(v)
			if !ok || b.Len()+len(s) > formats.ConditionalOperandBytes || b.Len()+len(s) > e.textBytes {
				return conditionalValue{}, "条件操作数超过本次文本容量", nil
			}
			b.WriteString(s)
		}
		e.textBytes -= b.Len()
		return conditionalText(b.String()), "", nil
	}
	vals := make([]conditionalValue, len(args))
	for i, arg := range args {
		v, notice, err := e.evalExpr(arg, group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		if v.kind == "error" {
			return v, "", nil
		}
		vals[i] = v
	}
	textOf := func(v conditionalValue) (string, bool) { return e.asText(v) }
	intOf := func(v conditionalValue) (int64, *conditionalValue) {
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			return 0, errVal
		}
		q := new(big.Int).Quo(r.Num(), r.Denom())
		if !q.IsInt64() {
			ev := conditionalErrorVal("#VALUE!")
			return 0, &ev
		}
		return q.Int64(), nil
	}
	switch op {
	case "LEN":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		return conditionalIntVal(int64(utf8.RuneCountInString(s))), "", nil
	case "TRIM":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		return conditionalText(excelTrim(s)), "", nil
	case "CLEAN":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		return conditionalText(strings.Map(func(r rune) rune {
			if r < 32 {
				return -1
			}
			return r
		}, s)), "", nil
	case "UPPER":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		return conditionalText(strings.ToUpper(s)), "", nil
	case "LOWER":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		return conditionalText(strings.ToLower(s)), "", nil
	case "EXACT":
		a, okA := textOf(vals[0])
		b, okB := textOf(vals[1])
		if !okA || !okB {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		return conditionalBoolVal(a == b), "", nil
	case "LEFT", "RIGHT":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		n := int64(1)
		if len(vals) == 2 {
			var errVal *conditionalValue
			n, errVal = intOf(vals[1])
			if errVal != nil {
				return *errVal, "", nil
			}
		}
		if n < 0 {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		runes := []rune(s)
		if int64(len(runes)) <= n {
			return conditionalText(s), "", nil
		}
		if op == "LEFT" {
			return conditionalText(string(runes[:n])), "", nil
		}
		return conditionalText(string(runes[len(runes)-int(n):])), "", nil
	case "MID":
		s, ok := textOf(vals[0])
		if !ok {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		start, errVal := intOf(vals[1])
		if errVal != nil {
			return *errVal, "", nil
		}
		count, errVal := intOf(vals[2])
		if errVal != nil {
			return *errVal, "", nil
		}
		if start < 1 || count < 0 {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		runes := []rune(s)
		if start > int64(len(runes)) {
			return conditionalText(""), "", nil
		}
		a := int(start - 1)
		b := min(len(runes), a+int(min(count, int64(len(runes)))))
		return conditionalText(string(runes[a:b])), "", nil
	case "FIND", "SEARCH":
		needle, okN := textOf(vals[0])
		hay, okH := textOf(vals[1])
		if !okN || !okH {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		start := int64(1)
		if len(vals) == 3 {
			var errVal *conditionalValue
			start, errVal = intOf(vals[2])
			if errVal != nil {
				return *errVal, "", nil
			}
		}
		hayRunes := []rune(hay)
		if start < 1 || start > int64(len(hayRunes)+1) || (op == "SEARCH" && start > int64(len(hayRunes))) {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		if needle == "" {
			return conditionalIntVal(start), "", nil
		}
		if op == "FIND" {
			sub := string(hayRunes[start-1:])
			idx := strings.Index(sub, needle)
			if idx < 0 {
				return conditionalErrorVal("#VALUE!"), "", nil
			}
			return conditionalIntVal(start + int64(utf8.RuneCountInString(sub[:idx]))), "", nil
		}
		pos, complete := e.wildcardPosition(excelformula.Wildcard(needle, false), hayRunes[start-1:], true)
		if !complete {
			return conditionalValue{}, conditionalWorkNotice, nil
		}
		if pos < 0 {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		return conditionalIntVal(start + int64(pos)), "", nil
	case "SUBSTITUTE":
		text, okT := textOf(vals[0])
		oldStr, okO := textOf(vals[1])
		newStr, okN := textOf(vals[2])
		if !okT || !okO || !okN {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		if oldStr == "" {
			return conditionalText(text), "", nil
		}
		var out string
		if len(vals) == 4 {
			inst, errVal := intOf(vals[3])
			if errVal != nil {
				return *errVal, "", nil
			}
			if inst < 1 {
				return conditionalErrorVal("#VALUE!"), "", nil
			}
			out = substituteNth(text, oldStr, newStr, inst)
		} else {
			if count := strings.Count(text, oldStr); len(text)+count*max(0, len(newStr)-len(oldStr)) > formats.ConditionalOperandBytes {
				return conditionalValue{}, "条件操作数超过本次文本容量", nil
			}
			out = strings.ReplaceAll(text, oldStr, newStr)
		}
		if len(out) > formats.ConditionalOperandBytes || len(out) > e.textBytes {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		e.textBytes -= len(out)
		return conditionalText(out), "", nil
	case "REPLACE":
		text, okT := textOf(vals[0])
		rep, okR := textOf(vals[3])
		if !okT || !okR {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		start, errVal := intOf(vals[1])
		if errVal != nil {
			return *errVal, "", nil
		}
		count, errVal := intOf(vals[2])
		if errVal != nil {
			return *errVal, "", nil
		}
		if start < 1 || count < 0 {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		runes := []rune(text)
		a := int(min(int64(len(runes)), start-1))
		b := min(len(runes), a+int(min(count, int64(len(runes)))))
		out := string(runes[:a]) + rep + string(runes[b:])
		if len(out) > formats.ConditionalOperandBytes || len(out) > e.textBytes {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		e.textBytes -= len(out)
		return conditionalText(out), "", nil
	case "REPT":
		text, okT := textOf(vals[0])
		if !okT {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		count, errVal := intOf(vals[1])
		if errVal != nil {
			return *errVal, "", nil
		}
		if count < 0 {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		if count == 0 || text == "" {
			return conditionalText(""), "", nil
		}
		if count > int64(formats.ConditionalOperandBytes) || int64(len(text))*count > int64(formats.ConditionalOperandBytes) || int64(len(text))*count > int64(e.textBytes) {
			return conditionalValue{}, "条件操作数超过本次文本容量", nil
		}
		out := strings.Repeat(text, int(count))
		e.textBytes -= len(out)
		return conditionalText(out), "", nil
	case "VALUE":
		if vals[0].kind == "number" {
			return vals[0], "", nil
		}
		if vals[0].kind == "boolean" {
			return conditionalErrorVal("#VALUE!"), "", nil
		}
		r, errVal := conditionalAsRat(vals[0])
		if errVal != nil {
			return *errVal, "", nil
		}
		return ratToConditionalValue(r), "", nil
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}

// Excel TRIM strips leading/trailing ASCII 0x20 spaces and collapses internal
// runs of ASCII spaces to a single space without stripping tabs or U+3000.
func excelTrim(s string) string {
	s = strings.Trim(s, " ")
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		} else {
			b.WriteByte(s[i])
			prevSpace = false
		}
	}
	return b.String()
}

func substituteNth(s, oldStr, newStr string, n int64) string {
	offset := 0
	for k := int64(1); ; k++ {
		idx := strings.Index(s[offset:], oldStr)
		if idx < 0 {
			return s
		}
		if k == n {
			return s[:offset+idx] + newStr + s[offset+idx+len(oldStr):]
		}
		offset += idx + len(oldStr)
	}
}

// Charge comparisons and retries to the same request budget as formula nodes.
// SEARCH takes the first matching start. Criteria and MATCH require a full match.
func (e *ConditionalEvaluator) wildcardPosition(tokens []excelformula.Token, text []rune, search bool) (int, bool) {
	for start := 0; start <= len(text); start++ {
		ti, pi := start, 0
		star, consumed := -1, start
		for {
			if !e.spend(1) {
				return -1, false
			}
			switch {
			case pi == len(tokens) && (search || ti == len(text)):
				return start, true
			case pi < len(tokens) && tokens[pi].Kind == '*':
				star, consumed = pi, ti
				pi++
				continue
			case pi < len(tokens) && ti < len(text) && (tokens[pi].Kind == '?' || tokens[pi].Rune == excelformula.FoldRune(text[ti])):
				pi++
				ti++
				continue
			case star >= 0 && consumed < len(text):
				consumed++
				ti, pi = consumed, star+1
				continue
			}
			break
		}
		if !search {
			break
		}
	}
	return -1, true
}

func (e *ConditionalEvaluator) evalDateFunc(op string, args []*conditionalExpr, group *conditionalGroup) (conditionalValue, string, error) {
	rats := make([]*big.Rat, len(args))
	nums := make([]int64, len(args))
	for i, arg := range args {
		v, notice, err := e.evalExpr(arg, group)
		if notice != "" || err != nil {
			return conditionalValue{}, notice, err
		}
		r, errVal := conditionalAsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		rats[i] = r
		q := new(big.Int).Quo(r.Num(), r.Denom())
		if !q.IsInt64() {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		nums[i] = q.Int64()
	}
	date1904 := e.index != nil && e.index.date1904
	maxSerial := excelformula.MaxSerial(date1904)
	if op == "DATE" {
		serial, ok := excelformula.Date(nums[0], nums[1], nums[2], date1904)
		if !ok {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return conditionalIntVal(serial), "", nil
	}
	// Reject negative fractional serials like -0.5 instead of truncating to 0.
	if rats[0].Sign() < 0 || nums[0] < 0 || nums[0] > maxSerial {
		return conditionalErrorVal("#NUM!"), "", nil
	}
	y, m, d, wd := excelformula.DateParts(nums[0], date1904)
	switch op {
	case "YEAR":
		return conditionalIntVal(int64(y)), "", nil
	case "MONTH":
		return conditionalIntVal(int64(m)), "", nil
	case "DAY":
		return conditionalIntVal(int64(d)), "", nil
	case "WEEKDAY":
		returnType := int64(1)
		if len(nums) == 2 {
			returnType = nums[1]
		}
		res, ok := excelformula.Weekday(wd, returnType)
		if !ok {
			return conditionalErrorVal("#NUM!"), "", nil
		}
		return conditionalIntVal(int64(res)), "", nil
	}
	return conditionalValue{}, "条件格式公式尚未求值", nil
}
