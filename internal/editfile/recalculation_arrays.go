package editfile

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// A result owns its whole rectangle, including repeated legacy CSE values.
// A blocked dynamic formula owns just its anchor.
type arrayResult struct {
	area    cellRect
	values  []editdoc.Value
	dynamic bool
	failure richError
}

type arrayPass struct {
	specs    map[editdoc.Address]sourceFormulaTag
	owners   map[editdoc.Address]editdoc.Address
	released map[editdoc.Address]bool
}

func anchorRect(a editdoc.Address) cellRect { return cellRect{a.Row, a.Col, a.Row, a.Col} }

func (b *CalculationBook) Calculate(ctx context.Context, patches map[editdoc.Address]editdoc.Value, previous *Calculation) (*Calculation, error) {
	return b.calculateArrays(ctx, patches, previous, nil)
}

func (b *CalculationBook) calculateArrays(ctx context.Context, patches map[editdoc.Address]editdoc.Value, previous *Calculation, explicit map[editdoc.Address]sourceFormulaTag) (*Calculation, error) {
	specs := maps.Clone(b.arrays)
	if specs == nil {
		specs = make(map[editdoc.Address]sourceFormulaTag)
	}
	valueEdit := false
	formulaBytes, edges := 0, 0
	programs := make(map[editdoc.Address]calculator.WorksheetFormula)
	for addr, v := range patches {
		valueEdit = valueEdit || v.Kind != ""
		if v.Kind == "" {
			continue
		}
		cell := b.cells[addr]
		if v.Kind != "formula" {
			delete(specs, addr)
			continue
		}
		formulaBytes += len(v.Raw)
		if formulaBytes > formats.EditCalcFormulaBytes {
			return nil, ErrCalcCapacity
		}
		tag := cell.tag
		if requested, ok := explicit[addr]; ok {
			tag = requested
		}
		var program calculator.WorksheetFormula
		reused := false
		if previous != nil && explicit == nil && previous.patches[addr].Raw == v.Raw {
			program, reused = previous.programs[addr]
		}
		if !reused {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			f, err := b.source.parseFormula(ctx, strings.TrimPrefix(v.Raw, "="), addr, nil)
			if err == nil {
				program = b.bind(f, addr, tag.kind == "array" || !f.Scalar)
			}
		}
		programs[addr] = program
		edges += len(program.References)
		if edges > formats.EditCalcReferences {
			return nil, ErrCalcCapacity
		}
		if tag.kind == "array" && !tag.dynamic {
			specs[addr] = tag
		} else if program.MaySpill && program.Bound() {
			if !tag.dynamic {
				tag = sourceFormulaTag{kind: "array", dynamic: true, area: anchorRect(addr)}
			}
			specs[addr] = tag
		}
	}
	maps.Copy(specs, explicit)
	if len(specs) == 0 || !valueEdit && len(b.missing) == 0 && len(explicit) == 0 {
		return b.calculatePass(ctx, patches, programs, previous, nil)
	}

	// Resolve against an immutable ownership map for each pass. Re-evaluate
	// when a spill moves so even precedents reading previously empty cells
	// (or a full-column SUM) bind to their new producer.
	shapes := make(map[editdoc.Address]cellRect, len(specs))
	oldMembers := map[editdoc.Address]bool{}
	for addr, tag := range specs {
		shapes[addr] = tag.area
		if old := b.cells[addr].tag; old.kind == "array" {
			cells, err := old.area.addresses(addr.Sheet)
			if err != nil {
				return nil, ErrCalcCapacity
			}
			for _, cell := range cells {
				oldMembers[cell] = true
			}
		}
	}
	used := 0
	for pass := 0; pass <= len(specs)+2; pass++ {
		run := &arrayPass{specs: specs, owners: map[editdoc.Address]editdoc.Address{}, released: maps.Clone(oldMembers)}
		for _, addr := range slices.SortedFunc(maps.Keys(shapes), compareCalcAddress) {
			cells, err := shapes[addr].addresses(addr.Sheet)
			if err != nil || len(run.owners)+len(cells) > formats.EditCalcSourceCells {
				return nil, ErrCalcCapacity
			}
			for _, member := range cells {
				if occupied, exists := run.owners[member]; exists && occupied != addr {
					return nil, fmt.Errorf("数组结果交叠于 %s!%s", member.Sheet, addressRef(member))
				}
				run.owners[member] = addr
				delete(run.released, member)
			}
		}
		calc, err := b.calculatePass(ctx, patches, programs, nil, run)
		if err != nil {
			return nil, err
		}
		used += calc.Visits
		if used > formats.EditCalcVisits {
			return nil, ErrCalcCapacity
		}
		stable := true
		for addr, result := range calc.Arrays {
			if shapes[addr] != result.area {
				shapes[addr] = result.area
				stable = false
			}
		}
		if stable {
			calc.ArrayMembers = make(map[editdoc.Address]editdoc.Value)
			for a := range run.released {
				if patches[a].Kind == "" && b.cells[a].value.Kind != "" {
					calc.ArrayMembers[a] = editdoc.Value{Kind: "blank", Style: b.cells[a].value.Style}
				}
			}
			for anchor, result := range calc.Arrays {
				width := result.area.lastCol - anchor.Col + 1
				for i, v := range result.values {
					if i == 0 {
						continue
					}
					a := editdoc.Address{Sheet: anchor.Sheet, Row: anchor.Row + i/width, Col: anchor.Col + i%width}
					v.Style = b.cells[a].value.Style
					if v != b.cells[a].value {
						calc.ArrayMembers[a] = v
					}
				}
			}
			calc.Visits = used
			return calc, nil
		}
	}
	return nil, errors.New("数组间的溢出范围无法稳定")
}

