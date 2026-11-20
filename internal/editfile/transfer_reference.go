package editfile

import (
	"strconv"
	"strings"
	"unicode"

	"excellens/internal/calculator"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type rangeTransfer struct {
	from, to       string
	source, target cellRect
	cut            bool
}

func (r cellRect) offset(dr, dc int) cellRect {
	return cellRect{r.firstRow + dr, r.firstCol + dc, r.lastRow + dr, r.lastCol + dc}
}

func (r cellRect) intersection(b cellRect) cellRect {
	return cellRect{max(r.firstRow, b.firstRow), max(r.firstCol, b.firstCol), min(r.lastRow, b.lastRow), min(r.lastCol, b.lastCol)}
}

func (r cellRect) hull(b cellRect) cellRect {
	return cellRect{min(r.firstRow, b.firstRow), min(r.firstCol, b.firstCol), max(r.lastRow, b.lastRow), max(r.lastCol, b.lastCol)}
}

type refSpan struct{ lo, hi int }

func (r cellRect) rowsSpan() refSpan    { return refSpan{r.firstRow, r.lastRow} }
func (r cellRect) colsSpan() refSpan    { return refSpan{r.firstCol, r.lastCol} }
func (s refSpan) inside(b refSpan) bool { return s.lo >= b.lo && s.hi <= b.hi }
func (s refSpan) has(v int) bool        { return v >= s.lo && v <= s.hi }

// A rectangular reference can lose a complete strip at an edge. Excel
// retains the address when the remainder has a hole or an L-shaped corner.
func (s refSpan) trim(b refSpan, overwritten bool) refSpan {
	if overwritten && b.inside(s) {
		return s
	}
	switch {
	case s.lo < b.lo && b.has(s.hi):
		return refSpan{s.lo, b.lo - 1}
	case s.hi > b.hi && b.has(s.lo):
		return refSpan{b.hi + 1, s.hi}
	}
	return s
}

func (r cellRect) without(b cellRect, overwritten bool) cellRect {
	if b.covers(r) {
		return cellRect{}
	}
	if r.rowsSpan().inside(b.rowsSpan()) {
		s := r.colsSpan().trim(b.colsSpan(), overwritten)
		r.firstCol, r.lastCol = s.lo, s.hi
	} else if r.colsSpan().inside(b.colsSpan()) {
		s := r.rowsSpan().trim(b.rowsSpan(), overwritten)
		r.firstRow, r.lastRow = s.lo, s.hi
	}
	return r
}

// A partial reference in a single lane can keep its stationary end. If
// that end falls between two separated blocks, Excel retains the old range.
func (s refSpan) crossesGap(from, to refSpan) bool {
	return to.lo > from.lo && s.lo >= from.lo && s.hi < to.lo || to.lo < from.lo && s.hi <= from.hi && s.lo > to.hi
}
func (s refSpan) reaches(from, to refSpan) bool {
	return to.has(s.hi) && s.lo >= from.lo || to.has(s.lo) && s.hi <= from.hi
}

func (m rangeTransfer) localReference(r cellRect) cellRect {
	from, to := m.source, m.target
	dr, dc := to.firstRow-from.firstRow, to.firstCol-from.firstCol
	part := r.intersection(from).offset(dr, dc)
	rest := r.without(from, false)
	rows, cols := r.rowsSpan(), r.colsSpan()
	if rest != r {
		if dr == 0 && rest.rowsSpan() == rows || dc == 0 && rest.colsSpan() == cols {
			if cols.crossesGap(from.colsSpan(), to.colsSpan()) || rows.crossesGap(from.rowsSpan(), to.rowsSpan()) {
				return r
			}
			return part.hull(rest)
		}
		inCol := cols.inside(from.colsSpan()) && cols.inside(to.colsSpan())
		inRow := rows.inside(from.rowsSpan()) && rows.inside(to.rowsSpan())
		if to.covers(rest) || inCol && rows.reaches(from.rowsSpan(), to.rowsSpan()) || inRow && cols.reaches(from.colsSpan(), to.colsSpan()) {
			return part
		}
	}
	if to.covers(r) {
		return cellRect{}
	}
	corners := [4][2]int{{r.firstRow, r.firstCol}, {r.lastRow, r.lastCol}, {r.firstRow, r.lastCol}, {r.lastRow, r.firstCol}}
	sourceCorner, crosses := false, false
	for i, p := range corners {
		if from.contains(p[0], p[1]) {
			sourceCorner = true
			q := corners[i^1]
			crosses = crosses || to.contains(q[0], q[1])
		}
	}
	if !sourceCorner {
		return r.without(to, true)
	}
	// A diagonal move can cover both ends along different axes. Preserve
	// the stationary coordinate of an address endpoint outside the move.
	rowBand, colBand := rows.inside(to.rowsSpan()), cols.inside(to.colsSpan())
	if dr == 0 || dc == 0 || !crosses || rows.inside(from.rowsSpan()) || cols.inside(from.colsSpan()) || !rowBand && !colBand {
		return r
	}
	movingEndpoint := from.contains(r.firstRow, r.firstCol) || from.contains(r.lastRow, r.lastCol)
	rows = rows.diagonal(from.rowsSpan(), to.rowsSpan(), part.rowsSpan(), movingEndpoint)
	cols = cols.diagonal(from.colsSpan(), to.colsSpan(), part.colsSpan(), movingEndpoint)
	return cellRect{rows.lo, cols.lo, rows.hi, cols.hi}
}

// For a diagonal move with opposite corners in different blocks, an axis
// inside the destination follows its individual source endpoints. The other
// axis follows the moved intersection if an A1 endpoint moved, or keeps the
// part of its original interval that the destination did not overwrite.
func (s refSpan) diagonal(from, to, moved refSpan, movingEndpoint bool) refSpan {
	if s.inside(to) {
		lo, hi := s.lo, s.hi
		if from.has(lo) {
			lo += to.lo - from.lo
		}
		if from.has(hi) {
			hi += to.lo - from.lo
		}
		return refSpan{min(lo, hi), max(lo, hi)}
	}
	if movingEndpoint {
		return moved
	}
	return s.trim(to, true)
}

func (m rangeTransfer) reference(sheet string, r cellRect) (string, cellRect) {
	if strings.EqualFold(sheet, m.from) && r.overlaps(m.source) {
		if m.source.covers(r) {
			return m.to, r.offset(m.target.firstRow-m.source.firstRow, m.target.firstCol-m.source.firstCol)
		}
		if m.from != m.to {
			return sheet, r.without(m.source, false)
		}
		return sheet, m.localReference(r)
	}
	if strings.EqualFold(sheet, m.to) {
		return sheet, r.without(m.target, true)
	}
	return sheet, r
}

func sheetQualifier(name string) string {
	for _, c := range name {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '.' {
			return "'" + strings.ReplaceAll(name, "'", "''") + "'!"
		}
	}
	// Excel also quotes names which could themselves be cell addresses.
	if _, _, err := workbook.ParseCellRef(strings.ToUpper(name)); err == nil || name == "" || name[0] >= '0' && name[0] <= '9' {
		return "'" + name + "'!"
	}
	return name + "!"
}

func (a addressAxis) at(row, col int) string {
	v := ""
	if a.col > 0 {
		if strings.HasPrefix(a.letters, "$") {
			v = "$"
		}
		v += workbook.ColumnName(col)
	}
	if a.row > 0 {
		if strings.HasPrefix(a.number, "$") {
			v += "$"
		}
		v += strconv.Itoa(row)
	}
	return v
}

func (m rangeTransfer) formula(body, oldScope, newScope string) (string, error) {
	return calculator.RewriteReferences(body, func(label, q, raw string) (string, error) {
		if raw == "" {
			return q, nil
		}
		sheet := label
		if sheet == "" {
			sheet = oldScope
		}
		first, last, pair := strings.Cut(raw, ":")
		if !pair {
			last = first
		}
		a, b := axisAddress(first), axisAddress(last)
		r := cellRect{min(a.row, b.row), min(a.col, b.col), max(a.row, b.row), max(a.col, b.col)}
		if a.row == 0 {
			r.firstRow, r.lastRow = 1, formats.ExcelRows
		}
		if a.col == 0 {
			r.firstCol, r.lastCol = 1, formats.ExcelColumns
		}
		nextSheet, next := m.reference(sheet, r)
		if !strings.EqualFold(sheet, nextSheet) || q == "" && !strings.EqualFold(oldScope, newScope) {
			if q != "" || !strings.EqualFold(nextSheet, newScope) {
				q = sheetQualifier(nextSheet)
			}
		}
		if next == r {
			return q + raw, nil
		}
		if next == (cellRect{}) {
			return q + "#REF!", nil
		}
		top, bottom, left, right := next.firstRow, next.lastRow, next.firstCol, next.lastCol
		if a.row > b.row {
			top, bottom = bottom, top
		}
		if a.col > b.col {
			left, right = right, left
		}
		ref := a.at(top, left)
		if pair {
			ref += ":" + b.at(bottom, right)
		}
		return q + ref, nil
	})
}
