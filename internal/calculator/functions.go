package calculator

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type FunctionSpec struct {
	Name      string "json:\"name\""
	Signature string "json:\"signature\""
}
type functionSpec struct {
	FunctionSpec
	min, max int
	run      func(*formulaEval, *formulaNode) (FormulaValue, error)
}

var formulaFunctions = map[string]functionSpec{}

func init() {
	out := formulaFunctions
	add := func(names, signature string, min, max int) {
		for _, name := range strings.Split(names, ",") {
			out[name] = functionSpec{FunctionSpec: FunctionSpec{name, name + "(" + signature + ")"}, min: min, max: max}
		}
	}
	add("SUM,MIN,MAX,COUNT,COUNTA", "数值1, …", 0, 128)
	add("AVERAGE,AVG", "数值1, …", 1, 128)
	add("ABS", "数值", 1, 1)
	add("ROUND,ROUNDUP,ROUNDDOWN", "数值, 位数", 2, 2)
	add("IF", "条件, 成立时, 不成立时", 2, 3)
	add("IFERROR", "公式, 出错时", 2, 2)
	add("AND,OR", "条件1, …", 1, 128)
	add("NOT", "条件", 1, 1)
	add("TRUE,FALSE", "", 0, 0)
	add("CONCAT,CONCATENATE", "文本1, …", 1, 128)
	add("TEXTJOIN", "分隔符, 忽略空值, 文本1, …", 3, 128)
	add("LEN,TRIM,UPPER,LOWER,VALUE,ISNUMBER,ISTEXT,ISBLANK,ISERROR", "值", 1, 1)
	add("LEFT,RIGHT", "文本, 字符数", 1, 2)
	add("MID", "文本, 起点, 字符数", 3, 3)
	add("SUBSTITUTE", "文本, 原文本, 新文本, [第几次]", 3, 4)
	add("EXACT", "文本1, 文本2", 2, 2)
	add("FIND,SEARCH", "查找文本, 文本, [起点]", 2, 3)
	add("TEXT", "值, 格式", 2, 2)
	registerExtendedFunctions(out)
}

