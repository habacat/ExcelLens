package calculator

import (
	"errors"
	"math/big"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

func registerLookupFunctions(add functionRegistrar) {
	add("INDEX", "区域, 行号, [列号]", 2, 3, lookupFunction)
	add("MATCH", "查找值, 区域, [匹配方式]", 2, 3, lookupFunction)
	add("XMATCH", "查找值, 区域, [匹配方式], [查找方向]", 2, 4, lookupFunction)
	add("VLOOKUP,HLOOKUP", "查找值, 数据区域, 返回序号, [近似匹配]", 3, 4, lookupFunction)
	add("XLOOKUP", "查找值, 查找区域, 返回区域, [未找到], [匹配方式], [查找方向]", 3, 6, lookupFunction)
	add("LOOKUP", "查找值, 查找区域, [返回区域]", 2, 3, lookupFunction)
	add("ROWS,COLUMNS,ROW,COLUMN", "区域", 1, 1, lookupFunction)
	add("ADDRESS", "行号, 列号, [绝对类型], [A1样式], [工作表]", 2, 5, lookupFunction)
	add("INDIRECT", "地址文本, [A1样式]", 1, 2, lookupFunction)
	add("OFFSET", "区域, 偏移行, 偏移列, [高度], [宽度]", 3, 5, lookupFunction)
}

var errLookupFound = errors.New("first ordered lookup match found")

func lookupIndex(e *formulaEval, needle, vector FormulaValue, mode, direction int, wildcard bool) (int, error) {
	rows, cols, err := rangeShape(vector)
	if err != nil {
		return -1, err
	}
	if rows != 1 && cols != 1 {
		return -1, errors.New("#VALUE! 查找区域必须是单行或单列")
	}
	var test criterion
	if wildcard && needle.Kind == "text" {
		re, err := wildcardPattern(needle.Text, true)
		if err != nil {
			return -1, err
		}
		test = func(v FormulaValue) bool { return v.Kind == "text" && re.MatchString(v.Text) }
	}
	best := -1
	var bestValue FormulaValue
	size := rows * cols
	ordered := vector.Kind != "range" || vector.Range.Ordered || vector.Range.Visit == nil && vector.Range.Load != nil
	// Missing source cells are blank. Iterate positions only for blank/zero
	// lookups; ordinary ledger lookups stream the actual stored cells.
	inspect := func(i int, x FormulaValue) error {
		if x.Kind == "error" {
			return nil
		}
		cmp, err := compareFormulaValues(x, needle)
		if err != nil {
			return nil
		}
		equal := cmp == 0
		if test != nil {
			equal = test(x)
		}
		if equal {
			if best < 0 || compareEqual(bestValue, needle, test) == false || direction > 0 && i < best || direction < 0 && i > best {
				best = i
				bestValue = x
			}
			if direction > 0 && ordered {
				return errLookupFound
			}
			return nil
		}
		if best >= 0 && compareEqual(bestValue, needle, test) {
			return nil
		}
		if mode == -1 && cmp < 0 || mode == 1 && cmp > 0 {
			if best < 0 {
				best = i
				bestValue = x
			} else {
				c, _ := compareFormulaValues(x, bestValue)
				if mode == -1 && c > 0 || mode == 1 && c < 0 || c == 0 && (direction > 0 && i < best || direction < 0 && i > best) {
					best = i
					bestValue = x
				}
			}
		}
		return nil
	}
	if needle.Kind == "blank" || needle.Kind == "number" && needle.Number.Sign() == 0 || needle.Kind == "text" && needle.Text == "" {
		if err = visitDense(e, vector, inspect); err != nil && !errors.Is(err, errLookupFound) {
			return -1, err
		}
	} else {
		if err = visitSparse(e, vector, inspect); err != nil && !errors.Is(err, errLookupFound) {
			return -1, err
		}
	}
	if best < 0 || best >= size {
		return -1, errors.New("#N/A 未找到匹配值")
	}
	return best, nil
}
func compareEqual(a, b FormulaValue, test criterion) bool {
	if test != nil {
		return test(a)
	}
	n, err := compareFormulaValues(a, b)
	return err == nil && n == 0
}
func lookupFunction(e *formulaEval, node *formulaNode) (FormulaValue, error) {
	integerArg := func(i int, defaultValue int64) (int64, error) {
		if i >= len(node.args) {
			return defaultValue, nil
		}
		v, err := e.eval(node.args[i])
		if err != nil {
			return 0, err
		}
		if v.Kind == "blank" {
			return defaultValue, nil
		}
		return formulaInteger(v)
	}
	switch node.op {
	case "ROWS", "COLUMNS", "ROW", "COLUMN":
		v, err := rangeArg(e, node.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		rows, cols, err := rangeShape(v)
		if err != nil {
			return FormulaValue{}, err
		}
		number := rows
		switch node.op {
		case "COLUMNS":
			number = cols
		case "ROW", "COLUMN":
			if v.Kind != "range" {
				return FormulaValue{}, errors.New("#VALUE! 需要源引用")
			}
			number = v.Range.FirstRow
			if node.op == "COLUMN" {
				number = v.Range.FirstCol
			}
			length, outRows, outCols := rows, rows, 1
			if node.op == "COLUMN" {
				length, outRows, outCols = cols, 1, cols
			}
			if length > 1 {
				if length > maxArrayCells {
					return FormulaValue{}, errors.New("#NUM! 数组超过20万格")
				}
				values := make([]FormulaValue, length)
				for i := range values {
					values[i] = NumberValue(big.NewRat(int64(number+i), 1))
				}
				return arrayValue(values, outRows, outCols), nil
			}
		}
		return integerResult(int64(number))
	case "ADDRESS":
		r, err := integerArg(0, 1)
		if err != nil {
			return FormulaValue{}, err
		}
		c, err := integerArg(1, 1)
		if err != nil {
			return FormulaValue{}, err
		}
		absolute, err := integerArg(2, 1)
		if err != nil || r < 1 || r > formats.ExternalRows || c < 1 || c > formats.ExcelColumns || absolute < 1 || absolute > 4 {
			return FormulaValue{}, errors.New("#VALUE! 地址参数越界")
		}
		a1 := true
		if len(node.args) > 3 {
			v, err := e.eval(node.args[3])
			if err != nil {
				return FormulaValue{}, err
			}
			a1, err = formulaBool(v)
			if err != nil {
				return FormulaValue{}, err
			}
		}
		text := ""
		if a1 {
			address := formulaAddress(int(c), int(r))
			letters := strings.TrimRight(address, "0123456789")
			if absolute == 1 || absolute == 3 {
				letters = "$" + letters
			}
			row := strconv.FormatInt(r, 10)
			if absolute == 1 || absolute == 2 {
				row = "$" + row
			}
			text = letters + row
		} else {
			rr, cc := strconv.FormatInt(r, 10), strconv.FormatInt(c, 10)
			if absolute == 3 || absolute == 4 {
				rr = "[" + rr + "]"
			}
			if absolute == 2 || absolute == 4 {
				cc = "[" + cc + "]"
			}
			text = "R" + rr + "C" + cc
		}
		if len(node.args) > 4 {
			v, err := e.eval(node.args[4])
			if err != nil {
				return FormulaValue{}, err
			}
			s, err := formulaText(v)
			if err != nil {
				return FormulaValue{}, err
			}
			text = "'" + strings.ReplaceAll(s, "'", "''") + "'!" + text
		}
		return TextValue(text), nil
	case "INDIRECT":
		v, err := e.eval(node.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		text, err := formulaText(v)
		if err != nil {
			return FormulaValue{}, err
		}
		if len(node.args) > 1 {
			a1, err := e.eval(node.args[1])
			if err != nil {
				return FormulaValue{}, err
			}
			yes, err := formulaBool(a1)
			if err != nil || !yes {
				return FormulaValue{}, errors.New("#REF! INDIRECT 目前支持A1地址")
			}
		}
		ref, ok := reference(strings.TrimSpace(text))
		if !ok || ref.length != len(strings.TrimSpace(text)) {
			return FormulaValue{}, errors.New("#REF! 地址无效")
		}
		return e.eval(&formulaNode{kind: "ref", ref: ref})
	case "OFFSET":
		base := node.args[0]
		if base.kind != "ref" {
			return FormulaValue{}, errors.New("#VALUE! OFFSET 需要源引用")
		}
		v, err := rangeArg(e, base)
		if err != nil {
			return FormulaValue{}, err
		}
		rows, cols, err := rangeShape(v)
		if err != nil {
			return FormulaValue{}, err
		}
		dr, err := integerArg(1, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		dc, err := integerArg(2, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		height, err := integerArg(3, int64(rows))
		if err != nil {
			return FormulaValue{}, err
		}
		width, err := integerArg(4, int64(cols))
		if err != nil {
			return FormulaValue{}, err
		}
		r, c := v.Range.FirstRow+int(dr), v.Range.FirstCol+int(dc)
		if r < 1 || c < 1 || height < 1 || width < 1 || r > formats.ExternalRows || c > formats.ExcelColumns || height > int64(formats.ExternalRows-r+1) || width > int64(formats.ExcelColumns-c+1) {
			return FormulaValue{}, errors.New("#REF! OFFSET 超出源坐标")
		}
		ref := formulaReference{sheet: base.ref.sheet, start: formulaAddress(c, r), end: formulaAddress(c+int(width)-1, r+int(height)-1)}
		if height == 1 && width == 1 {
			ref.end = ""
		}
		return e.eval(&formulaNode{kind: "ref", ref: ref})
	case "INDEX":
		v, err := rangeArg(e, node.args[0])
		if err != nil {
			return FormulaValue{}, err
		}
		rows, cols, err := rangeShape(v)
		if err != nil {
			return FormulaValue{}, err
		}
		r, err := integerArg(1, 0)
		if err != nil {
			return FormulaValue{}, err
		}
		defaultCol := int64(1)
		if len(node.args) == 3 {
			defaultCol = 0
		}
		c, err := integerArg(2, defaultCol)
		if err != nil {
			return FormulaValue{}, err
		}
		if len(node.args) == 2 && rows == 1 {
			c, r = r, 1
		}
		if r == 0 || c == 0 {
			row, col, rr, cc := int(r)-1, int(c)-1, 1, 1
			if r == 0 {
				row = 0
				rr = rows
			}
			if c == 0 {
				col = 0
				cc = cols
			}
			return subsetRange(e, v, row, col, rr, cc)
		}
		if r < 1 || r > int64(rows) || c < 1 || c > int64(cols) {
			return FormulaValue{}, errors.New("#REF! INDEX 下标越界")
		}
		return valueAt(e, v, (int(r)-1)*cols+int(c)-1)
	}
	needle, err := e.eval(node.args[0])
	if err != nil {
		return FormulaValue{}, err
	}
	table, err := rangeArg(e, node.args[1])
	if err != nil {
		return FormulaValue{}, err
	}
	rows, cols, err := rangeShape(table)
	if err != nil {
		return FormulaValue{}, err
	}
	vector, output := table, table
	mode, direction, wildcard := 0, 1, false
	switch node.op {
	case "MATCH", "XMATCH":
		m, err := integerArg(2, 1)
		if node.op == "XMATCH" {
			m, err = integerArg(2, 0)
		}
		if err != nil {
			return FormulaValue{}, err
		}
		if node.op == "MATCH" {
			if m < -1 || m > 1 {
				return FormulaValue{}, errors.New("#VALUE! MATCH 模式无效")
			}
			mode = -int(m)
			wildcard = m == 0
			if m != 0 {
				direction = -1
			}
		} else {
			if m < -1 || m > 2 {
				return FormulaValue{}, errors.New("#VALUE! XMATCH 模式无效")
			}
			mode = int(m)
			wildcard = m == 2
			d, err := integerArg(3, 1)
			if err != nil || d != 1 && d != -1 {
				return FormulaValue{}, errors.New("#VALUE! 仅支持正向或反向查找")
			}
			direction = int(d)
		}
	case "VLOOKUP", "HLOOKUP":
		index, err := integerArg(2, 1)
		if err != nil || index < 1 {
			return FormulaValue{}, errors.New("#VALUE! 返回序号无效")
		}
		approx := true
		if len(node.args) > 3 {
			v, err := e.eval(node.args[3])
			if err != nil {
				return FormulaValue{}, err
			}
			approx, err = formulaBool(v)
			if err != nil {
				return FormulaValue{}, err
			}
		}
		if approx {
			mode = -1
			direction = -1
		}
		wildcard = !approx
		if node.op == "VLOOKUP" {
			if index > int64(cols) {
				return FormulaValue{}, errors.New("#REF! 返回列超界")
			}
			vector, err = subsetRange(e, table, 0, 0, rows, 1)
			if err == nil {
				output, err = subsetRange(e, table, 0, int(index)-1, rows, 1)
			}
		} else {
			if index > int64(rows) {
				return FormulaValue{}, errors.New("#REF! 返回行超界")
			}
			vector, err = subsetRange(e, table, 0, 0, 1, cols)
			if err == nil {
				output, err = subsetRange(e, table, int(index)-1, 0, 1, cols)
			}
		}
		if err != nil {
			return FormulaValue{}, err
		}
	case "XLOOKUP":
		output, err = rangeArg(e, node.args[2])
		if err != nil {
			return FormulaValue{}, err
		}
		m, err := integerArg(4, 0)
		if err != nil || m < -1 || m > 2 {
			return FormulaValue{}, errors.New("#VALUE! XLOOKUP 匹配方式无效")
		}
		mode = int(m)
		wildcard = m == 2
		d, err := integerArg(5, 1)
		if err != nil || d != 1 && d != -1 {
			return FormulaValue{}, errors.New("#VALUE! 仅支持正向或反向查找")
		}
		direction = int(d)
		nr, nc, err := rangeShape(output)
		if err != nil || cols == 1 && nr != rows || cols != 1 && (rows != 1 || nc != cols) {
			return FormulaValue{}, errors.New("#VALUE! 查找与返回区域尺寸不一致")
		}
	case "LOOKUP":
		mode = -1
		direction = -1
		if len(node.args) == 3 {
			output, err = rangeArg(e, node.args[2])
			if err != nil {
				return FormulaValue{}, err
			}
			nr, nc, err := rangeShape(output)
			if err != nil || nr*nc != rows*cols || nr != 1 && nc != 1 {
				return FormulaValue{}, errors.New("#VALUE! LOOKUP 返回区域尺寸不一致")
			}
		} else if rows >= cols {
			vector, err = subsetRange(e, table, 0, 0, rows, 1)
			if err == nil {
				output, err = subsetRange(e, table, 0, cols-1, rows, 1)
			}
		} else {
			vector, err = subsetRange(e, table, 0, 0, 1, cols)
			if err == nil {
				output, err = subsetRange(e, table, rows-1, 0, 1, cols)
			}
		}
		if err != nil {
			return FormulaValue{}, err
		}
	}
	index, err := lookupIndex(e, needle, vector, mode, direction, wildcard)
	if err != nil {
		if node.op == "XLOOKUP" && len(node.args) > 3 && strings.HasPrefix(err.Error(), "#N/A") {
			return e.eval(node.args[3])
		}
		return FormulaValue{}, err
	}
	if node.op == "MATCH" || node.op == "XMATCH" {
		return NumberValue(big.NewRat(int64(index+1), 1)), nil
	}
	if node.op == "XLOOKUP" {
		nr, nc, err := rangeShape(output)
		if err != nil {
			return FormulaValue{}, err
		}
		if cols == 1 && nc > 1 {
			return subsetRange(e, output, index, 0, 1, nc)
		}
		if rows == 1 && nr > 1 {
			return subsetRange(e, output, 0, index, nr, 1)
		}
	}
	return valueAt(e, output, index)
}