func (p *arrayPass) seed(s *calcRevision, roots []editdoc.Address) []editdoc.Address {
	s.result.Arrays = map[editdoc.Address]arrayResult{}
	for cell := range p.owners {
		roots = append(roots, cell)
		if _, exists := s.book.cells[cell]; !exists {
			k := calcColumn{cell.Sheet, cell.Col}
			s.added[k] = append(s.added[k], cell.Row)
		}
	}
	for cell := range p.released {
		roots = append(roots, cell)
	}
	return roots
}

func (s *calcRevision) arrayMember(a editdoc.Address) (calculator.FormulaValue, bool, error) {
	anchor, owned := s.arrays.owners[a]
	if !owned {
		if s.arrays.released[a] && s.patches[a].Kind == "" {
			return calculator.BlankValue(), true, nil
		}
		return calculator.FormulaValue{}, false, nil
	}
	if a == anchor {
		return calculator.FormulaValue{}, false, nil
	}
	if _, err := s.value(anchor); err != nil {
		return calculator.FormulaValue{}, true, err
	}
	output, ok := s.result.Arrays[anchor]
	if !ok {
		return calculator.FormulaValue{}, true, calculator.ErrWorksheetPending
	}
	if !output.area.contains(a.Row, a.Col) {
		return calculator.BlankValue(), true, nil
	}
	width := output.area.lastCol - anchor.Col + 1
	v, err := calcOperand(output.values[(a.Row-anchor.Row)*width+a.Col-anchor.Col], s.book.date1904)
	return v, true, err
}

func (s *calcRevision) evalArray(anchor editdoc.Address, f calculator.WorksheetFormula, resolve calculator.FormulaResolver) (calculator.FormulaValue, error) {
	data, rows, cols, err := f.EvaluateArray(s.ctx, resolve, formats.EditBatchCells)
	if err != nil {
		return calculator.FormulaValue{}, err
	}
	spec := s.arrays.specs[anchor]
	out := arrayResult{area: spec.area, dynamic: spec.dynamic}
	if spec.dynamic {
		out.area = cellRect{anchor.Row, anchor.Col, anchor.Row + rows - 1, anchor.Col + cols - 1}
		if subtype := s.spillBlocker(anchor, out.area); subtype != 0 {
			out.failure = richError{kind: 8, subtype: subtype}
			if subtype != 3 {
				out.failure.row = rows - 1
				out.failure.col = cols - 1
			}
			out.area = anchorRect(anchor)
			v := calculator.FormulaValue{Kind: "error", Text: "#SPILL!"}
			out.values = []editdoc.Value{{Kind: "error", Raw: v.Text}}
			s.result.Arrays[anchor] = out
			return v, nil
		}
	}
	cells, err := out.area.addresses(anchor.Sheet)
	if err != nil {
		return calculator.FormulaValue{}, err
	}
	out.values = make([]editdoc.Value, len(cells))
	for i, a := range cells {
		r, c := a.Row-anchor.Row, a.Col-anchor.Col
		if rows == 1 {
			r = 0
		}
		if cols == 1 {
			c = 0
		}
		v := calculator.FormulaValue{Kind: "error", Text: "#N/A"}
		if r < rows && c < cols {
			v = data[r*cols+c]
		}
		if v.Kind == "blank" {
			out.values[i] = editdoc.Value{Kind: "number", Raw: "0"}
		} else {
			out.values[i], err = calcOutput(v)
			if err != nil {
				return calculator.FormulaValue{}, err
			}
		}
		s.resultBytes += len(out.values[i].Raw) + 64
		if s.resultBytes > formats.EditCalcProjectionBytes {
			return calculator.FormulaValue{}, ErrCalcCapacity
		}
	}
	s.result.Arrays[anchor] = out
	return calcOperand(out.values[0], s.book.date1904)
}

func (s *calcRevision) spillBlocker(anchor editdoc.Address, target cellRect) int {
	if target.lastRow > formats.ExcelRows || target.lastCol > formats.ExcelColumns {
		return 3
	}
	for _, r := range s.book.merges[anchor.Sheet] {
		if target.overlaps(r) {
			return 6
		}
	}
	for _, r := range s.book.barriers[anchor.Sheet] {
		if target.overlaps(r) {
			return 1
		}
	}
	for row := target.firstRow; row <= target.lastRow; row++ {
		for col := target.firstCol; col <= target.lastCol; col++ {
			a := editdoc.Address{Sheet: anchor.Sheet, Row: row, Col: col}
			if a == anchor {
				continue
			}
			if owner, exists := s.arrays.owners[a]; exists {
				if owner != anchor {
					return 1
				}
				// A newly reserved cell must still be checked against its source.
			}
			old := s.book.cells[anchor].tag
			if old.kind == "array" && old.area.contains(row, col) && s.patches[a].Kind == "" || s.arrays.released[a] && s.patches[a].Kind == "" {
				continue
			}
			switch s.current(a).Kind {
			case "", "missing", "blank":
			default:
				return 1
			}
		}
	}
	return 0
}
