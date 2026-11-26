package conditional

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"

	"excellens/internal/excelformula"
	"excellens/internal/formats"
)

func (e *Engine) evalCall(node *Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	var indexIssue string
	if e.StatHandler != nil {
		switch node.Op {
		case "COUNTIF", "COUNTIFS", "SUMIF", "SUMIFS", "MATCH", "SUM":
			v, notice, pending, err, handled := e.StatHandler(node, anchorRow, anchorCol, ranges)
			if pending {
				e.Pending = true
			}
			if handled || err != nil {
				return v, notice, err
			}
			indexIssue = notice
		}
	}
	if node.Op == "SUM" && len(node.Args) == 1 && node.Args[0].Kind == Range {
		arg := node.Args[0]
		lo, col, hi, lastCol, ok := e.ResolveRangeBounds(arg, anchorRow, anchorCol)
		moving := !arg.Ref.AbsoluteRow || !arg.EndRef.AbsoluteRow
		span := hi - lo + 1
		if ok && moving && col == lastCol && arg.Ref.ForeignSheet == "" && span > formats.ConditionalStatsDirectRangeRows && span <= formats.ConditionalStatsSlidingWindowRows {
			v, notice, err, handled := e.evalSlidingSum(node, col, lo, hi)
			if handled || err != nil {
				if indexIssue != "" && notice != "" {
					notice = indexIssue + "；" + notice
				}
				return v, notice, err
			}
		}
	}
	v, notice, err := e.evalBuiltin(node, anchorRow, anchorCol, ranges)
	if indexIssue != "" && notice != "" {
		notice = indexIssue + "；" + notice
	}
	return v, notice, err
}

