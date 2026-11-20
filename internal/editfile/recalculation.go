package editfile

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type calcCell struct {
	value editdoc.Value
	cache *editdoc.Value
	tag   sourceFormulaTag
}

type calcColumn struct {
	sheet string
	col   int
}

type calcBand struct {
	calcColumn
	block int
}

type calcOwner struct {
	input  calculator.WorksheetReference
	output GeneratedArea
}

type calcDependencies struct {
	exact  map[editdoc.Address][]editdoc.Address
	ranges map[calculator.WorksheetReference][]editdoc.Address
	bands  map[calcBand][]calculator.WorksheetReference
}

func newCalcDependencies() calcDependencies {
	return calcDependencies{
		exact:  make(map[editdoc.Address][]editdoc.Address),
		ranges: make(map[calculator.WorksheetReference][]editdoc.Address),
		bands:  make(map[calcBand][]calculator.WorksheetReference),
	}
}

func (d calcDependencies) add(a editdoc.Address, r calculator.WorksheetReference) {
	if r.Top == r.Bottom && r.Left == r.Right {
		addr := editdoc.Address{Sheet: r.Sheet, Row: r.Top, Col: r.Left}
		d.exact[addr] = append(d.exact[addr], a)
		return
	}
	if _, registered := d.ranges[r]; !registered {
		first, last := r.Left, r.Right
		if last-first > 15 {
			first, last = 0, 0
		}
		top, bottom := (r.Top-1)/128, (r.Bottom-1)/128
		if bottom-top > 1 {
			top, bottom = -1, -1
		}
		for col := first; col <= last; col++ {
			for block := top; block <= bottom; block++ {
				key := calcBand{calcColumn{r.Sheet, col}, block}
				d.bands[key] = append(d.bands[key], r)
			}
		}
	}
	d.ranges[r] = append(d.ranges[r], a)
}

// CalculationBook is built only for explicit editing, once for an immutable
// source. It holds sparse values, translated formulas and their reverse edges.
type CalculationBook struct {
	source           xlsxPackage
	cells            map[editdoc.Address]calcCell
	columns          map[calcColumn][]int
	formulas         map[editdoc.Address]calculator.WorksheetFormula
	missing          []editdoc.Address
	unbound          []editdoc.Address
	deps             calcDependencies
	owners           []calcOwner
	arrays           map[editdoc.Address]sourceFormulaTag
	merges           map[string][]cellRect
	barriers         map[string][]cellRect
	hidden           map[string]map[int]bool
	filtered         map[string][]cellRect
	date1904         bool
	pivotDateFormats map[string]workbook.CalendarNumberFormat
}

var ErrCalcCapacity = errors.New("工作簿超过即时重算容量")
var ErrCalcSettings = errors.New("工作簿的精度或日期设置需要在 Excel 中重算")

func NewCalculationBook(ctx context.Context, f *workbook.EditSourceFile, layout XLSXLayout) (*CalculationBook, error) {
	p, err := openXLSXPackage(f, f.Size())
	if err != nil {
		return nil, err
	}
	if layout.Native {
		if err = p.checkCalculationSize(); err != nil {
			return nil, err
		}
		p.layout.Native, p.layout.Structured = true, true
	}
	if err = p.loadWorkbook(ctx); err != nil {
		return nil, err
	}
	p.layout = layout
	if layout.Native {
		if err = p.inspectOfficeParts(ctx); err != nil {
			return nil, err
		}
	} else if layout.Structured {
		if err = p.inspectLinkedParts(ctx, true); err != nil {
			return nil, err
		}
	}
	b, err := p.buildCalculationBook(ctx)
	if err != nil {
		return nil, err
	}
	return b, f.Check()
}

func (p *xlsxPackage) checkCalculationSize() error {
	var expanded uint64
	for _, entry := range p.zip.File {
		size := entry.UncompressedSize64
		if raw, ok := p.revisedParts[entry.Name]; ok {
			size = uint64(len(raw))
		}
		expanded += size
	}
	if expanded > formats.EditOfficeLocalScanBytes {
		return ErrCalcCapacity
	}
	return nil
}

