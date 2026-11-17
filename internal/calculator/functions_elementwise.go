package calculator

import "strings"

// These functions consume scalar arguments. They share one broadcasting path
// for source ranges, array literals and values returned by array functions.
// Aggregations and functions with range-valued parameters stay in their own
// implementations; in particular, holiday and lookup ranges are not lifted.
var scalarArrayFunctions = func() map[string]bool {
	out := map[string]bool{}
	for _, names := range []string{
		"ABS,ROUND,ROUNDUP,ROUNDDOWN,SIGN,INT,SQRT,SQRTPI,EXP,LN,LOG,LOG10,SIN,COS,TAN,ASIN,ACOS,ATAN,ATAN2,SINH,COSH,TANH,ASINH,ACOSH,ATANH,DEGREES,RADIANS,FACT,FACTDOUBLE,EVEN,ODD,TRUNC,MOD,QUOTIENT,POWER,COMBIN,COMBINA,PERMUT,PERMUTATIONA,MROUND,CEILING,FLOOR,CEILING.MATH,FLOOR.MATH,CEILING.PRECISE,FLOOR.PRECISE",
		"LEN,TRIM,UPPER,LOWER,VALUE,LEFT,RIGHT,MID,SUBSTITUTE,EXACT,FIND,SEARCH,TEXT,PROPER,CLEAN,CODE,UNICODE,CHAR,UNICHAR,REPLACE,REPT,NUMBERVALUE,FIXED",
		"ISNUMBER,ISTEXT,ISBLANK,ISERROR,ISNA,ISERR,ISLOGICAL,ISNONTEXT,ISEVEN,ISODD,N,T,ERROR.TYPE,NOT",
		"DATE,TIME,YEAR,MONTH,DAY,HOUR,MINUTE,SECOND,DATEVALUE,TIMEVALUE,ISOWEEKNUM,WEEKDAY,WEEKNUM,EDATE,EOMONTH,DAYS,DATEDIF,DAYS360,YEARFRAC",
		"NORM.DIST,NORM.INV,NORM.S.DIST,NORM.S.INV,PV,FV,PMT,NPER,RATE,IPMT,PPMT,SLN,SYD,DDB,EFFECT,NOMINAL,RRI,PDURATION,DOLLARDE,DOLLARFR",
	} {
		for _, name := range strings.Split(names, ",") {
			out[name] = true
		}
	}
	return out
}()

func (e *formulaEval) liftScalarFunction(n *formulaNode) (FormulaValue, error) {
	args := make([]*formulaNode, len(n.args))
	arrays := make([]formulaArray, len(n.args))
	hasArray := false
	for i, arg := range n.args {
		v, err := e.eval(arg)
		if err != nil {
			if e.ctx.Err() != nil {
				return FormulaValue{}, e.ctx.Err()
			}
			v = errorValue(err)
		}
		args[i] = &formulaNode{kind: "value", value: v}
		hasArray = hasArray || v.Kind == "range"
	}
	call := func() (FormulaValue, error) {
		// Only original AST nodes consume the expression complexity budget.
		// Synthetic literals are evaluated again for each array position.
		steps := e.steps
		defer func() { e.steps = steps }()
		return e.scalarFunction(&formulaNode{kind: "function", op: n.op, args: args})
	}
	if !hasArray {
		return call()
	}
	for i, arg := range args {
		a, err := e.arrayData(arg.value)
		if err != nil {
			return FormulaValue{}, err
		}
		arrays[i] = a
	}
	rows, cols, err := broadcastSize(arrays...)
	if err != nil {
		return FormulaValue{}, err
	}
	out := make([]FormulaValue, rows*cols)
	textBytes := 0
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if err := e.ctx.Err(); err != nil {
				return FormulaValue{}, err
			}
			for i, a := range arrays {
				args[i].value = a.get(r, c)
			}
			v, err := call()
			if err != nil {
				v = errorValue(err)
			}
			if err := trackArrayText(&textBytes, v); err != nil {
				return FormulaValue{}, err
			}
			out[r*cols+c] = v
		}
	}
	return arrayValue(out, rows, cols), nil
}

func (e *formulaEval) arrayFallback(n *formulaNode, value FormulaValue) (FormulaValue, error) {
	a, err := e.arrayData(value)
	if err != nil {
		return FormulaValue{}, err
	}
	selected := func(v FormulaValue) bool {
		return v.Kind == "error" && (n.op == "IFERROR" || strings.HasPrefix(v.Text, "#N/A"))
	}
	needed := false
	for _, v := range a.values {
		needed = needed || selected(v)
	}
	if !needed {
		return value, nil
	}
	fallback, err := e.eval(n.args[1])
	if err != nil {
		if e.ctx.Err() != nil {
			return FormulaValue{}, e.ctx.Err()
		}
		fallback = errorValue(err)
	}
	b, err := e.arrayData(fallback)
	if err != nil {
		return FormulaValue{}, err
	}
	rows, cols, err := broadcastSize(a, b)
	if err != nil {
		return FormulaValue{}, err
	}
	out := make([]FormulaValue, rows*cols)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			if err := e.ctx.Err(); err != nil {
				return FormulaValue{}, err
			}
			v := a.get(r, c)
			if selected(v) {
				v = b.get(r, c)
			}
			out[r*cols+c] = v
		}
	}
	return arrayValue(out, rows, cols), nil
}