func (e *Engine) evalBuiltin(node *Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	switch node.Op {
	case "TRUE":
		return BoolValue(true), "", nil
	case "FALSE":
		return BoolValue(false), "", nil
	case "NA":
		return ErrorValue("#N/A"), "", nil
	case "IF":
		return e.evalIf(node.Args, anchorRow, anchorCol, ranges)
	case "IFS":
		return e.evalIfs(node.Args, anchorRow, anchorCol, ranges)
	case "IFERROR", "IFNA":
		return e.evalIfError(node.Op, node.Args, anchorRow, anchorCol, ranges)
	case "AND", "OR", "XOR":
		return e.evalLogical(node.Op, node.Args, anchorRow, anchorCol, ranges)
	case "NOT":
		v, notice, err := e.Eval(node.Args[0], anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		b, errVal := AsBool(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		return BoolValue(!b), "", nil
	case "ISBLANK", "ISNUMBER", "ISTEXT", "ISNONTEXT", "ISLOGICAL", "ISERROR", "ISERR", "ISNA", "ISEVEN", "ISODD", "N", "T":
		return e.evalInfo(node.Op, node.Args[0], anchorRow, anchorCol, ranges)
	case "ROW", "COLUMN", "ROWS", "COLUMNS":
		return e.evalPosition(node.Op, node.Args, anchorRow, anchorCol)
	case "ABS", "SIGN", "INT", "TRUNC", "ROUND", "ROUNDUP", "ROUNDDOWN", "MOD", "QUOTIENT", "POWER", "SQRT":
		return e.evalMathScalar(node.Op, node.Args, anchorRow, anchorCol, ranges)
	case "SUM", "AVERAGE", "MIN", "MAX", "COUNT", "COUNTA", "COUNTBLANK", "PRODUCT":
		return e.evalAggregate(node.Op, node.Args, anchorRow, anchorCol, ranges)
	case "LEN", "TRIM", "CLEAN", "LEFT", "RIGHT", "MID", "UPPER", "LOWER", "EXACT", "FIND", "SEARCH", "SUBSTITUTE", "REPLACE", "REPT", "CONCAT", "CONCATENATE", "VALUE":
		return e.evalTextFunc(node.Op, node.Args, anchorRow, anchorCol, ranges)
	case "COUNTIF":
		return e.evalCountIf(node.Args, anchorRow, anchorCol, ranges)
	case "COUNTIFS":
		return e.evalCountIfs(node.Args, anchorRow, anchorCol, ranges)
	case "SUMIF":
		return e.evalSumIf(node.Args, anchorRow, anchorCol, ranges)
	case "SUMIFS":
		return e.evalSumIfs(node.Args, anchorRow, anchorCol, ranges)
	case "MATCH":
		return e.evalMatch(node.Args, anchorRow, anchorCol, ranges)
	case "DATE", "YEAR", "MONTH", "DAY", "WEEKDAY":
		return e.evalDateFunc(node.Op, node.Args, anchorRow, anchorCol, ranges)
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}

func (e *Engine) evalIf(args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	cond, notice, err := e.Eval(args[0], anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	b, errVal := AsBool(cond)
	if errVal != nil {
		return *errVal, "", nil
	}
	if b {
		return e.Eval(args[1], anchorRow, anchorCol, ranges)
	}
	if len(args) == 3 {
		return e.Eval(args[2], anchorRow, anchorCol, ranges)
	}
	return BoolValue(false), "", nil
}

func (e *Engine) evalIfs(args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	for i := 0; i < len(args); i += 2 {
		cond, notice, err := e.Eval(args[i], anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		b, errVal := AsBool(cond)
		if errVal != nil {
			return *errVal, "", nil
		}
		if b {
			return e.Eval(args[i+1], anchorRow, anchorCol, ranges)
		}
	}
	return ErrorValue("#N/A"), "", nil
}

func (e *Engine) evalIfError(op string, args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	v, notice, err := e.Eval(args[0], anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	if v.Kind == "error" && (op == "IFERROR" || strings.EqualFold(v.Text, "#N/A")) {
		return e.Eval(args[1], anchorRow, anchorCol, ranges)
	}
	return v, "", nil
}

func (e *Engine) evalLogical(op string, args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
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
				switch v.Kind {
				case "error":
					return v, "", nil
				case "boolean":
					apply(v.Boolean)
				case "number":
					apply(IsNonZero(v))
				}
			}
			continue
		}
		v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		switch v.Kind {
		case "error":
			return v, "", nil
		case "boolean":
			apply(v.Boolean)
		case "number":
			apply(IsNonZero(v))
		case "blank":
			if arg.Kind != ReferenceCell {
				apply(false)
			}
		case "text":
			if arg.Kind == ReferenceCell {
				// Direct cell reference holding text is ignored in AND/OR/XOR in Excel.
				continue
			}
			switch strings.ToUpper(v.Text) {
			case "TRUE":
				apply(true)
			case "FALSE":
				apply(false)
			default:
				return ErrorValue("#VALUE!"), "", nil
			}
		}
	}
	if !found {
		return ErrorValue("#VALUE!"), "", nil
	}
	if op == "XOR" {
		return BoolValue(xorCount%2 == 1), "", nil
	}
	return BoolValue(result), "", nil
}

func (e *Engine) evalInfo(op string, arg *Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
	if notice != "" || err != nil {
		return EvalValue{}, notice, err
	}
	switch op {
	case "ISBLANK":
		return BoolValue(v.Kind == "blank"), "", nil
	case "ISNUMBER":
		return BoolValue(v.Kind == "number"), "", nil
	case "ISTEXT":
		return BoolValue(v.Kind == "text"), "", nil
	case "ISNONTEXT":
		return BoolValue(v.Kind != "text"), "", nil
	case "ISLOGICAL":
		return BoolValue(v.Kind == "boolean"), "", nil
	case "ISERROR":
		return BoolValue(v.Kind == "error"), "", nil
	case "ISERR":
		return BoolValue(v.Kind == "error" && !strings.EqualFold(v.Text, "#N/A")), "", nil
	case "ISNA":
		return BoolValue(v.Kind == "error" && strings.EqualFold(v.Text, "#N/A")), "", nil
	case "N":
		switch v.Kind {
		case "error":
			return v, "", nil
		case "number":
			return v, "", nil
		case "boolean":
			if v.Boolean {
				return IntValue(1), "", nil
			}
			return IntValue(0), "", nil
		default:
			return IntValue(0), "", nil
		}
	case "T":
		if v.Kind == "error" || v.Kind == "text" {
			return v, "", nil
		}
		return TextValue(""), "", nil
	case "ISEVEN", "ISODD":
		if v.Kind == "error" {
			return v, "", nil
		}
		if v.Kind == "boolean" {
			return ErrorValue("#VALUE!"), "", nil
		}
		r, errVal := AsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		q := new(big.Int).Quo(r.Num(), r.Denom())
		even := q.Bit(0) == 0
		if op == "ISODD" {
			even = !even
		}
		return BoolValue(even), "", nil
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}

func (e *Engine) evalPosition(op string, args []*Expression[EvalValue], anchorRow, anchorCol int) (EvalValue, string, error) {
	switch op {
	case "ROW":
		if len(args) == 0 {
			return IntValue(int64(e.Row)), "", nil
		}
		r1, _, _, _, ok := e.ResolveRangeBounds(args[0], anchorRow, anchorCol)
		if !ok {
			return ErrorValue("#REF!"), "", nil
		}
		return IntValue(int64(r1)), "", nil
	case "COLUMN":
		if len(args) == 0 {
			return IntValue(int64(e.Col)), "", nil
		}
		_, c1, _, _, ok := e.ResolveRangeBounds(args[0], anchorRow, anchorCol)
		if !ok {
			return ErrorValue("#REF!"), "", nil
		}
		return IntValue(int64(c1)), "", nil
	case "ROWS":
		r1, _, r2, _, ok := e.ResolveRangeBounds(args[0], anchorRow, anchorCol)
		if !ok {
			return ErrorValue("#REF!"), "", nil
		}
		return IntValue(int64(r2 - r1 + 1)), "", nil
	case "COLUMNS":
		_, c1, _, c2, ok := e.ResolveRangeBounds(args[0], anchorRow, anchorCol)
		if !ok {
			return ErrorValue("#REF!"), "", nil
		}
		return IntValue(int64(c2 - c1 + 1)), "", nil
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}

func (e *Engine) evalMathScalar(op string, args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	vals := make([]*big.Rat, len(args))
	for i, arg := range args {
		v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		r, errVal := AsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		vals[i] = r
	}
	switch op {
	case "ABS":
		return RatToValue(vals[0].Abs(vals[0])), "", nil
	case "SIGN":
		return IntValue(int64(vals[0].Sign())), "", nil
	case "INT":
		return RatToValue(ratFloor(vals[0])), "", nil
	case "TRUNC":
		digits := int64(0)
		if len(vals) == 2 {
			d, ok := ratTruncInt64(vals[1])
			if !ok {
				return ErrorValue("#NUM!"), "", nil
			}
			digits = d
		}
		return ratRound(vals[0], digits, "down"), "", nil
	case "ROUND":
		d, ok := ratTruncInt64(vals[1])
		if !ok {
			return ErrorValue("#NUM!"), "", nil
		}
		return ratRound(vals[0], d, "half"), "", nil
	case "ROUNDUP":
		d, ok := ratTruncInt64(vals[1])
		if !ok {
			return ErrorValue("#NUM!"), "", nil
		}
		return ratRound(vals[0], d, "up"), "", nil
	case "ROUNDDOWN":
		d, ok := ratTruncInt64(vals[1])
		if !ok {
			return ErrorValue("#NUM!"), "", nil
		}
		return ratRound(vals[0], d, "down"), "", nil
	case "MOD":
		if vals[1].Sign() == 0 {
			return ErrorValue("#DIV/0!"), "", nil
		}
		// Excel MOD(n, d) = n - d * INT(n / d)
		q := new(big.Rat).Quo(vals[0], vals[1])
		fl := ratFloor(q)
		res := new(big.Rat).Sub(vals[0], new(big.Rat).Mul(vals[1], fl))
		return RatToValue(res), "", nil
	case "QUOTIENT":
		if vals[1].Sign() == 0 {
			return ErrorValue("#DIV/0!"), "", nil
		}
		q := new(big.Rat).Quo(vals[0], vals[1])
		i := new(big.Int).Quo(q.Num(), q.Denom())
		return RatToValue(new(big.Rat).SetInt(i)), "", nil
	case "POWER":
		return Power(vals[0], vals[1]), "", nil
	case "SQRT":
		if vals[0].Sign() < 0 {
			return ErrorValue("#NUM!"), "", nil
		}
		f, _ := vals[0].Float64()
		d, ok := ParseDecimal(strconv.FormatFloat(math.Sqrt(f), 'g', 15, 64))
		if !ok {
			return ErrorValue("#NUM!"), "", nil
		}
		return NumberValue(d), "", nil
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
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

func ratRound(r *big.Rat, digits int64, mode string) EvalValue {
	return RatToValue(excelformula.Round(r, digits, mode))
}

func (e *Engine) evalTextFunc(op string, args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	if op == "CONCAT" || op == "CONCATENATE" {
		var b strings.Builder
		for _, arg := range args {
			if op == "CONCAT" && arg.Kind == Range {
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
					if v.Kind == "error" {
						return v, "", nil
					}
					s, ok := e.asText(v)
					if !ok || b.Len()+len(s) > formats.ConditionalOperandBytes || b.Len()+len(s) > e.TextBytes {
						return EvalValue{}, "条件操作数超过本次文本容量", nil
					}
					b.WriteString(s)
				}
				continue
			}
			v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
			if notice != "" || err != nil {
				return EvalValue{}, notice, err
			}
			if v.Kind == "error" {
				return v, "", nil
			}
			s, ok := e.asText(v)
			if !ok || b.Len()+len(s) > formats.ConditionalOperandBytes || b.Len()+len(s) > e.TextBytes {
				return EvalValue{}, "条件操作数超过本次文本容量", nil
			}
			b.WriteString(s)
		}
		e.TextBytes -= b.Len()
		return TextValue(b.String()), "", nil
	}
	vals := make([]EvalValue, len(args))
	for i, arg := range args {
		v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		if v.Kind == "error" {
			return v, "", nil
		}
		vals[i] = v
	}
	textOf := func(v EvalValue) (string, bool) { return e.asText(v) }
	intOf := func(v EvalValue) (int64, *EvalValue) {
		r, errVal := AsRat(v)
		if errVal != nil {
			return 0, errVal
		}
		q := new(big.Int).Quo(r.Num(), r.Denom())
		if !q.IsInt64() {
			ev := ErrorValue("#VALUE!")
			return 0, &ev
		}
		return q.Int64(), nil
	}
	switch op {
	case "LEN":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		return IntValue(int64(utf8.RuneCountInString(s))), "", nil
	case "TRIM":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		return TextValue(excelTrim(s)), "", nil
	case "CLEAN":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		return TextValue(strings.Map(func(r rune) rune {
			if r < 32 {
				return -1
			}
			return r
		}, s)), "", nil
	case "UPPER":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		return TextValue(strings.ToUpper(s)), "", nil
	case "LOWER":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		return TextValue(strings.ToLower(s)), "", nil
	case "EXACT":
		a, okA := textOf(vals[0])
		b, okB := textOf(vals[1])
		if !okA || !okB {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		return BoolValue(a == b), "", nil
	case "LEFT", "RIGHT":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		n := int64(1)
		if len(vals) == 2 {
			var errVal *EvalValue
			n, errVal = intOf(vals[1])
			if errVal != nil {
				return *errVal, "", nil
			}
		}
		if n < 0 {
			return ErrorValue("#VALUE!"), "", nil
		}
		runes := []rune(s)
		if int64(len(runes)) <= n {
			return TextValue(s), "", nil
		}
		if op == "LEFT" {
			return TextValue(string(runes[:n])), "", nil
		}
		return TextValue(string(runes[len(runes)-int(n):])), "", nil
	case "MID":
		s, ok := textOf(vals[0])
		if !ok {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
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
			return ErrorValue("#VALUE!"), "", nil
		}
		runes := []rune(s)
		if start > int64(len(runes)) {
			return TextValue(""), "", nil
		}
		a := int(start - 1)
		b := min(len(runes), a+int(min(count, int64(len(runes)))))
		return TextValue(string(runes[a:b])), "", nil
	case "FIND", "SEARCH":
		needle, okN := textOf(vals[0])
		hay, okH := textOf(vals[1])
		if !okN || !okH {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		start := int64(1)
		if len(vals) == 3 {
			var errVal *EvalValue
			start, errVal = intOf(vals[2])
			if errVal != nil {
				return *errVal, "", nil
			}
		}
		hayRunes := []rune(hay)
		if start < 1 || start > int64(len(hayRunes)+1) || (op == "SEARCH" && start > int64(len(hayRunes))) {
			return ErrorValue("#VALUE!"), "", nil
		}
		if needle == "" {
			return IntValue(start), "", nil
		}
		if op == "FIND" {
			sub := string(hayRunes[start-1:])
			idx := strings.Index(sub, needle)
			if idx < 0 {
				return ErrorValue("#VALUE!"), "", nil
			}
			return IntValue(start + int64(utf8.RuneCountInString(sub[:idx]))), "", nil
		}
		pos, complete := e.WildcardPosition(excelformula.Wildcard(needle, false), hayRunes[start-1:], true)
		if !complete {
			return EvalValue{}, WorkNotice, nil
		}
		if pos < 0 {
			return ErrorValue("#VALUE!"), "", nil
		}
		return IntValue(start + int64(pos)), "", nil
	case "SUBSTITUTE":
		text, okT := textOf(vals[0])
		oldStr, okO := textOf(vals[1])
		newStr, okN := textOf(vals[2])
		if !okT || !okO || !okN {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		if oldStr == "" {
			return TextValue(text), "", nil
		}
		var out string
		if len(vals) == 4 {
			inst, errVal := intOf(vals[3])
			if errVal != nil {
				return *errVal, "", nil
			}
			if inst < 1 {
				return ErrorValue("#VALUE!"), "", nil
			}
			out = substituteNth(text, oldStr, newStr, inst)
		} else {
			if count := strings.Count(text, oldStr); len(text)+count*max(0, len(newStr)-len(oldStr)) > formats.ConditionalOperandBytes {
				return EvalValue{}, "条件操作数超过本次文本容量", nil
			}
			out = strings.ReplaceAll(text, oldStr, newStr)
		}
		if len(out) > formats.ConditionalOperandBytes || len(out) > e.TextBytes {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		e.TextBytes -= len(out)
		return TextValue(out), "", nil
	case "REPLACE":
		text, okT := textOf(vals[0])
		rep, okR := textOf(vals[3])
		if !okT || !okR {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
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
			return ErrorValue("#VALUE!"), "", nil
		}
		runes := []rune(text)
		a := int(min(int64(len(runes)), start-1))
		b := min(len(runes), a+int(min(count, int64(len(runes)))))
		out := string(runes[:a]) + rep + string(runes[b:])
		if len(out) > formats.ConditionalOperandBytes || len(out) > e.TextBytes {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		e.TextBytes -= len(out)
		return TextValue(out), "", nil
	case "REPT":
		text, okT := textOf(vals[0])
		if !okT {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		count, errVal := intOf(vals[1])
		if errVal != nil {
			return *errVal, "", nil
		}
		if count < 0 {
			return ErrorValue("#VALUE!"), "", nil
		}
		if count == 0 || text == "" {
			return TextValue(""), "", nil
		}
		if count > int64(formats.ConditionalOperandBytes) || int64(len(text))*count > int64(formats.ConditionalOperandBytes) || int64(len(text))*count > int64(e.TextBytes) {
			return EvalValue{}, "条件操作数超过本次文本容量", nil
		}
		out := strings.Repeat(text, int(count))
		e.TextBytes -= len(out)
		return TextValue(out), "", nil
	case "VALUE":
		if vals[0].Kind == "number" {
			return vals[0], "", nil
		}
		if vals[0].Kind == "boolean" {
			return ErrorValue("#VALUE!"), "", nil
		}
		r, errVal := AsRat(vals[0])
		if errVal != nil {
			return *errVal, "", nil
		}
		return RatToValue(r), "", nil
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
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

// WildcardPosition charges comparisons and retries to the same request budget as
// formula nodes. SEARCH takes the first matching start; criteria and MATCH require a full match.
func (e *Engine) WildcardPosition(tokens []excelformula.Token, text []rune, search bool) (int, bool) {
	for start := 0; start <= len(text); start++ {
		ti, pi := start, 0
		star, consumed := -1, start
		for {
			if !e.Spend(1) {
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

func (e *Engine) evalDateFunc(op string, args []*Expression[EvalValue], anchorRow, anchorCol int, ranges []Area) (EvalValue, string, error) {
	rats := make([]*big.Rat, len(args))
	nums := make([]int64, len(args))
	for i, arg := range args {
		v, notice, err := e.Eval(arg, anchorRow, anchorCol, ranges)
		if notice != "" || err != nil {
			return EvalValue{}, notice, err
		}
		r, errVal := AsRat(v)
		if errVal != nil {
			return *errVal, "", nil
		}
		rats[i] = r
		q := new(big.Int).Quo(r.Num(), r.Denom())
		if !q.IsInt64() {
			return ErrorValue("#NUM!"), "", nil
		}
		nums[i] = q.Int64()
	}
	maxSerial := excelformula.MaxSerial(e.Date1904)
	if op == "DATE" {
		serial, ok := excelformula.Date(nums[0], nums[1], nums[2], e.Date1904)
		if !ok {
			return ErrorValue("#NUM!"), "", nil
		}
		return IntValue(serial), "", nil
	}
	// Reject negative fractional serials like -0.5 instead of truncating to 0.
	if rats[0].Sign() < 0 || nums[0] < 0 || nums[0] > maxSerial {
		return ErrorValue("#NUM!"), "", nil
	}
	y, m, d, wd := excelformula.DateParts(nums[0], e.Date1904)
	switch op {
	case "YEAR":
		return IntValue(int64(y)), "", nil
	case "MONTH":
		return IntValue(int64(m)), "", nil
	case "DAY":
		return IntValue(int64(d)), "", nil
	case "WEEKDAY":
		mode := int64(1)
		if len(nums) == 2 {
			mode = nums[1]
		}
		day, ok := excelformula.Weekday(wd, mode)
		if !ok {
			return ErrorValue("#NUM!"), "", nil
		}
		return IntValue(int64(day)), "", nil
	}
	return EvalValue{}, "条件格式公式尚未求值", nil
}