func (p *xlsxPackage) buildCalculationBook(ctx context.Context) (*CalculationBook, error) {
	layout := p.layout
	if p.displayPrecision || p.calendarErr != nil {
		return nil, ErrCalcSettings
	}
	b := &CalculationBook{
		source: xlsxPackage{layout: layout, names: p.names},
		cells:  make(map[editdoc.Address]calcCell), columns: make(map[calcColumn][]int),
		formulas: make(map[editdoc.Address]calculator.WorksheetFormula),
		arrays:   map[editdoc.Address]sourceFormulaTag{}, merges: map[string][]cellRect{}, barriers: map[string][]cellRect{},
		deps: newCalcDependencies(), date1904: p.date1904, hidden: map[string]map[int]bool{}, filtered: map[string][]cellRect{},
	}
	if len(layout.Pivots) > 0 && p.files["xl/styles.xml"] != nil {
		raw, err := p.readPart(ctx, "xl/styles.xml")
		if err != nil {
			return nil, err
		}
		b.pivotDateFormats, err = workbook.XLSXCalendarFormats(raw)
		if err != nil {
			return nil, err
		}
	}
	edges, formulaBytes := 0, 0
	for _, sheet := range layout.Sheets {
		data, err := p.readPart(ctx, sheet.Part)
		if err != nil {
			return nil, err
		}
		b.hidden[sheet.Name] = map[int]bool{}
		structure := worksheetStructure{hidden: b.hidden[sheet.Name], visitCell: func(row, col int, c sheetCell) error {
			if c.value.Kind == "blank" {
				return nil
			}
			if len(b.cells) >= formats.EditCalcSourceCells {
				return ErrCalcCapacity
			}
			a := editdoc.Address{Sheet: sheet.Name, Row: row, Col: col}
			b.cells[a] = calcCell{c.value, c.cache, c.formula}
			k := calcColumn{a.Sheet, col}
			b.columns[k] = append(b.columns[k], row)
			if c.value.Kind == "formula" {
				formulaBytes += len(c.value.Raw)
				if formulaBytes > formats.EditCalcFormulaBytes {
					return ErrCalcCapacity
				}
				program := b.compile(ctx, a, c.value.Raw, c.formula)
				b.formulas[a] = program
				if !program.Bound() {
					b.unbound = append(b.unbound, a)
				}
				edges += len(program.References)
				if edges > formats.EditCalcReferences {
					return ErrCalcCapacity
				}
				for _, ref := range program.References {
					b.deps.add(a, b.qualify(ref, a.Sheet))
				}
				if program.Volatile || c.cache == nil || p.staleFormulaCaches || b.constantCacheDiffers(ctx, program, c) {
					b.missing = append(b.missing, a)
				}
				if c.formula.kind == "array" && (!c.formula.area.single() || c.formula.dynamic && program.MaySpill) && program.Bound() {
					b.arrays[a] = c.formula
				} else if c.formula.kind == "array" && !c.formula.area.single() {
					b.addOwner(a.Sheet, cellRect{row, col, row, col}, a.Sheet, c.formula.area, false)
				}
			}
			return nil
		}}
		checked, err := p.scanSheet(ctx, data, sheet, nil, nil, nil, &structure)
		if err != nil {
			if layout.Native && ctx.Err() == nil && !errors.Is(err, ErrCalcCapacity) {
				return nil, ErrCalcSettings
			}
			return nil, err
		}
		if !layout.Native && checked != sheet {
			return nil, ErrBinding
		}
		b.merges[sheet.Name] = structure.merges
		for _, block := range structure.blocks {
			b.barriers[sheet.Name] = append(b.barriers[sheet.Name], block.area)
		}
		for _, owner := range p.pivotRanges[sheet.Name] {
			b.barriers[sheet.Name] = append(b.barriers[sheet.Name], owner.area)
		}
		for _, table := range layout.Tables {
			if table.Sheet == sheet.Name {
				b.barriers[sheet.Name] = append(b.barriers[sheet.Name], table.bounds())
			}
		}
		if structure.filtered {
			rows := structure.filter
			rows.firstRow++ // the worksheet filter includes its header in ref.
			b.filtered[sheet.Name] = append(b.filtered[sheet.Name], rows)
		}
		for _, t := range layout.Tables {
			if t.Sheet == sheet.Name && t.Filtered {
				first, last := t.dataRows()
				b.filtered[sheet.Name] = append(b.filtered[sheet.Name], cellRect{firstRow: first, lastRow: last})
			}
		}
		for _, owner := range p.pivotRanges[sheet.Name] {
			if owner.pivot != "" {
				src := p.pivotSources[p.pivotOutputs[owner.pivot]]
				b.addOwner(src.sheet, src.area, sheet.Name, owner.area, true)
			}
		}
	}
	return b, ctx.Err()
}

