package editfile

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type axisEdit struct {
	editdoc.Structure
	rows   bool
	limit  int
	action string
}

func newAxisEdit(op editdoc.Structure, layout XLSXLayout) (axisEdit, error) {
	a := axisEdit{Structure: op}
	if !layout.HasSheet(op.Sheet) {
		return a, ErrTarget
	}
	for _, sheet := range layout.Sheets {
		if strings.EqualFold(sheet.Name, op.Sheet) {
			a.Sheet = sheet.Name
			break
		}
	}
	op.Sheet = a.Sheet
	if op.Kind == "renameSheet" {
		a.action = "rename"
		if op.Name == "" || len(utf16.Encode([]rune(op.Name))) > 31 || strings.TrimSpace(op.Name) == "" ||
			strings.ContainsAny(op.Name, "[]:*?/\\\r\n\t") || strings.HasPrefix(op.Name, "'") || strings.HasSuffix(op.Name, "'") || strings.EqualFold(op.Name, "History") {
			return a, errors.New("工作表名称无效或超过 31 个字符")
		}
		for _, s := range layout.Sheets {
			if s.Name != op.Sheet && strings.EqualFold(s.Name, op.Name) {
				return a, errors.New("工作表名称已存在")
			}
		}
		if op.Sheet == op.Name {
			return a, editdoc.ErrNoChange
		}
		return a, nil
	}
	switch {
	case strings.HasSuffix(op.Kind, "Rows"):
		a.rows = true
		a.limit = formats.ExcelRows
		a.action = strings.TrimSuffix(op.Kind, "Rows")
	case strings.HasSuffix(op.Kind, "Columns"):
		a.limit = formats.ExcelColumns
		a.action = strings.TrimSuffix(op.Kind, "Columns")
	default:
		return a, editdoc.ErrInvalid
	}
	if !slices.Contains([]string{"insert", "delete", "move"}, a.action) || op.At < 1 || op.At > a.limit || op.Count < 1 || op.Count > a.limit-op.At+1 {
		return a, ErrTarget
	}
	if a.action == "move" && (op.Before < 1 || op.Before > a.limit+1 || op.Before >= op.At && op.Before <= op.At+op.Count) {
		return a, errors.New("移动目标应位于源区域之外")
	}
	return a, nil
}

// point maps a stored coordinate. Zero denotes a deleted member.
func (a axisEdit) point(x int) int {
	end := a.At + a.Count
	switch a.action {
	case "insert":
		if x >= a.At {
			return x + a.Count
		}
	case "delete":
		if x >= end {
			return x - a.Count
		}
		if x >= a.At {
			return 0
		}
	case "move":
		if x >= a.At && x < end {
			if a.Before < a.At {
				return a.Before + x - a.At
			}
			return a.Before - a.Count + x - a.At
		}
		if x >= a.Before && x < a.At {
			return x + a.Count
		}
		if x >= end && x < a.Before {
			return x - a.Count
		}
	}
	return x
}

// True when the operation changes the membership or order inside an object.
// Moving all its columns or rows together carries the object intact.
func (a axisEdit) changesMembers(lo, hi int) bool {
	last := a.At + a.Count - 1
	switch a.action {
	case "insert":
		return a.At > lo && a.At <= hi
	case "delete":
		return a.At <= hi && last >= lo
	case "move":
		if a.At <= lo && last >= hi {
			return false
		}
		return a.At <= hi && last >= lo || a.Before > lo && a.Before <= hi
	}
	return false
}

func (a axisEdit) edges(r cellRect) (int, int) {
	if a.rows {
		return r.firstRow, r.lastRow
	}
	return r.firstCol, r.lastCol
}

