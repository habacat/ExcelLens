package workbook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// InspectOptions drives read-only Excel inspection without loading entire sheets
// or materializing selection-sized arrays. Row and column coordinates ALWAYS
// refer to the source worksheet, even when the screen is filtered or sorted.
type InspectOptions struct {
	Mode              string
	FirstRow, LastRow int
	FirstCol, LastCol int
	Limit             int
	AllowedRows       map[int]struct{} // optional: visible rows in an active filter
	RowNumbers        []int            // optional: explicit selection, in source order
}

type InspectOutcome struct {
	Mode            string         `json:"mode"`
	Matches         []SearchResult `json:"matches"`
	Matched         int            `json:"matched"`
	Scanned         int64          `json:"scanned"`
	DuplicateGroups int            `json:"duplicateGroups,omitempty"`
	Truncated       bool           `json:"truncated"`
}

func inspectFilled(cell CellData) bool {
	// Style-only cells and empty shared strings are not content. A formula is
	// content even if its saved cached result is blank/missing.
	return cell.HasFormula || cell.Raw != "" || cell.Display != ""
}

func inspectMatch(c CellData, mode string) bool {
	switch mode {
	case "formulas":
		return c.HasFormula
	case "constants":
		return !c.HasFormula && inspectFilled(c)
	case "errors":
		return c.Type == "e" && (c.Raw != "" || c.Display != "")
	case "missing_cache":
		return c.HasFormula && c.CacheMissing
	case "blanks":
		return !inspectFilled(c)
	default:
		return false
	}
}

func inspectValueKey(c CellData) string {
	if !inspectFilled(c) || c.CacheMissing {
		return ""
	}
	raw := c.Raw
	// Excel numbers are values, not number-formatted strings. By using the
	// raw number, 1234 remains the same key regardless of displayed currency
	// or date formatting. Normalizing well-formed numbers treats 1 and 1.0
	// alike while retaining the exact value held by Excel's numeric storage.
	if c.Type == "n" || c.Type == "" {
		if key, ok := inspectNumericKey(raw); ok {
			return "n:" + key
		}
		return "n:" + raw
	}
	if c.Type == "b" || c.Type == "e" {
		return c.Type + ":" + raw
	}
	return "s:" + strings.ToLower(raw)
}

// Canonical decimal identity without float rounding or expanding exponents.
// Even a hostile 1e1000000000 stays a short key, rather than a huge big.Int.
func inspectNumericKey(raw string) (string, bool) {
	s := raw
	sign := ""
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		if s[0] == '-' {
			sign = "-"
		}
		s = s[1:]
	}
	var exponent int64
	if at := strings.IndexAny(s, "eE"); at >= 0 {
		var err error
		exponent, err = strconv.ParseInt(s[at+1:], 10, 32)
		if err != nil {
			return "", false
		}
		s = s[:at]
	}
	point, digits := false, 0
	for _, ch := range s {
		if ch == '.' && !point {
			point = true
			continue
		}
		if ch < '0' || ch > '9' {
			return "", false
		}
		digits++
		if point {
			exponent--
		}
	}
	if digits == 0 {
		return "", false
	}
	s = strings.TrimLeft(strings.ReplaceAll(s, ".", ""), "0")
	if s == "" {
		return "0", true
	}
	tail := len(s) - len(strings.TrimRight(s, "0"))
	exponent += int64(tail)
	s = s[:len(s)-tail]
	return sign + s + "e" + strconv.FormatInt(exponent, 10), true
}

func previewCellResult(row int, cell CellData) SearchResult {
	short := func(s string) string {
		if len(s) > 1800 {
			return strings.ToValidUTF8(s[:1800], "") + "…"
		}
		return s
	}
	return SearchResult{Ref: cell.Ref, Row: row, Col: cell.Col, Value: short(cell.Display), Formula: short(cell.Formula)}
}