// A generator can save a foreign rounding result even for a formula with
// no precedents. Checking these roots never requires visiting other cells.
func (b *CalculationBook) constantCacheDiffers(ctx context.Context, program calculator.WorksheetFormula, c sheetCell) bool {
	if !program.Bound() || len(program.References) != 0 || c.formula.kind == "array" && !c.formula.area.single() {
		return false
	}
	actual, err := program.Evaluate(calculator.WithDateSystem(ctx, b.date1904), nil)
	if err != nil {
		return false
	}
	want, err := calcOperand(*c.cache, b.date1904)
	if err != nil || actual.Kind != want.Kind {
		return true
	}
	if actual.Kind == "number" {
		a, _ := actual.Number.Float64()
		w, _ := want.Number.Float64()
		return a != w
	}
	return actual.Text != want.Text || actual.Bool != want.Bool
}

func (b *CalculationBook) addOwner(input string, src cellRect, sheet string, dest cellRect, pivot bool) {
	a := editdoc.Area{Start: editdoc.Address{Sheet: sheet, Row: dest.firstRow, Col: dest.firstCol}, EndRow: dest.lastRow, EndCol: dest.lastCol}
	b.owners = append(b.owners, calcOwner{calculator.WorksheetReference{Sheet: input, Top: src.firstRow, Left: src.firstCol, Bottom: src.lastRow, Right: src.lastCol}, GeneratedArea{Area: a, Input: input, Pivot: pivot}})
}

func (b *CalculationBook) qualify(r calculator.WorksheetReference, current string) calculator.WorksheetReference {
	if r.Sheet == "" {
		r.Sheet = current
		return r
	}
	for _, s := range b.source.layout.Sheets {
		if strings.EqualFold(s.Name, r.Sheet) {
			r.Sheet = s.Name
			break
		}
	}
	return r
}

func (b *CalculationBook) compile(ctx context.Context, a editdoc.Address, raw string, tag sourceFormulaTag) calculator.WorksheetFormula {
	body, ok := strings.CutPrefix(raw, "=")
	if !ok {
		return calculator.WorksheetFormula{}
	}
	f, err := b.source.parseFormula(ctx, body, a, nil)
	if err != nil {
		return calculator.WorksheetFormula{}
	}
	return b.bind(f, a, tag.kind == "array")
}

func (b *CalculationBook) bind(f calculator.EditableFormula, a editdoc.Address, array bool) calculator.WorksheetFormula {
	return f.BindWorksheet(a.Sheet, a.Row, a.Col, array, func(name string) calculator.WorksheetName {
		if ref, _, ok := b.source.layout.tableReference(name, a); ok {
			return calculator.WorksheetName{Reference: &ref}
		}
		if code := b.source.layout.tableReferenceError(name, a); code != "" {
			return calculator.WorksheetName{Constant: calculator.FormulaValue{Kind: "error", Text: code}}
		}
		n, ok := b.source.names[nameKey{a.Sheet, name}]
		if !ok {
			n = b.source.names[nameKey{"", name}]
		}
		if len(n.areas) == 1 {
			area := n.areas[0]
			return calculator.WorksheetName{Reference: &calculator.WorksheetReference{Sheet: area.Sheet, Top: area.Row, Left: area.Col, Bottom: area.EndRow, Right: area.EndCol}}
		}
		var constant calculator.FormulaValue
		switch {
		case len(n.areas) != 0:
		case n.definition == "#REF!":
			constant = calculator.FormulaValue{Kind: "error", Text: "#REF!"}
		case strings.HasPrefix(n.definition, "\""):
			constant = calculator.TextValue(strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(n.definition, "\""), "\""), "\"\"", "\""))
		case strings.EqualFold(n.definition, "true"), strings.EqualFold(n.definition, "false"):
			constant = calculator.BoolValue(strings.EqualFold(n.definition, "true"))
		default:
			if number, err := calculator.Canonical(n.definition); err == nil {
				constant = calculator.NumberValue(number)
			}
		}
		return calculator.WorksheetName{Constant: constant}
	})
}

