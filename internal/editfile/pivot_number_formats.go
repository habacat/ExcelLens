package editfile

import (
	"context"
	"strconv"

	"excellens/internal/editdoc"
)

type pivotNumberFormats struct {
	byMeasure []int
	output    map[editdoc.Address]int
	previous  map[int]bool
}

func measureFormats(previous, current []pivotDataField) (pivotNumberFormats, error) {
	var f pivotNumberFormats
	for _, m := range previous {
		if m.Number == "" {
			continue
		}
		n, err := strconv.Atoi(m.Number)
		if err != nil || n < 0 {
			return f, ErrXLSX
		}
		if f.previous == nil {
			f.previous = make(map[int]bool)
		}
		f.previous[n] = true
	}
	for i, m := range current {
		if m.Number == "" {
			continue
		}
		n, err := strconv.Atoi(m.Number)
		if err != nil || n < 0 {
			return f, ErrXLSX
		}
		if f.output == nil {
			f.output = make(map[editdoc.Address]int)
			f.byMeasure = make([]int, len(current))
			for j := range f.byMeasure {
				f.byMeasure[j] = -1
			}
		}
		f.byMeasure[i] = n
	}
	return f, nil
}

// Apply each measure's number format to its intersections (including blanks)
// and release formats left behind when the value axis moves or shrinks.
func (p *xlsxPackage) applyPivotFormats(ctx context.Context, sheet string, plans []pivotPlan, t *sheetTargets, edits map[editdoc.Address]editdoc.Value, updates map[string][]byte) error {
	var styles entryFormatTable
	requests := map[editdoc.Address]xfNumber{}
	ids := map[xfNumber]string{}
	for _, plan := range plans {
		if sheet != plan.sheet || plan.formats.output == nil && plan.formats.previous == nil {
			continue
		}
		if styles.data == nil {
			raw := updates["xl/styles.xml"]
			var err error
			if raw == nil {
				styles, err = entryFormats(ctx, p)
			} else {
				styles, err = parseEntryFormats(ctx, raw)
			}
			if err != nil {
				return err
			}
			if len(styles.xfs) == 0 {
				// General is the implicit format in a workbook without a style part.
				for _, number := range plan.formats.byMeasure {
					if number > 0 {
						return ErrXLSX
					}
				}
				continue
			}
		}
		old := plan.old
		old.firstRow += plan.table.Area.Header
		old.firstCol += plan.table.Area.Col
		for _, row := range t.rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			for _, cell := range row.cells {
				a := cell.address
				base := cell.original.value.Style
				if base == "" {
					base = "0"
				}
				before := styles.number(base)
				wanted, explicit := plan.formats.output[a]
				if !explicit && !(old.contains(a.Row, a.Col) && plan.formats.previous[before] && edits[a].Style == "") {
					continue
				}
				if before == wanted {
					continue
				}
				key := xfNumber{base, wanted}
				requests[a], ids[key] = key, ""
			}
		}
	}
	patches, err := p.composeNumberFormats(ctx, styles, ids)
	if err != nil {
		return err
	}
	if len(patches) > 0 {
		updates["xl/styles.xml"], err = applyXML(ctx, styles.data, patches)
		if err != nil {
			return err
		}
	}
	for a, key := range requests {
		value := edits[a]
		value.Style = ids[key]
		edits[a] = value
	}
	return nil
}
