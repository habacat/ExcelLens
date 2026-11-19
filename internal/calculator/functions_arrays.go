package calculator

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
)

func arrayValue(values []FormulaValue, rows, cols int) FormulaValue {
	r := &RangeValue{Rows: rows, Cols: cols, Ordered: true}
	r.Visit = func(fn func(int, FormulaValue) error) error {
		for i, v := range values {
			if err := fn(i, v); err != nil {
				return err
			}
		}
		return nil
	}
	r.At = func(i int) (FormulaValue, error) {
		if i < 0 || i >= len(values) {
			return FormulaValue{}, errors.New("#REF! 数组下标越界")
		}
		return values[i], valueFault(values[i])
	}
	r.Load = func() ([]FormulaValue, error) { return values, nil }
	return FormulaValue{Kind: "range", Range: r}
}
func (e *formulaEval) arrayResult(v FormulaValue) (Result, error) {
	values, rows, cols, err := materialize(e, v, maxArrayCells)
	if err != nil {
		return Result{}, err
	}
	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	writer.Comma = '\t'
	for row := 0; row < rows; row++ {
		fields := make([]string, cols)
		for col := 0; col < cols; col++ {
			item := values[row*cols+col]
			if item.Kind == "error" {
				fields[col] = formulaErrorCode(item.Text)
			} else if item.Kind == "blank" {
				fields[col] = "0"
			} else {
				fields[col], err = formulaText(item)
				if err != nil {
					return Result{}, err
				}
			}
		}
		if err = writer.Write(fields); err != nil {
			return Result{}, err
		}
		writer.Flush()
		if out.Len() > maxFormulaText {
			return Result{}, errors.New("#NUM! 数组复制结果超过64KiB")
		}
	}
	copy := strings.TrimSuffix(out.String(), "\n")
	return Result{Kind: "array", Value: copy, Display: fmt.Sprintf("%d行 × %d列", rows, cols), Copy: copy, Exact: copy, Approximate: e.approximate, Steps: []Step{}, Used: []string{}}, nil
}

type formulaArray struct {
	values     []FormulaValue
	rows, cols int
}