type CalculatedCell struct {
	State   string
	Value   editdoc.Value
	Formula editdoc.Value
}

type Calculation struct {
	Cells map[editdoc.Address]CalculatedCell
	// Affected is the conservative fallback when the source index exceeds capacity.
	Affected     map[string]bool
	Generated    []GeneratedArea
	Arrays       map[editdoc.Address]arrayResult
	ArrayMembers map[editdoc.Address]editdoc.Value
	Visits       int
	patches      map[editdoc.Address]editdoc.Value
	programs     map[editdoc.Address]calculator.WorksheetFormula
}

func (c *Calculation) GeneratedStatus(a editdoc.Address) string {
	for _, area := range c.Generated {
		if area.Contains(a) {
			if area.Pivot {
				return "待刷新"
			}
			if a != area.Start {
				return "待重算"
			}
		}
	}
	return ""
}

func (c *Calculation) FormulaResult(a editdoc.Address, source SourceCell) (string, *editdoc.Value) {
	if cell, ok := c.Cells[a]; ok {
		if cell.State == "calculated" {
			return cell.State, &cell.Value
		}
		return cell.State, nil
	}
	if c.Affected[a.Sheet] {
		return "pending", nil
	}
	if source.Cache == nil {
		return "missing", nil
	}
	return "saved", source.Cache
}

type calcRevision struct {
	ctx           context.Context
	book          *CalculationBook
	patches       map[editdoc.Address]editdoc.Value
	added         map[calcColumn][]int
	programs      map[editdoc.Address]calculator.WorksheetFormula
	dirty         map[editdoc.Address]bool
	busy          map[editdoc.Address]bool
	waiting       map[editdoc.Address]bool
	typed         map[editdoc.Address]calculator.FormulaValue
	positions     map[calculator.WorksheetReference][]editdoc.Address
	aggregates    map[calculator.WorksheetReference]calculator.RangeValue
	positionCount int
	resultBytes   int
	arrays        *arrayPass
	result        Calculation
}

func inCalcArea(r calculator.WorksheetReference, a editdoc.Address) bool {
	return r.Sheet == a.Sheet && a.Row >= r.Top && a.Row <= r.Bottom && a.Col >= r.Left && a.Col <= r.Right
}
func compareCalcAddress(a, b editdoc.Address) int {
	if d := cmp.Compare(a.Sheet, b.Sheet); d != 0 {
		return d
	}
	if d := cmp.Compare(a.Row, b.Row); d != 0 {
		return d
	}
	return cmp.Compare(a.Col, b.Col)
}

// Calculate takes ownership of an immutable document snapshot. Previous must
// belong to the same base and document; a cold restart can always pass nil.
func (b *CalculationBook) calculatePass(ctx context.Context, patches map[editdoc.Address]editdoc.Value, programs map[editdoc.Address]calculator.WorksheetFormula, previous *Calculation, arrays *arrayPass) (*Calculation, error) {
	s := &calcRevision{
		ctx: calculator.WithDateSystem(ctx, b.date1904), book: b, patches: patches, arrays: arrays,
		added: map[calcColumn][]int{}, programs: programs,
		busy: map[editdoc.Address]bool{}, waiting: map[editdoc.Address]bool{}, typed: map[editdoc.Address]calculator.FormulaValue{},
		positions: map[calculator.WorksheetReference][]editdoc.Address{},
		result:    Calculation{Cells: map[editdoc.Address]CalculatedCell{}, patches: patches, programs: programs},
	}
	roots := slices.Clone(b.missing)
	overlay := newCalcDependencies()
	for a, v := range patches {
		if v.Kind == "" {
			continue
		}
		roots = append(roots, a)
		_, exists := b.cells[a]
		if !exists {
			k := calcColumn{a.Sheet, a.Col}
			s.added[k] = append(s.added[k], a.Row)
		}
		if v.Kind == "formula" {
			form := programs[a]
			for _, ref := range form.References {
				overlay.add(a, b.qualify(ref, a.Sheet))
			}
		}
	}
	if arrays != nil {
		roots = arrays.seed(s, roots)
	}
	for key, rows := range s.added {
		slices.Sort(rows)
		s.added[key] = slices.Compact(rows)
	}
	var queue []editdoc.Address
	var err error
	queue, s.dirty, s.result.Generated, err = b.dependents(ctx, roots, overlay)
	if err != nil {
		return nil, err
	}
	if previous != nil && len(previous.Cells) > 0 {
		// Removing an edit is itself a change. Styles do not alter operands.
		var changed []editdoc.Address
		for a, v := range patches {
			old := previous.patches[a]
			if v.Kind != old.Kind || v.Raw != old.Raw {
				changed = append(changed, a)
			}
		}
		for a, old := range previous.patches {
			if _, ok := patches[a]; !ok && old.Kind != "" {
				changed = append(changed, a)
			}
		}
		_, invalid, _, err := b.dependents(ctx, changed, overlay)
		if err != nil {
			return nil, err
		}
		for a, cell := range previous.Cells {
			if s.dirty[a] && !invalid[a] && cell.State == "calculated" {
				cell.Formula = s.current(a)
				if !s.store(a, cell) {
					return nil, ErrCalcCapacity
				}
			}
		}
	}
	for _, a := range queue {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if s.resultBytes > formats.EditCalcProjectionBytes {
			return nil, ErrCalcCapacity
		}
		if v := s.current(a); v.Kind == "formula" {
			s.evaluate(a)
			if _, ok := s.result.Cells[a]; !ok {
				s.store(a, CalculatedCell{Formula: v, State: "pending"})
			}
		}
	}
	if s.resultBytes > formats.EditCalcProjectionBytes {
		return nil, ErrCalcCapacity
	}
	return &s.result, ctx.Err()
}

