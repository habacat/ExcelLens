package editfile

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type columnEdit struct {
	table  *xlsxTable
	index  int
	inputs []editdoc.Change
}

// PrepareChanges uses the same prospective column and the same rule checks
// for the grid, paste, fill and transaction confirmation.
func (l Layout) PrepareChanges(ctx context.Context, f *workbook.EditSourceFile, input []editdoc.Change, doc *editdoc.Document) (editdoc.PreparedChanges, error) {
	var plan editdoc.PreparedChanges
	changes := slices.Clone(input)
	columns := map[editdoc.Address]*columnEdit{}
	var order []editdoc.Address
	for i := range changes {
		c := &changes[i]
		if l.XLSX != nil && c.Value.Kind == "formula" {
			// Normalize Excel's formula-bar spelling before journaling. The
			// following validation still binds every name to its real scope.
			if f, err := calculator.ParseLocalFormula(ctx, strings.TrimPrefix(c.Value.Raw, "="), l.XLSX.HasSheet, func(string) (bool, bool) { return false, true }); err == nil {
				c.Value.Raw = f.ArrayStorageFormula()
			}
		}
		if l.XLSX == nil || len(l.XLSX.Tables) == 0 {
			continue
		}
		if c.Value.Kind == "formula" && strings.Contains(c.Value.Raw, "[") {
			parsed, err := l.XLSX.FormulaForFill(ctx, c.Value.Raw)
			if err != nil {
				return plan, err
			}
			c.Value.Raw = parsed.StoredTableFormula(func(raw string) string {
				if _, stored, ok := l.XLSX.tableReference(raw, c.Address); ok {
					return stored
				}
				return raw
			})
		}
		t, col := l.XLSX.tableAt(c.Address)
		if t == nil {
			continue
		}
		first, last := t.dataRows()
		if c.Row > last && c.Value.Kind != "" && c.Value.Kind != "formula" && c.Value.Kind != "blank" {
			// Excel serializes a directly entered totals value as a column label.
			raw := c.Value.Raw
			if c.Value.Kind == "number" {
				if err := validateXLSXNumber(raw); err != nil {
					return plan, err
				}
				n, _ := strconv.ParseFloat(raw, 64)
				raw = totalNumberLabel(n)
			}
			c.Value.Kind, c.Value.Raw = "text", raw
		}
		if c.Row < first || c.Row > last {
			continue
		}
		anchor := editdoc.Address{Sheet: t.Sheet, Row: first, Col: c.Col}
		if columns[anchor] == nil {
			columns[anchor] = &columnEdit{table: t, index: col}
			order = append(order, anchor)
		}
		columns[anchor].inputs = append(columns[anchor].inputs, *c)
	}
	for _, a := range order {
		col := columns[a]
		var err error
		changes, err = col.prepare(ctx, l, f, a, doc, changes, &plan)
		if err != nil {
			return plan, err
		}
	}
	addresses := make([]editdoc.Address, len(changes))
	for i, change := range changes {
		addresses[i] = change.Address
	}
	batch, err := l.ReadBatch(ctx, f, addresses, doc.RuleChanges())
	if err != nil {
		return plan, err
	}
	plan.Base, plan.Changes, err = batch.Validate(ctx, changes, doc)
	// Confirmation replays the preview. Its extent matters even when a member
	// already contains its proposed value (a whole column and a single input
	// have different effects on the template).
	if len(columns) != 0 && len(plan.Changes) != 0 {
		plan.Changes = changes
	}
	for _, t := range plan.Templates {
		if doc.ColumnFormula(t.Address, t.Original) != t.Formula {
			plan.Changes = changes
			break
		}
	}
	return plan, err
}

func (c columnEdit) prepare(ctx context.Context, l Layout, f *workbook.EditSourceFile, a editdoc.Address, doc *editdoc.Document, changes []editdoc.Change, plan *editdoc.PreparedChanges) ([]editdoc.Change, error) {
	first, last := c.table.dataRows()
	source := c.table.Columns[c.index]
	seed := c.inputs[0]
	expr, low, high, uniform, err := c.formulaSeries(ctx, l.XLSX)
	if err != nil {
		return nil, err
	}
	if !uniform {
		// Pasting an entire heterogeneous column removes the calculated
		// definition. A partial paste keeps it and writes cell exceptions.
		if len(c.inputs) == last-first+1 && !slices.ContainsFunc(c.inputs, func(input editdoc.Change) bool { return input.Value.Kind == "" }) {
			plan.Templates = append(plan.Templates, editdoc.ColumnFormula{Address: a, Original: source.Formula})
		}
		return changes, nil
	}
	if low > first || high < last {
		exception, err := l.hasColumnException(ctx, f, doc, a, low, high, source.Formula, last)
		if err != nil {
			return nil, err
		}
		if exception {
			return changes, nil
		}
	}
	if len(changes)+last-first+1-len(c.inputs) > formats.EditBatchCells {
		return nil, fmt.Errorf("此公式需要填充整列 %d 格，单次最多 %d 格", last-first+1, formats.EditBatchCells)
	}
	template, err := expr.Shift(first-seed.Row, 0)
	if err != nil {
		return nil, err
	}
	plan.Templates = append(plan.Templates, editdoc.ColumnFormula{Address: a, Original: source.Formula, Formula: template})
	for row := first; row <= last; row++ {
		if row >= low && row <= high {
			continue
		}
		raw, err := expr.Shift(row-seed.Row, 0)
		if err != nil {
			return nil, err
		}
		changes = append(changes, editdoc.Change{Address: editdoc.Address{Sheet: a.Sheet, Row: row, Col: a.Col}, Value: editdoc.Value{Kind: "formula", Raw: raw}})
	}
	return changes, nil
}

