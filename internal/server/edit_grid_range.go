package server

import (
	"context"
	"encoding/json"
	"errors"

	"excellens/internal/calculator"
	"excellens/internal/checkpoint"
	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/formats"
)

// Only the existing input kind is needed to interpret pasted plain text.
// Returning the kinds in request order also works for cells outside reader tiles.
func editInputKinds(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, req editRequest) ([]string, error) {
	if req.Revision != doc.Revision {
		return nil, checkpoint.ErrConflict
	}
	if len(req.Cells) == 0 || len(req.Cells) > formats.EditBatchCells {
		return nil, errEditRequestCapacity
	}
	for _, addr := range req.Cells {
		if addr.Sheet != req.Sheet || !layout.Contains(addr) {
			return nil, editfile.ErrTarget
		}
	}
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cells, err := layout.Read(ctx, f, req.Cells)
	if err != nil {
		return nil, err
	}
	kinds := make([]string, len(req.Cells))
	for i, addr := range req.Cells {
		kinds[i] = doc.ValueAt(addr, cells[addr].Value).Kind
	}
	return kinds, f.Check()
}

// Excel fills each column from its top cell, or each row from its left
// cell. A one-cell selection takes its seed from the preceding row/column.
func (a *App) planGridFill(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, first editdoc.Address, req editRequest) (editBatchPreview, error) {
	last, err := editAddress(first.Sheet, req.End)
	var plan editBatchPreview
	if err != nil || !layout.Contains(first) || !layout.Contains(last) || first.Row > last.Row || first.Col > last.Col {
		return plan, editfile.ErrTarget
	}
	rows, cols := last.Row-first.Row+1, last.Col-first.Col+1
	if rows > formats.EditBatchCells/cols {
		return plan, errEditRequestCapacity
	}
	entry := req.Mode == "fill-entry"
	seed := first
	values := map[editdoc.Address]editdoc.Value{}
	var anchors []editdoc.Address
	if entry {
		inputs := req.Operation.Changes
		if len(inputs) != 1 {
			return plan, editdoc.ErrInvalid
		}
		input := inputs[0]
		if input.Sheet != first.Sheet || input.Row < first.Row || input.Row > last.Row || input.Col < first.Col || input.Col > last.Col || !input.Value.Valid() || input.Value.Style != "" {
			return plan, editdoc.ErrInvalid
		}
		seed = input.Address
		values[seed] = input.Value
	} else {
		if req.Mode == "fill-down" {
			if rows == 1 {
				seed.Row--
			}
			for col := first.Col; col <= last.Col; col++ {
				anchors = append(anchors, editdoc.Address{Sheet: first.Sheet, Row: seed.Row, Col: col})
			}
		} else {
			if cols == 1 {
				seed.Col--
			}
			for row := first.Row; row <= last.Row; row++ {
				anchors = append(anchors, editdoc.Address{Sheet: first.Sheet, Row: row, Col: seed.Col})
			}
		}
		if !layout.Contains(seed) {
			return plan, errors.New("填充起点前没有可复制的单元格")
		}
		values, err = a.gridFillSources(ctx, doc, layout, anchors)
		if err != nil {
			return plan, err
		}
	}
	plan.Last = last
	formulas := make(map[string]calculator.EditableFormula)
	size := 1024
	for row := first.Row; row <= last.Row; row++ {
		for col := first.Col; col <= last.Col; col++ {
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			addr := editdoc.Address{Sheet: first.Sheet, Row: row, Col: col}
			origin := seed
			switch req.Mode {
			case "fill-down":
				origin.Col = col
			case "fill-right":
				origin.Row = row
			}
			if !entry && addr == origin {
				continue
			}
			v := values[origin]
			if v.Kind == "formula" && addr != origin {
				if layout.XLSX == nil {
					return plan, editdoc.ErrInvalid
				}
				formula, ok := formulas[v.Raw]
				if !ok {
					formula, err = layout.XLSX.FormulaForFill(ctx, v.Raw)
					if err != nil {
						return plan, err
					}
					formulas[v.Raw] = formula
				}
				if v.Raw, err = formula.Shift(row-origin.Row, col-origin.Col); err != nil {
					return plan, err
				}
			}
			change := editdoc.Change{Address: addr, Value: v}
			encoded, _ := json.Marshal(change)
			size += len(encoded) + 1
			if size > formats.EditRequestBytes {
				return plan, errEditRequestCapacity
			}
			plan.Changes = append(plan.Changes, change)
			plan.LastValue = v.Raw
		}
	}
	return plan, nil
}

func (a *App) gridFillSources(ctx context.Context, doc *editdoc.Document, layout editfile.Layout, anchors []editdoc.Address) (map[editdoc.Address]editdoc.Value, error) {
	f, err := openEditBase(ctx, doc)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sources, err := layout.Read(ctx, f, anchors)
	if err != nil {
		return nil, err
	}
	calc, err := a.calculationFor(ctx, doc, layout, f)
	if err != nil {
		return nil, err
	}
	values := make(map[editdoc.Address]editdoc.Value, len(anchors))
	for _, addr := range anchors {
		source := sources[addr]
		v := doc.ValueAt(addr, source.Value)
		if layout.XLSX != nil {
			v.Style = source.FillStyle(v)
			if v.Style == "" {
				v.NumberStyle = ""
				v.BuiltinFormat = 0
			}
		}
		if derived, ok := calc.ArrayMembers[addr]; ok {
			v.Kind, v.Raw = derived.Kind, derived.Raw
		} else if calc.GeneratedStatus(addr) != "" && v.Kind != "formula" {
			return nil, errors.New("填充来源需要先重算或刷新")
		}
		if v.Kind == "missing" {
			v.Kind = "blank"
		}
		values[addr] = v
	}
	return values, f.Check()
}