func (a axisEdit) interval(lo, hi int) (int, int, error) {
	if lo > hi {
		x, y, err := a.interval(hi, lo)
		return y, x, err
	}
	// Entire-axis references stay entire when Excel inserts at a boundary.
	if lo == 1 && hi == a.limit {
		return lo, hi, nil
	}
	if a.action == "delete" {
		end := a.At + a.Count - 1
		if lo >= a.At && hi <= end {
			return 0, 0, nil
		}
		if lo >= a.At && lo <= end {
			lo = end + 1
		}
		if hi >= a.At && hi <= end {
			hi = a.At - 1
		}
	}
	left, right := a.point(lo), a.point(hi)
	if a.action == "move" {
		// An intact range may move with the cut or be crossed as a whole.
		// Reject a split range instead of silently including intervening cells.
		minX, maxX := min(left, right), max(left, right)
		for _, x := range []int{a.At - 1, a.At, a.At + a.Count - 1, a.At + a.Count, a.Before - 1, a.Before} {
			if x >= lo && x <= hi {
				y := a.point(x)
				minX, maxX = min(minX, y), max(maxX, y)
			}
		}
		if maxX-minX != hi-lo {
			return 0, 0, errors.New("此次移动会把现有引用范围拆为不连续的区域")
		}
		left, right = minX, maxX
	}
	if left > a.limit || right > a.limit {
		return 0, 0, errors.New("变换后的引用超出工作表边界")
	}
	return left, right, nil
}

type addressAxis struct {
	letters, number string
	col, row        int
}

func axisAddress(ref string) addressAxis {
	i := 0
	if strings.HasPrefix(ref, "$") {
		i++
	}
	start := i
	for i < len(ref) && (ref[i] >= 'A' && ref[i] <= 'Z' || ref[i] >= 'a' && ref[i] <= 'z') {
		i++
	}
	var v addressAxis
	if i > start {
		v.letters = ref[:i]
		v.col, _, _ = workbook.ParseCellRef(strings.ToUpper(ref[start:i]) + "1")
	} else {
		i = 0
	}
	v.number = ref[i:]
	v.row, _ = strconv.Atoi(strings.TrimPrefix(v.number, "$"))
	return v
}
func (v addressAxis) shifted(rows bool, x int) string {
	if rows {
		if x != v.row {
			v.number = strings.TrimSuffix(v.number, strings.TrimPrefix(v.number, "$")) + strconv.Itoa(x)
		}
	} else if x != v.col {
		v.letters = strings.TrimSuffix(v.letters, strings.TrimPrefix(v.letters, "$")) + workbook.ColumnName(x)
	}
	return v.letters + v.number
}

func (a axisEdit) address(raw string) (string, error) {
	first, last, pair := strings.Cut(raw, ":")
	if !pair {
		last = first
	}
	x, y := axisAddress(first), axisAddress(last)
	start, end := x.col, y.col
	if a.rows {
		start, end = x.row, y.row
	}
	if start == 0 || end == 0 {
		return raw, nil
	}
	lo, hi, err := a.interval(start, end)
	if err != nil {
		return "", err
	}
	if lo == 0 {
		return "#REF!", nil
	}
	out := x.shifted(a.rows, lo)
	if pair {
		out += ":" + y.shifted(a.rows, hi)
	}
	return out, nil
}

func (a axisEdit) formula(body, sheet string) (string, error) {
	return calculator.RewriteReferences(body, func(label, q, raw string) (string, error) {
		if strings.ContainsAny(label, "[]:") {
			return "", errors.New("结构变更涉及三维或外部引用")
		}
		target := label
		if target == "" {
			target = sheet
		}
		if !strings.EqualFold(target, a.Sheet) {
			return q + raw, nil
		}
		if a.action == "rename" {
			if label != "" {
				q = "'" + strings.ReplaceAll(a.Name, "'", "''") + "'!"
			}
			return q + raw, nil
		}
		if raw == "" {
			return q, nil
		}
		v, err := a.address(raw)
		if err != nil {
			return "", fmt.Errorf("%s：%w", raw, err)
		}
		return q + v, nil
	})
}
