package editfile

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

type PivotChoices struct {
	Items []editdoc.PivotItem `json:"items"`
	More  bool                `json:"more"`
}

// PivotItemList belongs to one immutable edit revision and one source column.
// The first page reads only enough rows to answer it. Later pages reuse the
// distinct values already visited. Changing the search reuses the same values.
type PivotItemList struct {
	group                 *dateBuckets
	view                  objectValues
	sheet                 string
	col, nextRow, lastRow int
	items                 []pivotValue
	seen                  map[pivotValue]bool
	query                 string
	checked               int
	matches               []int
}

func (b *CalculationBook) NewPivotItemList(calc *Calculation, patches map[editdoc.Address]editdoc.Value, info PivotInfo, field string) (*PivotItemList, error) {
	col := slices.Index(info.Fields, field)
	var group *dateBuckets
	if g, found := info.GroupFields[field]; found {
		col = slices.Index(info.Fields, g.Source)
		b, err := (pivotDateLevel{rule: g.rule()}).buckets(g.Start, g.End, g.English)
		if err != nil {
			return nil, err
		}
		group = &b
	}
	area, ok := parseRect(info.SourceRef)
	if !ok || col < 0 || col > area.lastCol-area.firstCol {
		return nil, ErrTarget
	}
	if info.RefreshReason != "" {
		return nil, errors.New(info.RefreshReason)
	}
	view := objectValues{book: b, calculation: calc, overlay: patches}
	area = effectivePivotSource(view, info.SourceSheet, area)
	if area.lastRow-area.firstRow > formats.EditCalcSourceCells {
		return nil, ErrCalcCapacity
	}
	return &PivotItemList{group: group, view: view, sheet: info.SourceSheet, col: area.firstCol + col,
		nextRow: area.firstRow + 1, lastRow: area.lastRow, seen: map[pivotValue]bool{}}, nil
}

func (v pivotValue) matchesSearch(query string) bool {
	if query == "" || strings.Contains(strings.ToLower(v.raw), query) {
		return true
	}
	switch v.kind {
	case "b":
		return strings.Contains(strconv.FormatBool(v.raw == "1"), query)
	case "m":
		return strings.Contains("(空白)", query)
	case "d":
		return strings.Contains(strings.ToLower(strings.Replace(v.raw, "T", " ", 1)), query)
	}
	return false
}

// The owner serializes Page calls. A canceled or failed source read leaves
// nextRow at the first unread record, so a retry cannot skip a candidate.
func (p *PivotItemList) Page(ctx context.Context, query string, offset int) (PivotChoices, error) {
	out := PivotChoices{Items: []editdoc.PivotItem{}}
	if offset < 0 {
		return out, ErrTarget
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	query = strings.ToLower(query)
	if query != p.query {
		p.query, p.checked, p.matches = query, 0, p.matches[:0]
	}
	for len(p.matches)-offset <= formats.EditPivotChoicesPage {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if p.checked < len(p.items) {
			if p.items[p.checked].matchesSearch(query) {
				p.matches = append(p.matches, p.checked)
			}
			p.checked++
			continue
		}
		if p.nextRow > p.lastRow {
			break
		}
		cell, err := p.view.cell(editdoc.Address{Sheet: p.sheet, Row: p.nextRow, Col: p.col})
		if err != nil {
			return out, err
		}
		item, err := p.view.book.pivotCell(cell)
		if err != nil {
			return out, err
		}
		if b := p.group; b != nil {
			i := len(b.values) - 1
			if item.kind == "d" {
				t, _ := editdoc.ISODateTime(item.raw)
				i = b.index(t)
			}
			item = b.values[i]
		}
		if !p.seen[item] {
			p.seen[item] = true
			p.items = append(p.items, item)
		}
		p.nextRow++
	}
	start := min(offset, len(p.matches))
	end := min(start+formats.EditPivotChoicesPage, len(p.matches))
	out.Items = make([]editdoc.PivotItem, 0, end-start)
	for _, index := range p.matches[start:end] {
		out.Items = append(out.Items, p.items[index].choice())
	}
	out.More = end < len(p.matches)
	return out, nil
}

func effectivePivotSource(v objectValues, sheet string, r cellRect) cellRect {
	// Excel includes one empty record below the used range for a full-height source.
	if r.lastRow != formats.ExcelRows {
		return r
	}
	last := r.firstRow
	for _, s := range v.book.source.layout.Sheets {
		if s.Name == sheet {
			last = max(last, s.Rows)
			break
		}
	}
	for a := range v.overlay {
		if a.Sheet == sheet {
			last = max(last, a.Row)
		}
	}
	r.lastRow = min(r.lastRow, last+1)
	return r
}
