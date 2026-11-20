package server

import (
	"context"
	"encoding/json"
	"math"
	"strconv"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
)

// Excel extends each column or row independently. Numeric seeds use a
// linear trend; ordinary values repeat and formulas travel with their source.
type fillTrend struct {
	start, step float64
	series      bool
}

func numericTrend(values []editdoc.Value, singleStep bool) fillTrend {
	var sum, weighted float64
	n := float64(len(values))
	mid := (n - 1) / 2
	for i, v := range values {
		if v.Kind != "number" {
			return fillTrend{}
		}
		x, err := strconv.ParseFloat(v.Raw, 64)
		if err != nil || math.IsInf(x, 0) || math.IsNaN(x) {
			return fillTrend{}
		}
		sum += x
		weighted += (float64(i) - mid) * x
	}
	if len(values) == 1 {
		return fillTrend{sum, 1, singleStep}
	}
	slope := weighted / (n * (n*n - 1) / 12)
	return fillTrend{sum/n - mid*slope, slope, true}
}

func (a *App) planAutoFill(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, first editdoc.Address, req editRequest) (editBatchPreview, error) {
	var plan editBatchPreview
	last, err := editAddress(first.Sheet, req.End)
	if err != nil {
		return plan, err
	}
	to, err := editAddress(first.Sheet, req.Destination)
	if err != nil {
		return plan, err
	}
	rows, cols := last.Row-first.Row+1, last.Col-first.Col+1
	if !layout.Contains(first) || !layout.Contains(last) || !layout.Contains(to) || rows < 1 || cols < 1 || rows > formats.EditBatchCells/cols {
		return plan, editfile.ErrTarget
	}
	vertical := to.Row < first.Row || to.Row > last.Row
	horizontal := to.Col < first.Col || to.Col > last.Col
	if vertical == horizontal {
		return plan, editfile.ErrTarget
	}
	length, lanes, endpoint := rows, cols, to.Row-first.Row
	point := func(index, lane int) editdoc.Address {
		addr := first
		if vertical {
			addr.Row += index
			addr.Col += lane
		} else {
			addr.Col += index
			addr.Row += lane
		}
		return addr
	}
	if horizontal {
		length, lanes, endpoint = cols, rows, to.Col-first.Col
	}
	lo, hi := length, endpoint
	if endpoint < 0 {
		lo, hi = endpoint, -1
	}
	if hi-lo+1 > formats.EditBatchCells/lanes {
		return plan, errEditRequestCapacity
	}
	sources := make([]editdoc.Address, 0, rows*cols)
	for lane := 0; lane < lanes; lane++ {
		for i := 0; i < length; i++ {
			sources = append(sources, point(i, lane))
		}
	}
	vals, err := a.gridFillSources(ctx, doc, layout, sources)
	if err != nil {
		return plan, err
	}
	formulas := map[string]calculator.EditableFormula{}
	var increment func(editdoc.Value) float64
	if length == 1 && layout.XLSX != nil {
		file, err := openEditBase(ctx, doc)
		if err != nil {
			return plan, err
		}
		increment, err = layout.AutoFillIncrements(ctx, file)
		file.Close()
		if err != nil {
			return plan, err
		}
	}
	size := 1024
	for lane := 0; lane < lanes; lane++ {
		line := make([]editdoc.Value, length)
		for i := range line {
			line[i] = vals[point(i, lane)]
		}
		trend := numericTrend(line, req.Alternate)
		var texts []fillTextSeed
		if !req.Alternate {
			texts = textFillSeeds(line)
		}
		if length == 1 && line[0].Kind == "number" && increment != nil {
			step := increment(line[0])
			trend.series = (step != 0) != req.Alternate
			if step != 0 {
				trend.step = step
			}
		} else if length > 1 && req.Alternate {
			trend.series = false
		}
		for i := lo; i <= hi; i++ {
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			seed := (i%length + length) % length
			v := line[seed]
			dst, origin := point(i, lane), point(seed, lane)
			if trend.series {
				result := trend.start + float64(i)*trend.step
				if math.IsInf(result, 0) || math.IsNaN(result) {
					return plan, editfile.ErrTarget
				}
				v.Raw = strconv.FormatFloat(result, 'g', 15, 64)
			} else if len(texts) > 0 && v.Kind == "text" {
				if result, ok := texts[seed].extend((i - seed) / length); ok {
					v.Raw = result
				}
			} else if v.Kind == "formula" {
				f, ok := formulas[v.Raw]
				if !ok {
					f, err = layout.XLSX.FormulaForFill(ctx, v.Raw)
					if err != nil {
						return plan, err
					}
					formulas[v.Raw] = f
				}
				v.Raw, err = f.Shift(dst.Row-origin.Row, dst.Col-origin.Col)
				if err != nil {
					return plan, err
				}
			}
			change := editdoc.Change{Address: dst, Value: v}
			b, _ := json.Marshal(change)
			size += len(b) + 1
			if size > formats.EditRequestBytes {
				return plan, errEditRequestCapacity
			}
			plan.Changes = append(plan.Changes, change)
			plan.Last, plan.LastValue = dst, v.Raw
		}
	}
	return plan, nil
}