// A contiguous paste is one formula series if each member is the seed
// translated to its own row. Source order can differ from physical order.
func (c columnEdit) formulaSeries(ctx context.Context, l *XLSXLayout) (expr calculator.EditableFormula, low, high int, uniform bool, err error) {
	seed := c.inputs[0]
	low, high = seed.Row, seed.Row
	for _, input := range c.inputs {
		if input.Value.Kind != "formula" {
			return expr, 0, 0, false, nil
		}
		low, high = min(low, input.Row), max(high, input.Row)
	}
	if high-low+1 != len(c.inputs) {
		return expr, low, high, false, nil
	}
	expr, err = l.FormulaForFill(ctx, seed.Value.Raw)
	if err != nil {
		return expr, low, high, false, err
	}
	for _, input := range c.inputs[1:] {
		shifted, shiftErr := expr.Shift(input.Row-seed.Row, 0)
		if shiftErr != nil || shifted != input.Value.Raw {
			return expr, low, high, false, nil
		}
	}
	return expr, low, high, true, nil
}

func matchesColumnMember(template string, parsed calculator.EditableFormula, parseErr error, offset int, v editdoc.Value) bool {
	if template == "" {
		return v.Kind == "missing" || v.Kind == "blank"
	}
	if parseErr != nil || v.Kind != "formula" {
		return false
	}
	want, err := parsed.Shift(offset, 0)
	return err == nil && v.Raw == want
}

// The transaction limit applies to edits, not to the height of a table. A
// nonuniform million-row column may still accept a single-cell exception.
func (l Layout) hasColumnException(ctx context.Context, f *workbook.EditSourceFile, doc *editdoc.Document, anchor editdoc.Address, low, high int, base string, last int) (bool, error) {
	current := doc.ColumnFormula(anchor, base)
	var parsed calculator.EditableFormula
	var parseErr error
	if current != "" {
		parsed, parseErr = l.XLSX.FormulaForFill(ctx, current)
	}
	// Most ordinary data columns supply a conflicting value immediately.
	for row, chunk := anchor.Row, 32; row <= last; chunk = formats.EditBatchCells {
		end := min(last+1, row+chunk)
		positions := make([]editdoc.Address, 0, end-row)
		for ; row < end; row++ {
			if row < low || row > high {
				positions = append(positions, editdoc.Address{Sheet: anchor.Sheet, Row: row, Col: anchor.Col})
			}
		}
		if len(positions) == 0 {
			continue
		}
		cells, err := l.Read(ctx, f, positions)
		if err != nil {
			return false, err
		}
		for _, at := range positions {
			if !matchesColumnMember(current, parsed, parseErr, at.Row-anchor.Row, doc.ValueAt(at, cells[at].Value)) {
				return true, nil
			}
		}
	}
	return false, nil
}

// Excel converts a numeric totals label with General's fixed eleven-character
// budget (excluding the sign), independent of the column's visible width.
func totalNumberLabel(n float64) string {
	if n == 0 {
		return "0"
	}
	exponent := int(math.Floor(math.Log10(math.Abs(n))))
	trim := func(value string) string {
		if strings.Contains(value, ".") {
			return strings.TrimRight(strings.TrimRight(value, "0"), ".")
		}
		return value
	}
	if exponent >= -9 && exponent <= 10 {
		raw := strconv.FormatFloat(n, 'f', -1, 64)
		if len(strings.TrimPrefix(raw, "-")) <= 11 {
			return raw
		}
		if exponent >= -4 {
			return trim(strconv.FormatFloat(n, 'f', max(0, 9-max(exponent, 0)), 64))
		}
	}
	mantissa, power, _ := strings.Cut(strconv.FormatFloat(n, 'E', 5, 64), "E")
	return trim(mantissa) + "E" + power
}