// InspectSelection uses a single open XML descriptor and one reusable row
// buffer. It is O(R*row-data) for a selected range and O(U) in additional
// memory for distinct values in duplicate mode, bounded by a safety limit.
// It collects at most Limit previews, but Matched counts ALL matches.
func (w *Workbook) InspectSelection(ctx context.Context, idx *SheetIndex, opt InspectOptions) (InspectOutcome, error) {
	result := InspectOutcome{Mode: opt.Mode, Matches: make([]SearchResult, 0, minInt(opt.Limit, 1024))}
	if opt.FirstRow < 1 || opt.LastRow > MaxRowsForIndex(idx) || opt.FirstRow > opt.LastRow ||
		opt.FirstCol < 1 || opt.LastCol > 16384 || opt.FirstCol > opt.LastCol {
		return result, errors.New("invalid inspection range")
	}
	if opt.Limit < 1 || opt.Limit > 2500 {
		return result, errors.New("invalid result limit")
	}
	cols := int64(opt.LastCol - opt.FirstCol + 1)
	rows := int64(opt.LastRow - opt.FirstRow + 1)
	if opt.RowNumbers != nil {
		rows = int64(len(opt.RowNumbers))
		previous := 0
		for _, number := range opt.RowNumbers {
			if number <= previous || number < opt.FirstRow || number > opt.LastRow {
				return result, errors.New("invalid selection row order")
			}
			previous = number
		}
	}
	if rows*cols > 12_000_000 {
		return result, errors.New("选区过大：每次最多分析1200万个单元格，请先选取关键列")
	}
	switch opt.Mode {
	case "formulas", "constants", "errors", "missing_cache", "blanks", "duplicates":
	default:
		return result, errors.New("unknown inspection mode")
	}
	if opt.Mode == "duplicates" && opt.FirstCol != opt.LastCol {
		return result, errors.New("重复值检查必须选择单列")
	}
	file, err := os.Open(idx.XMLPath)
	if err != nil {
		return result, err
	}
	defer file.Close()
	var reuse []byte
	var seen map[string]struct {
		first SearchResult
		count int
	}
	var seenBytes int64
	if opt.Mode == "duplicates" {
		seen = make(map[string]struct {
			first SearchResult
			count int
		}, minInt(idx.RowCount(), 32768))
	}
	add := func(hit SearchResult) {
		if len(result.Matches) < opt.Limit {
			result.Matches = append(result.Matches, hit)
		}
	}
	start := idx.FindRowOffset(opt.FirstRow)
	end := idx.FindRowOffset(opt.LastRow + 1)
	positions := func(yield func(int, int) bool) {
		if opt.RowNumbers != nil {
			for _, number := range opt.RowNumbers {
				if !yield(number, idx.FindRowOffset(number)) {
					return
				}
			}
			return
		}
		for i := start; i < end; i++ {
			if !yield(idx.RowAt(i).Number, i) {
				return
			}
		}
	}
	nextActual := opt.FirstRow
	visited := 0
	for number, i := range positions {
		if visited&15 == 0 {
			if err := ctx.Err(); err != nil {
				return result, err
			}
		}
		visited++
		if number > opt.LastRow {
			break
		}
		// Account for completely absent XML rows when locating blanks.
		if opt.Mode == "blanks" && opt.AllowedRows == nil && opt.RowNumbers == nil && nextActual < number {
			for r := nextActual; r < number; r++ {
				if r&1023 == 0 {
					if err := ctx.Err(); err != nil {
						return result, err
					}
				}
				result.Scanned += cols
				result.Matched += int(cols)
				for col := opt.FirstCol; col <= opt.LastCol && len(result.Matches) < opt.Limit; col++ {
					add(SearchResult{Ref: fmt.Sprintf("%s%d", ColumnName(col), r), Row: r, Col: col})
				}
			}
		}
		nextActual = number + 1
		if opt.AllowedRows != nil {
			if _, ok := opt.AllowedRows[number]; !ok {
				continue
			}
		}
		row := RowData{Number: number}
		if i < idx.RowCount() && idx.RowAt(i).Number == number {
			row, err = w.fastReadRowWithOptions(file, idx, i, opt.FirstCol, opt.LastCol, nil, false, opt.Mode != "duplicates", &reuse)
			if err != nil {
				return result, err
			}
		}
		if opt.Mode == "duplicates" {
			result.Scanned++
			for _, cell := range row.Cells {
				if !inspectFilled(cell) || cell.CacheMissing {
					continue
				}
				key := inspectValueKey(cell)
				if key == "" {
					continue
				}
				previous, exists := seen[key]
				if !exists {
					seenBytes += int64(len(key)) + 256
					if seenBytes > 128<<20 {
						return result, errors.New("查重工作内存超过128 MiB预算，请缩小行范围")
					}
					if len(seen) >= 500_000 {
						return result, errors.New("本列不同值超过50万，请先缩小行范围或使用精确筛选")
					}
					seen[key] = struct {
						first SearchResult
						count int
					}{first: previewCellResult(number, cell), count: 1}
					continue
				}
				if previous.count == 1 {
					result.DuplicateGroups++
					result.Matched++
					add(previous.first)
				}
				result.Matched++
				add(previewCellResult(number, cell))
				previous.count++
				seen[key] = previous
			}
			continue
		}
		if opt.Mode == "blanks" {
			byCol := make(map[int]CellData, len(row.Cells))
			for _, cell := range row.Cells {
				byCol[cell.Col] = cell
			}
			for col := opt.FirstCol; col <= opt.LastCol; col++ {
				result.Scanned++
				c, ok := byCol[col]
				if !ok || inspectMatch(c, "blanks") {
					result.Matched++
					if len(result.Matches) < opt.Limit {
						add(SearchResult{Ref: fmt.Sprintf("%s%d", ColumnName(col), number), Row: number, Col: col})
					}
				}
			}
		} else {
			result.Scanned += cols
			for _, cell := range row.Cells {
				if inspectMatch(cell, opt.Mode) {
					result.Matched++
					if cell.Type == "" || cell.Type == "n" {
						cell.Display = w.formatNumeric(cell.Raw, cell.Style)
					}
					add(previewCellResult(number, cell))
				}
			}
		}
	}
	if opt.Mode == "blanks" && opt.AllowedRows == nil && opt.RowNumbers == nil && nextActual <= opt.LastRow {
		for row := nextActual; row <= opt.LastRow; row++ {
			if row&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return result, err
				}
			}
			result.Scanned += cols
			result.Matched += int(cols)
			for col := opt.FirstCol; col <= opt.LastCol && len(result.Matches) < opt.Limit; col++ {
				add(SearchResult{Ref: fmt.Sprintf("%s%d", ColumnName(col), row), Row: row, Col: col})
			}
		}
	}
	if opt.Mode == "duplicates" {
		// First occurrences are discovered only when their second occurrence
		// appears. Present a source-coordinate order regardless of discovery.
		sort.Slice(result.Matches, func(i, j int) bool {
			a, b := result.Matches[i], result.Matches[j]
			if a.Row != b.Row {
				return a.Row < b.Row
			}
			return a.Col < b.Col
		})
	}
	result.Truncated = result.Matched > len(result.Matches)
	return result, nil
}