func (b *CalculationBook) dependents(ctx context.Context, roots []editdoc.Address, overlay calcDependencies) ([]editdoc.Address, map[editdoc.Address]bool, []GeneratedArea, error) {
	dirty := make(map[editdoc.Address]bool)
	queue := make([]editdoc.Address, 0, len(roots))
	mark := func(a editdoc.Address) {
		if !dirty[a] {
			dirty[a] = true
			queue = append(queue, a)
		}
	}
	slices.SortFunc(roots, compareCalcAddress)
	for _, a := range roots {
		mark(a)
	}
	if len(b.unbound) > 0 {
		changedSheets := map[string]bool{}
		for _, a := range queue {
			changedSheets[a.Sheet] = true
		}
		affected := b.source.layout.Affected(changedSheets)
		for _, a := range b.unbound {
			if affected[a.Sheet] {
				mark(a)
			}
		}
	}
	fired := map[calculator.WorksheetReference]bool{}
	owners := make([]bool, len(b.owners))
	var generated []GeneratedArea
	deps := []calcDependencies{b.deps, overlay}
	fire := func(r calculator.WorksheetReference) {
		if fired[r] {
			return
		}
		fired[r] = true
		for _, d := range deps {
			for _, a := range d.ranges[r] {
				mark(a)
			}
		}
	}
	propagated := 0
	for i := 0; i < len(queue); i++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		a := queue[i]
		for _, d := range deps {
			for _, dependent := range d.exact[a] {
				mark(dependent)
			}
			for _, c := range []int{a.Col, 0} {
				for _, block := range []int{(a.Row - 1) / 128, -1} {
					for _, r := range d.bands[calcBand{calcColumn{a.Sheet, c}, block}] {
						if inCalcArea(r, a) {
							fire(r)
						}
					}
				}
			}
		}
		for j, owner := range b.owners {
			if owners[j] || !inCalcArea(owner.input, a) {
				continue
			}
			owners[j] = true
			generated = append(generated, owner.output)
		}
		// Generated regions can themselves supply the next pivot. Propagate
		// intersections even when the two sources share only unallocated cells.
		for propagated < len(generated) {
			o := generated[propagated]
			propagated++
			for j, owner := range b.owners {
				if !owners[j] && intersectsGenerated(owner.input, o) {
					owners[j] = true
					generated = append(generated, owner.output)
				}
			}
			for _, d := range deps {
				for addr := range d.exact {
					if o.Contains(addr) {
						mark(addr)
					}
				}
				for r := range d.ranges {
					if intersectsGenerated(r, o) {
						fire(r)
					}
				}
			}
		}
	}
	return queue, dirty, generated, ctx.Err()
}

func intersectsGenerated(r calculator.WorksheetReference, o GeneratedArea) bool {
	return r.Sheet == o.Start.Sheet && r.Top <= o.EndRow && r.Bottom >= o.Start.Row && r.Left <= o.EndCol && r.Right >= o.Start.Col
}