func Functions() []FunctionSpec {
	out := make([]FunctionSpec, 0, len(formulaFunctions))
	for _, spec := range formulaFunctions {
		out = append(out, spec.FunctionSpec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (e *formulaEval) function(n *formulaNode) (FormulaValue, error) {
	if n.op == "SUBTOTAL" && e.worksheet {
		return e.worksheetSubtotal(n)
	}
	spec := formulaFunctions[n.op]
	if len(n.args) < spec.min || len(n.args) > spec.max {
		return FormulaValue{}, fmt.Errorf("#VALUE! %s 参数数量不正确", n.op)
	}
	if n.op == "IFERROR" || n.op == "IFNA" {
		v, err := e.eval(n.args[0])
		if e.ctx.Err() != nil {
			return FormulaValue{}, e.ctx.Err()
		}
		if err != nil {
			if n.op == "IFERROR" || strings.HasPrefix(err.Error(), "#N/A") {
				return e.eval(n.args[1])
			}
			return FormulaValue{}, err
		}
		if v.Kind == "range" {
			return e.arrayFallback(n, v)
		}
		return v, nil
	}
	if scalarArrayFunctions[n.op] {
		return e.liftScalarFunction(n)
	}
	return e.scalarFunction(n)
}

func (e *formulaEval) scalarFunction(n *formulaNode) (FormulaValue, error) {
	spec := formulaFunctions[n.op]
	if spec.run != nil {
		return spec.run(e, n)
	}
	if n.op == "IF" {
		test, err := e.eval(n.args[0])
		if err != nil {
			return test, err
		}
		if test.Kind == "range" {
			return e.arrayIf(n, test)
		}
		yes, err := formulaBool(test)
		if err != nil {
			return FormulaValue{}, err
		}
		if yes {
			return e.eval(n.args[1])
		}
		if len(n.args) == 3 {
			return e.eval(n.args[2])
		}
		return BoolValue(false), nil
	}
	switch n.op {
	case "SUM", "AVG", "AVERAGE", "MIN", "MAX", "COUNT", "COUNTA":
		return e.aggregate(n)
	}
	values := make([]FormulaValue, len(n.args))
	for i, arg := range n.args {
		v, err := e.eval(arg)
		if err != nil {
			return v, err
		}
		values[i] = v
	}
	switch n.op {
	case "TRUE", "FALSE":
		return BoolValue(n.op == "TRUE"), nil
	case "ISNUMBER":
		return BoolValue(values[0].Kind == "number"), nil
	case "ISTEXT":
		return BoolValue(values[0].Kind == "text"), nil
	case "ISBLANK":
		return BoolValue(values[0].Kind == "blank"), nil
	case "NOT":
		b, err := formulaBool(values[0])
		return BoolValue(!b), err
	case "AND", "OR":
		flat, err := e.flattenFormulaValues(values)
		if err != nil {
			return FormulaValue{}, err
		}
		seen := false
		answer := n.op == "AND"
		for _, v := range flat {
			if v.Kind == "blank" {
				continue
			}
			b, err := formulaBool(v)
			if err != nil {
				return FormulaValue{}, err
			}
			seen = true
			if n.op == "AND" {
				answer = answer && b
			} else {
				answer = answer || b
			}
		}
		if !seen {
			return FormulaValue{}, errors.New("#VALUE! 没有逻辑值")
		}
		return BoolValue(answer), nil
	case "ABS", "ROUND", "ROUNDUP", "ROUNDDOWN":
		req := Request{Operands: map[string]string{}, Precision: -1}
		args := []string{}
		for i, v := range values {
			number, err := formulaNumber(v)
			if err != nil {
				return FormulaValue{}, err
			}
			id := strconv.Itoa(i + 1)
			req.Operands[id] = number.RatString()
			args = append(args, "["+id+"]")
		}
		req.Expression = n.op + "(" + strings.Join(args, ",") + ")"
		r, err := Evaluate(e.ctx, req)
		if err != nil {
			return FormulaValue{}, err
		}
		number, err := Canonical(r.Value)
		return NumberValue(number), err
	case "CONCAT", "CONCATENATE", "TEXTJOIN":
		delimiter := ""
		ignore := false
		parts := values
		if n.op == "TEXTJOIN" {
			var err error
			delimiter, err = formulaText(values[0])
			if err != nil {
				return FormulaValue{}, err
			}
			ignore, err = formulaBool(values[1])
			if err != nil {
				return FormulaValue{}, err
			}
			parts = values[2:]
		}
		flat, err := e.flattenFormulaValues(parts)
		if err != nil {
			return FormulaValue{}, err
		}
		var b strings.Builder
		written := false
		for _, v := range flat {
			s, err := formulaText(v)
			if err != nil {
				return FormulaValue{}, err
			}
			if ignore && s == "" {
				continue
			}
			extra := len(s)
			if written {
				extra += len(delimiter)
			}
			if b.Len()+extra > maxFormulaText {
				return FormulaValue{}, errors.New("#NUM! 文本结果过长")
			}
			if written {
				b.WriteString(delimiter)
			}
			b.WriteString(s)
			written = true
		}
		return TextValue(b.String()), nil
	case "VALUE":
		number, err := formulaNumber(values[0])
		return NumberValue(number), err
	case "TEXT":
		mask, err := formulaText(values[1])
		if err != nil {
			return FormulaValue{}, err
		}
		if mask == "@" {
			s, err := formulaText(values[0])
			return TextValue(s), err
		}
		if strings.ContainsAny(strings.ToLower(mask), "ymdhs") {
			text, err := formatDateMask(e, values[0], mask)
			return TextValue(text), err
		}
		number, err := formulaNumber(values[0])
		if err != nil {
			return FormulaValue{}, err
		}
		s, err := formatFormulaText(number, mask)
		return TextValue(s), err
	case "EXACT":
		a, err := formulaText(values[0])
		if err != nil {
			return FormulaValue{}, err
		}
		b, err := formulaText(values[1])
		return BoolValue(a == b), err
	}
	text, err := formulaText(values[0])
	if err != nil {
		return FormulaValue{}, err
	}
	switch n.op {
	case "LEN":
		return NumberValue(big.NewRat(int64(utf8.RuneCountInString(text)), 1)), nil
	case "UPPER":
		return TextValue(strings.ToUpper(text)), nil
	case "LOWER":
		return TextValue(strings.ToLower(text)), nil
	case "TRIM":
		parts := strings.Split(strings.Trim(text, " "), " ")
		kept := parts[:0]
		for _, p := range parts {
			if p != "" {
				kept = append(kept, p)
			}
		}
		return TextValue(strings.Join(kept, " ")), nil
	case "LEFT", "RIGHT", "MID":
		start, count := int64(0), int64(1)
		if n.op == "MID" {
			start, err = formulaInteger(values[1])
			if err != nil || start < 1 {
				return FormulaValue{}, errors.New("#VALUE! 起点必须大于0")
			}
			start--
			count, err = formulaInteger(values[2])
		} else if len(values) > 1 {
			count, err = formulaInteger(values[1])
		}
		if err != nil || count < 0 {
			return FormulaValue{}, errors.New("#VALUE! 字符数无效")
		}
		if n.op == "RIGHT" {
			start = max(0, int64(utf8.RuneCountInString(text))-count)
		}
		return TextValue(sliceFormulaText(text, start, count)), nil
	case "SUBSTITUTE":
		old, err := formulaText(values[1])
		if err != nil {
			return FormulaValue{}, err
		}
		replacement, err := formulaText(values[2])
		if err != nil {
			return FormulaValue{}, err
		}
		if old == "" {
			return TextValue(text), nil
		}
		count := int64(-1)
		if len(values) == 4 {
			count, err = formulaInteger(values[3])
			if err != nil || count < 1 {
				return FormulaValue{}, errors.New("#VALUE! 次数无效")
			}
		}
		if count < 0 {
			hits := strings.Count(text, old)
			if len(text)+hits*(len(replacement)-len(old)) > maxFormulaText {
				return FormulaValue{}, errors.New("#NUM! 文本结果过长")
			}
			return TextValue(strings.ReplaceAll(text, old, replacement)), nil
		}
		at := 0
		for i := int64(1); i <= count; i++ {
			offset := strings.Index(text[at:], old)
			if offset < 0 {
				return TextValue(text), nil
			}
			at += offset
			if i == count {
				return TextValue(text[:at] + replacement + text[at+len(old):]), nil
			}
			at += len(old)
		}
	case "FIND", "SEARCH":
		hay, err := formulaText(values[1])
		if err != nil {
			return FormulaValue{}, err
		}
		needle := text
		start := int64(1)
		if len(values) == 3 {
			start, err = formulaInteger(values[2])
			if err != nil {
				return FormulaValue{}, err
			}
		}
		if n.op == "SEARCH" {
			hay = strings.ToUpper(hay)
			needle = strings.ToUpper(needle)
		}
		if start < 1 || start > int64(utf8.RuneCountInString(hay))+1 {
			return FormulaValue{}, errors.New("#VALUE! 起点无效")
		}
		tail := sliceFormulaText(hay, start-1, int64(utf8.RuneCountInString(hay)))
		at := strings.Index(tail, needle)
		if n.op == "SEARCH" {
			var pattern strings.Builder
			escaped := false
			for _, c := range needle {
				if escaped {
					pattern.WriteString(regexp.QuoteMeta(string(c)))
					escaped = false
					continue
				}
				switch c {
				case '~':
					escaped = true
				case '*':
					pattern.WriteString(".*?")
				case '?':
					pattern.WriteByte('.')
				default:
					pattern.WriteString(regexp.QuoteMeta(string(c)))
				}
			}
			if escaped {
				pattern.WriteString("~")
			}
			re, err := regexp.Compile("(?s)" + pattern.String())
			if err != nil {
				return FormulaValue{}, errors.New("#VALUE! 查找条件无效")
			}
			match := re.FindStringIndex(tail)
			at = -1
			if match != nil {
				at = match[0]
			}
		}
		if at < 0 {
			return FormulaValue{}, errors.New("#VALUE! 未找到文本")
		}
		return NumberValue(big.NewRat(start+int64(utf8.RuneCountInString(tail[:at])), 1)), nil
	}
	return FormulaValue{}, errors.New("#NAME? 不支持的函数")
}
func (e *formulaEval) aggregate(n *formulaNode) (FormulaValue, error) {
	sum := new(big.Rat)
	var nativeSum float64
	addSum := func(value *big.Rat) {
		if e.worksheet {
			n, _ := value.Float64()
			nativeSum += n
		} else {
			sum.Add(sum, value)
		}
	}
	var low, high *big.Rat
	count, nonempty := int64(0), int64(0)
	add := func(v *big.Rat) {
		addSum(v)
		if low == nil || v.Cmp(low) < 0 {
			low = new(big.Rat).Set(v)
		}
		if high == nil || v.Cmp(high) > 0 {
			high = new(big.Rat).Set(v)
		}
	}
	for _, arg := range n.args {
		if arg.kind == "ref" {
			rv, err := e.get(arg.ref)
			if err != nil {
				return FormulaValue{}, err
			}
			if rv.Aggregate != nil {
				rv, err = rv.Aggregate()
				if err != nil {
					return FormulaValue{}, err
				}
			}
			if n.op != "COUNT" && n.op != "COUNTA" && rv.Fault != nil {
				return FormulaValue{}, rv.Fault
			}
			count += rv.Count
			nonempty += rv.NonEmpty
			if rv.NonEmpty == 0 && rv.Count > 0 {
				nonempty += rv.Count
			}
			if rv.Count > 0 && rv.Sum != nil {
				addSum(rv.Sum)
				if rv.Min != nil && (low == nil || rv.Min.Cmp(low) < 0) {
					low = new(big.Rat).Set(rv.Min)
				}
				if rv.Max != nil && (high == nil || rv.Max.Cmp(high) > 0) {
					high = new(big.Rat).Set(rv.Max)
				}
			}
			continue
		}
		v, err := e.eval(arg)
		if err != nil {
			return v, err
		}
		if v.Kind == "range" {
			err = visitSparse(e, v, func(_ int, item FormulaValue) error {
				if item.Kind != "blank" {
					nonempty++
				}
				if n.op != "COUNT" && n.op != "COUNTA" {
					if err := valueFault(item); err != nil {
						return err
					}
				}
				if number, ok := dataNumber(item); ok {
					count++
					add(number)
				}
				return nil
			})
			if err != nil {
				return FormulaValue{}, err
			}
			continue
		}
		if v.Kind == "blank" {
			continue
		}
		nonempty++
		number, err := formulaNumber(v)
		if err != nil {
			if n.op == "COUNTA" || n.op == "COUNT" {
				continue
			}
			return FormulaValue{}, err
		}
		count++
		add(number)
	}
	if err := bounded(sum); err != nil {
		return FormulaValue{}, err
	}
	switch n.op {
	case "COUNT":
		return NumberValue(big.NewRat(count, 1)), nil
	case "COUNTA":
		return NumberValue(big.NewRat(nonempty, 1)), nil
	case "MIN":
		if low == nil {
			low = new(big.Rat)
		}
		return NumberValue(low), nil
	case "MAX":
		if high == nil {
			high = new(big.Rat)
		}
		return NumberValue(high), nil
	case "AVERAGE", "AVG":
		if count == 0 {
			return FormulaValue{}, errors.New("#DIV/0! 没有数值")
		}
		sum.Quo(sum, big.NewRat(count, 1))
		nativeSum /= float64(count)
	}
	if e.worksheet {
		return worksheetNumber(nativeSum)
	}
	return NumberValue(sum), nil
}
func (e *formulaEval) flattenFormulaValues(values []FormulaValue) ([]FormulaValue, error) {
	flat := []FormulaValue{}
	for _, v := range values {
		if v.Kind != "range" {
			flat = append(flat, v)
			continue
		}
		items, _, _, err := materialize(e, v, maxArrayCells-len(flat))
		if err != nil {
			return nil, err
		}
		flat = append(flat, items...)
		if len(flat) > maxArrayCells {
			return nil, errors.New("#NUM! 文本范围过大")
		}
	}
	return flat, nil
}
func formulaInteger(v FormulaValue) (int64, error) {
	n, err := formulaNumber(v)
	if err != nil {
		return 0, err
	}
	i := new(big.Int).Quo(n.Num(), n.Denom())
	if !i.IsInt64() {
		return 0, errors.New("#VALUE! 整数过大")
	}
	return i.Int64(), nil
}
func sliceFormulaText(s string, start, count int64) string {
	if count <= 0 {
		return ""
	}
	first, end, index := len(s), len(s), int64(0)
	for at := range s {
		if index == start {
			first = at
		}
		if index-start == count {
			end = at
			break
		}
		index++
	}
	if first > end {
		return ""
	}
	return s[first:end]
}

var formulaTextMask = regexp.MustCompile("^[#0,]+(?:\\.[0#]{1,20})?%?$")

func formatFormulaText(n *big.Rat, mask string) (string, error) {
	if !formulaTextMask.MatchString(mask) {
		return "", errors.New("#VALUE! 不支持的数字格式")
	}
	percent := strings.HasSuffix(mask, "%")
	mask = strings.TrimSuffix(mask, "%")
	parts := strings.SplitN(mask, ".", 2)
	places := 0
	if len(parts) > 1 {
		places = len(parts[1])
	}
	s := strings.TrimSuffix(Format(n, places, percent).Copy, "%")
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign = "-"
		s = s[1:]
	}
	number := strings.SplitN(s, ".", 2)
	zeros := strings.Count(parts[0], "0")
	if len(number[0]) < zeros {
		number[0] = strings.Repeat("0", zeros-len(number[0])) + number[0]
	}
	if strings.Contains(parts[0], ",") {
		digits := number[0]
		var chunks []string
		for len(digits) > 3 {
			chunks = append([]string{digits[len(digits)-3:]}, chunks...)
			digits = digits[:len(digits)-3]
		}
		chunks = append([]string{digits}, chunks...)
		number[0] = strings.Join(chunks, ",")
	}
	if len(parts) > 1 && strings.HasSuffix(parts[1], "#") {
		optional := len(parts[1]) - len(strings.TrimRight(parts[1], "#"))
		for optional > 0 && strings.HasSuffix(number[1], "0") {
			number[1] = strings.TrimSuffix(number[1], "0")
			optional--
		}
		if number[1] == "" {
			number = number[:1]
		}
	}
	out := sign + strings.Join(number, ".")
	if percent {
		out += "%"
	}
	return out, nil
}