func (e *formulaEval) arrayData(v FormulaValue) (formulaArray, error) {
	values, rows, cols, err := materialize(e, v, maxArrayCells)
	return formulaArray{values, rows, cols}, err
}
func (a formulaArray) get(row, col int) FormulaValue {
	if a.rows == 1 {
		row = 0
	}
	if a.cols == 1 {
		col = 0
	}
	return a.values[row*a.cols+col]
}
func broadcastSize(arrays ...formulaArray) (int, int, error) {
	rows, cols := 1, 1
	for _, a := range arrays {
		rows = max(rows, a.rows)
		cols = max(cols, a.cols)
	}
	if rows*cols > maxArrayCells {
		return 0, 0, errors.New("#NUM! 数组超过20万格")
	}
	for _, a := range arrays {
		if a.rows != 1 && a.rows != rows || a.cols != 1 && a.cols != cols {
			return 0, 0, errors.New("#VALUE! 数组尺寸不能对应")
		}
	}
	return rows, cols, nil
}
func (e *formulaEval) arrayBinary(op string, left, right FormulaValue) (FormulaValue, error) {
	a, err := e.arrayData(left)
	if err != nil {
		return FormulaValue{}, err
	}
	b, err := e.arrayData(right)
	if err != nil {
		return FormulaValue{}, err
	}
	rows, cols, err := broadcastSize(a, b)
	if err != nil {
		return FormulaValue{}, err
	}
	out := make([]FormulaValue, rows*cols)
	textBytes := 0
	for r := 0; r < rows; r++ {
		if err = e.ctx.Err(); err != nil {
			return FormulaValue{}, err
		}
		for c := 0; c < cols; c++ {
			v, err := e.binaryValue(op, a.get(r, c), b.get(r, c))
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
func (e *formulaEval) arrayUnary(op string, value FormulaValue) (FormulaValue, error) {
	a, err := e.arrayData(value)
	if err != nil {
		return FormulaValue{}, err
	}
	out := make([]FormulaValue, len(a.values))
	for i, v := range a.values {
		number, err := formulaNumber(v)
		if fault := valueFault(v); fault != nil {
			err = fault
		}
		if err != nil {
			out[i] = errorValue(err)
			continue
		}
		if op == "-" {
			number.Neg(number)
		}
		if op == "%" {
			number.Quo(number, big.NewRat(100, 1))
		}
		out[i] = NumberValue(number)
	}
	return arrayValue(out, a.rows, a.cols), nil
}
func (e *formulaEval) arrayIf(n *formulaNode, test FormulaValue) (FormulaValue, error) {
	condition, err := e.arrayData(test)
	if err != nil {
		return FormulaValue{}, err
	}
	yesNeeded, noNeeded := false, false
	for _, v := range condition.values {
		yes, err := formulaBool(v)
		if err == nil {
			yesNeeded = yesNeeded || yes
			noNeeded = noNeeded || !yes
		}
	}
	yes, no := BoolValue(false), BoolValue(false)
	if yesNeeded {
		yes, err = e.eval(n.args[1])
		if err != nil {
			if e.ctx.Err() != nil {
				return FormulaValue{}, e.ctx.Err()
			}
			yes = errorValue(err)
		}
	}
	if noNeeded && len(n.args) > 2 {
		no, err = e.eval(n.args[2])
		if err != nil {
			if e.ctx.Err() != nil {
				return FormulaValue{}, e.ctx.Err()
			}
			no = errorValue(err)
		}
	}
	a, err := e.arrayData(yes)
	if err != nil {
		return FormulaValue{}, err
	}
	b, err := e.arrayData(no)
	if err != nil {
		return FormulaValue{}, err
	}
	rows, cols, err := broadcastSize(condition, a, b)
	if err != nil {
		return FormulaValue{}, err
	}
	out := make([]FormulaValue, rows*cols)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			v := condition.get(r, c)
			if err := valueFault(v); err != nil {
				out[r*cols+c] = v
				continue
			}
			yes, err := formulaBool(v)
			if err != nil {
				out[r*cols+c] = errorValue(err)
			} else if yes {
				out[r*cols+c] = a.get(r, c)
			} else {
				out[r*cols+c] = b.get(r, c)
			}
		}
	}
	return arrayValue(out, rows, cols), nil
}
func registerArrayFunctions(add functionRegistrar) {
	add("SEQUENCE", "行数, [列数], [起点], [步长]", 1, 4, arrayFunction)
	add("TRANSPOSE", "数组", 1, 1, arrayFunction)
	add("FILTER", "数组, 条件数组, [无结果时]", 2, 3, arrayFunction)
	add("SORT", "数组, [排序序号], [顺序], [按列排序]", 1, 4, arrayFunction)
	add("UNIQUE", "数组, [按列], [仅出现一次]", 1, 3, arrayFunction)
	add("TAKE,DROP", "数组, 行数, [列数]", 2, 3, arrayFunction)
	add("CHOOSECOLS,CHOOSEROWS", "数组, 序号1, …", 2, 128, arrayFunction)
	add("HSTACK,VSTACK", "数组1, …", 1, 128, arrayFunction)
	add("TOCOL,TOROW", "数组, [忽略项], [按列扫描]", 1, 3, arrayFunction)
	add("TEXTSPLIT", "文本, 列分隔符, [行分隔符], [忽略空项], [忽略大小写], [填充值]", 2, 6, arrayFunction)
}
func arrayFunction(e *formulaEval, n *formulaNode) (FormulaValue, error) {
	integer := func(i int, def int64) (int64, error) {
		if i >= len(n.args) {
			return def, nil
		}
		v, err := e.eval(n.args[i])
		if err != nil {
			return 0, err
		}
		if v.Kind == "blank" {
			return def, nil
		}
		return formulaInteger(v)
	}
	boolean := func(i int, def bool) (bool, error) {
		if i >= len(n.args) {
			return def, nil
		}
		v, err := e.eval(n.args[i])
		if err != nil {
			return false, err
		}
		return formulaBool(v)
	}
	if n.op == "SEQUENCE" {
		rows, err := integer(0, 1)
		if err != nil {
			return FormulaValue{}, err
		}
		cols, err := integer(1, 1)
		if err != nil || rows < 1 || cols < 1 {
			return FormulaValue{}, errors.New("#NUM! SEQUENCE 尺寸无效")
		}
		if rows > maxArrayCells || cols > maxArrayCells || rows*cols > maxArrayCells {
			return FormulaValue{}, errors.New("#NUM! 数组超过20万格")
		}
		start, step := big.NewRat(1, 1), big.NewRat(1, 1)
		for i, target := range []*big.Rat{start, step} {
			if i+2 < len(n.args) {
				v, err := e.eval(n.args[i+2])
				if err != nil {
					return FormulaValue{}, err
				}
				number, err := formulaNumber(v)
				if err != nil {
					return FormulaValue{}, err
				}
				target.Set(number)
			}
		}
		out := make([]FormulaValue, int(rows*cols))
		for i := range out {
			out[i] = NumberValue(new(big.Rat).Add(start, new(big.Rat).Mul(step, big.NewRat(int64(i), 1))))
		}
		return arrayValue(out, int(rows), int(cols)), nil
	}
	if n.op == "TEXTSPLIT" {
		vals, err := evaluatedArgs(e, n)
		if err != nil {
			return FormulaValue{}, err
		}
		text, err := formulaText(vals[0])
		if err != nil {
			return FormulaValue{}, err
		}
		delimiter, err := formulaText(vals[1])
		if err != nil {
			return FormulaValue{}, err
		}
		rowDelimiter := ""
		if len(vals) > 2 {
			rowDelimiter, err = formulaText(vals[2])
			if err != nil {
				return FormulaValue{}, err
			}
		}
		if delimiter == "" && rowDelimiter == "" {
			return FormulaValue{}, errors.New("#VALUE! 至少需要一个分隔符")
		}
		ignore, err := boolean(3, false)
		if err != nil {
			return FormulaValue{}, err
		}
		insensitive, err := boolean(4, false)
		if err != nil {
			return FormulaValue{}, err
		}
		split := func(text, delimiter string) []string {
			if delimiter == "" {
				return []string{text}
			}
			if insensitive {
				// Match literal delimiters while preserving source byte offsets.
				pieces := []string{}
				from := 0
				for i := 0; i+len(delimiter) <= len(text); i++ {
					if strings.EqualFold(text[i:i+len(delimiter)], delimiter) {
						pieces = append(pieces, text[from:i])
						i += len(delimiter) - 1
						from = i + 1
					}
				}
				return append(pieces, text[from:])
			}
			return strings.Split(text, delimiter)
		}
		rows := split(text, rowDelimiter)
		grid := [][]FormulaValue{}
		width := 0
		for _, row := range rows {
			parts := split(row, delimiter)
			values := []FormulaValue{}
			for _, part := range parts {
				if ignore && part == "" {
					continue
				}
				values = append(values, TextValue(part))
			}
			if len(values) > 0 {
				grid = append(grid, values)
				width = max(width, len(values))
			}
		}
		if len(grid) == 0 {
			return FormulaValue{}, errors.New("#CALC! 没有结果")
		}
		if len(grid)*width > maxArrayCells {
			return FormulaValue{}, errors.New("#NUM! 数组过大")
		}
		pad := errorValue(errors.New("#N/A"))
		if len(vals) > 5 {
			pad = vals[5]
		}
		out := make([]FormulaValue, len(grid)*width)
		for i := range out {
			out[i] = pad
		}
		for r, row := range grid {
			copy(out[r*width:], row)
		}
		return arrayValue(out, len(grid), width), nil
	}
	if n.op == "HSTACK" || n.op == "VSTACK" {
		arrays := []formulaArray{}
		rows, cols := 0, 0
		for _, arg := range n.args {
			v, err := rangeArg(e, arg)
			if err != nil {
				return FormulaValue{}, err
			}
			a, err := e.arrayData(v)
			if err != nil {
				return FormulaValue{}, err
			}
			arrays = append(arrays, a)
			if n.op == "HSTACK" {
				rows = max(rows, a.rows)
				cols += a.cols
			} else {
				rows += a.rows
				cols = max(cols, a.cols)
			}
		}
		if rows*cols > maxArrayCells {
			return FormulaValue{}, errors.New("#NUM! 数组过大")
		}
		out := make([]FormulaValue, rows*cols)
		for i := range out {
			out[i] = errorValue(errors.New("#N/A"))
		}
		rr, cc := 0, 0
		for _, a := range arrays {
			for r := 0; r < a.rows; r++ {
				for c := 0; c < a.cols; c++ {
					out[(rr+r)*cols+cc+c] = a.get(r, c)
				}
			}
			if n.op == "HSTACK" {
				cc += a.cols
			} else {
				rr += a.rows
			}
		}
		return arrayValue(out, rows, cols), nil
	}
	v, err := rangeArg(e, n.args[0])
	if err != nil {
		return FormulaValue{}, err
	}
	a, err := e.arrayData(v)
	if err != nil {
		return FormulaValue{}, err
	}
	switch n.op {
	case "TRANSPOSE":
		out := make([]FormulaValue, len(a.values))
		for r := 0; r < a.rows; r++ {
			for c := 0; c < a.cols; c++ {
				out[c*a.rows+r] = a.get(r, c)
			}
		}
		return arrayValue(out, a.cols, a.rows), nil
	case "TAKE", "DROP":
		rows, err := integer(1, int64(a.rows))
		if err != nil {
			return FormulaValue{}, err
		}
		cols, err := integer(2, int64(a.cols))
		if err != nil {
			return FormulaValue{}, err
		}
		r0, c0, nr, nc := 0, 0, a.rows, a.cols
		if n.op == "TAKE" {
			if rows == 0 || cols == 0 {
				return FormulaValue{}, errors.New("#CALC! 空数组")
			}
			nr = min(a.rows, absInt(int(rows)))
			nc = min(a.cols, absInt(int(cols)))
			if rows < 0 {
				r0 = a.rows - nr
			}
			if cols < 0 {
				c0 = a.cols - nc
			}
		} else {
			if rows > 0 {
				r0 = min(a.rows, int(rows))
			}
			nr = a.rows - min(a.rows, absInt(int(rows)))
			if len(n.args) == 2 {
				cols = 0
			}
			if cols > 0 {
				c0 = min(a.cols, int(cols))
			}
			nc = a.cols - min(a.cols, absInt(int(cols)))
			if nr < 1 || nc < 1 {
				return FormulaValue{}, errors.New("#CALC! 空数组")
			}
		}
		return subsetRange(e, arrayValue(a.values, a.rows, a.cols), r0, c0, nr, nc)
	case "CHOOSECOLS", "CHOOSEROWS":
		indices := []int{}
		size := a.cols
		if n.op == "CHOOSEROWS" {
			size = a.rows
		}
		for i := 1; i < len(n.args); i++ {
			index, err := integer(i, 0)
			if err != nil {
				return FormulaValue{}, err
			}
			if index < 0 {
				index = int64(size) + index + 1
			}
			if index < 1 || index > int64(size) {
				return FormulaValue{}, errors.New("#VALUE! 选择序号越界")
			}
			indices = append(indices, int(index)-1)
		}
		rows, cols := a.rows, len(indices)
		if n.op == "CHOOSEROWS" {
			rows, cols = len(indices), a.cols
		}
		if rows*cols > maxArrayCells {
			return FormulaValue{}, errors.New("#NUM! 数组过大")
		}
		out := make([]FormulaValue, rows*cols)
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				rr, cc := r, c
				if n.op == "CHOOSEROWS" {
					rr = indices[r]
				} else {
					cc = indices[c]
				}
				out[r*cols+c] = a.get(rr, cc)
			}
		}
		return arrayValue(out, rows, cols), nil
	case "TOCOL", "TOROW":
		ignore, err := integer(1, 0)
		if err != nil || ignore < 0 || ignore > 3 {
			return FormulaValue{}, errors.New("#VALUE! 忽略类型无效")
		}
		byCol, err := boolean(2, false)
		if err != nil {
			return FormulaValue{}, err
		}
		out := []FormulaValue{}
		for i := range a.values {
			r, c := i/a.cols, i%a.cols
			if byCol {
				r, c = i%a.rows, i/a.rows
			}
			x := a.get(r, c)
			if ignore&1 != 0 && x.Kind == "blank" || ignore&2 != 0 && x.Kind == "error" {
				continue
			}
			out = append(out, x)
		}
		if len(out) == 0 {
			return FormulaValue{}, errors.New("#CALC! 空数组")
		}
		if n.op == "TOCOL" {
			return arrayValue(out, len(out), 1), nil
		}
		return arrayValue(out, 1, len(out)), nil
	case "FILTER":
		mask, err := rangeArg(e, n.args[1])
		if err != nil {
			return FormulaValue{}, err
		}
		b, err := e.arrayData(mask)
		if err != nil {
			return FormulaValue{}, err
		}
		byCol := b.rows == 1 && b.cols == a.cols
		if !(b.cols == 1 && b.rows == a.rows) && !byCol {
			return FormulaValue{}, errors.New("#VALUE! FILTER 条件尺寸不一致")
		}
		out := []FormulaValue{}
		kept := []int{}
		for i, x := range b.values {
			if err := valueFault(x); err != nil {
				return FormulaValue{}, err
			}
			yes, err := formulaBool(x)
			if err != nil {
				return FormulaValue{}, err
			}
			if yes {
				kept = append(kept, i)
			}
		}
		if len(kept) == 0 {
			if len(n.args) > 2 {
				return e.eval(n.args[2])
			}
			return FormulaValue{}, errors.New("#CALC! 没有符合条件的结果")
		}
		rows, cols := len(kept), a.cols
		if byCol {
			rows, cols = a.rows, len(kept)
		}
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				rr, cc := r, c
				if byCol {
					cc = kept[c]
				} else {
					rr = kept[r]
				}
				out = append(out, a.get(rr, cc))
			}
		}
		return arrayValue(out, rows, cols), nil
	case "SORT", "UNIQUE":
		byCol := false
		var err error
		if n.op == "SORT" {
			byCol, err = boolean(3, false)
		} else {
			byCol, err = boolean(1, false)
		}
		if err != nil {
			return FormulaValue{}, err
		}
		size, width := a.rows, a.cols
		if byCol {
			size, width = a.cols, a.rows
		}
		at := func(i, j int) FormulaValue {
			if byCol {
				return a.get(j, i)
			}
			return a.get(i, j)
		}
		indices := make([]int, size)
		for i := range indices {
			indices[i] = i
		}
		if n.op == "SORT" {
			sortIndex, err := integer(1, 1)
			if err != nil || sortIndex < 1 || sortIndex > int64(width) {
				return FormulaValue{}, errors.New("#VALUE! 排序序号无效")
			}
			order, err := integer(2, 1)
			if err != nil || order != 1 && order != -1 {
				return FormulaValue{}, errors.New("#VALUE! 排序顺序无效")
			}
			sort.SliceStable(indices, func(i, j int) bool {
				x, y := at(indices[i], int(sortIndex)-1), at(indices[j], int(sortIndex)-1)
				if x.Kind == "error" || y.Kind == "error" {
					return x.Kind != "error" && y.Kind == "error"
				}
				cmp, _ := compareFormulaValues(x, y)
				return cmp*int(order) < 0
			})
		} else {
			once, err := boolean(2, false)
			if err != nil {
				return FormulaValue{}, err
			}
			counts := map[string]int{}
			keys := make([]string, size)
			for i := 0; i < size; i++ {
				var key strings.Builder
				for j := 0; j < width; j++ {
					x := at(i, j)
					text, _ := formulaText(x)
					if x.Kind == "error" {
						text = x.Text
					}
					fmt.Fprintf(&key, "%d:%s:%d:%s;", len(x.Kind), x.Kind, len(strings.ToUpper(text)), strings.ToUpper(text))
				}
				keys[i] = key.String()
				counts[keys[i]]++
			}
			seen := map[string]bool{}
			indices = indices[:0]
			for i, key := range keys {
				if once && counts[key] != 1 || !once && seen[key] {
					continue
				}
				seen[key] = true
				indices = append(indices, i)
			}
			if len(indices) == 0 {
				return FormulaValue{}, errors.New("#CALC! 没有唯一结果")
			}
		}
		rows, cols := len(indices), a.cols
		if byCol {
			rows, cols = a.rows, len(indices)
		}
		out := make([]FormulaValue, rows*cols)
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				rr, cc := r, c
				if byCol {
					cc = indices[c]
				} else {
					rr = indices[r]
				}
				out[r*cols+c] = a.get(rr, cc)
			}
		}
		return arrayValue(out, rows, cols), nil
	}
	return FormulaValue{}, errors.New("#NAME? 未注册数组函数")
}
