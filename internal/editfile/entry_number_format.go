package editfile

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"maps"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// The source styles belong to the pre-entry revision. A pasted or filled
// expression can share one inference across all its targets on this sheet.
type entryFormatInput struct {
	hints    []calculator.EntryFormatOperand
	winner   editdoc.Value
	resolved bool
}

func (l XLSXLayout) entryFormatReferences(ctx context.Context, inputs []editdoc.Change, targets []editdoc.Address, anchor *editdoc.Address) (map[editdoc.Address]*entryFormatInput, []editdoc.Address) {
	refs := make(map[editdoc.Address]*entryFormatInput)
	type expression struct{ sheet, raw string }
	parsed := make(map[expression]*entryFormatInput)
	seeds := make(map[editdoc.Address]bool)
	sheets := make(map[string]string, len(l.Sheets))
	for _, s := range l.Sheets {
		sheets[strings.ToLower(s.Name)] = s.Name
	}
	for _, c := range inputs {
		if c.Value.Kind != "formula" || c.Value.Style != "" || anchor != nil && c.Address != *anchor {
			continue
		}
		key := expression{c.Sheet, c.Value.Raw}
		input, seen := parsed[key]
		if !seen {
			f, err := l.FormulaForFill(ctx, c.Value.Raw)
			if err == nil {
				hints := f.EntryFormatOperands(c.Sheet)
				for i := range hints {
					h := &hints[i]
					if h.Row == 0 {
						continue
					}
					source := c.Sheet
					if h.Sheet != "" {
						source = sheets[strings.ToLower(h.Sheet)]
						h.Sheet = source
					}
					seeds[editdoc.Address{Sheet: source, Row: h.Row, Col: h.Col}] = true
				}
				if len(hints) != 0 {
					input = &entryFormatInput{hints: hints}
				}
			}
			parsed[key] = input
		}
		if input != nil {
			refs[c.Address] = input
		}
	}
	if anchor != nil {
		if r, ok := refs[*anchor]; ok {
			for _, c := range inputs {
				if c.Sheet == anchor.Sheet && c.Value.Kind == "formula" && c.Value.Style == "" {
					refs[c.Address] = r
				}
			}
		}
	}
	for _, a := range targets {
		delete(seeds, a)
	}
	return refs, slices.Collect(maps.Keys(seeds))
}

type entryXF struct{ start, tagEnd, end, number int }
type entryFormatTable struct {
	data                   []byte
	xfs                    []entryXF
	calendar               map[int]bool
	start, tagEnd, closeAt int
}

func entryFormats(ctx context.Context, p *xlsxPackage) (entryFormatTable, error) {
	var t entryFormatTable
	if p.files["xl/styles.xml"] == nil {
		return t, nil
	}
	data, err := p.readPart(ctx, "xl/styles.xml")
	if err != nil {
		return t, err
	}
	return parseEntryFormats(ctx, data)
}

func parseEntryFormats(ctx context.Context, data []byte) (entryFormatTable, error) {
	var t entryFormatTable
	t.data = data
	var parents []int
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if len(e.stack) < 2 || e.stack[len(e.stack)-1].Space != mainNS {
			return nil
		}
		switch v := e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 3 && e.parent() == "numFmts" && e.local() == "numFmt" {
				id, _ := strconv.Atoi(attribute(v, "", "numFmtId"))
				if t.calendar == nil {
					t.calendar = map[int]bool{}
				}
				t.calendar[id] = workbook.CalendarDateNumberFormat(id, attribute(v, "", "formatCode"))
			}
			if len(e.stack) == 2 && e.local() == "cellXfs" {
				t.start, t.tagEnd = e.from, e.to
			}
			if len(e.stack) != 3 || e.local() != "xf" {
				return nil
			}
			n, _ := strconv.Atoi(attribute(v, "", "numFmtId"))
			switch e.parent() {
			case "cellStyleXfs":
				parents = append(parents, n)
			case "cellXfs":
				flag := attribute(v, "", "applyNumberFormat")
				parent, _ := strconv.Atoi(attribute(v, "", "xfId"))
				if (flag == "0" || flag == "false") && parent >= 0 && parent < len(parents) {
					n = parents[parent]
				}
				t.xfs = append(t.xfs, entryXF{start: e.from, tagEnd: e.to, number: n})
			}
		case xml.EndElement:
			if len(e.stack) == 3 && e.parent() == "cellXfs" && e.local() == "xf" {
				t.xfs[len(t.xfs)-1].end = e.to
			}
			if len(e.stack) == 2 && e.local() == "cellXfs" {
				t.closeAt = e.from
			}
		}
		return nil
	})
	return t, err
}

func (t entryFormatTable) number(style string) int {
	if style == "" {
		style = "0"
	}
	i, err := strconv.Atoi(style)
	if i == 0 && err == nil && len(t.xfs) == 0 {
		return 0
	}
	if err != nil || i < 0 || i >= len(t.xfs) {
		return -1
	}
	return t.xfs[i].number
}

type xfFormatKey struct {
	body   [sha256.Size]byte
	number int
}

// Exclude the two number-format attributes only. Custom alignment, named
// style, protection and extension markup must all match before reusing an XF.
func (t entryFormatTable) keys(ctx context.Context) ([]xfFormatKey, error) {
	keys := make([]xfFormatKey, len(t.xfs))
	for i, x := range t.xfs {
		raw := t.data[x.start:x.end]
		ps, err := removeXMLAttributes(raw, 0, x.tagEnd-x.start, "numFmtId", "applyNumberFormat")
		if err != nil {
			return nil, err
		}
		h := sha256.New()
		if err = writeXMLPatches(ctx, h, raw, ps); err != nil {
			return nil, err
		}
		copy(keys[i].body[:], h.Sum(nil))
		keys[i].number = x.number
	}
	return keys, nil
}

func (t entryFormatTable) effectiveNumber(v editdoc.Value) int {
	if v.BuiltinFormat != 0 {
		return v.BuiltinFormat
	}
	return t.number(v.NumberFormatStyle())
}

func (t entryFormatTable) calendarDate(id int) bool {
	if date, ok := t.calendar[id]; ok {
		return date
	}
	return workbook.CalendarDateNumberFormat(id, "")
}

func (b EditBatch) inheritEntryFormats(ctx context.Context, f *workbook.EditSourceFile, changes []editdoc.Change, doc *editdoc.Document, refs map[editdoc.Address]*entryFormatInput) error {
	t, err := b.layout.entryStyleTable(ctx, f)
	if err != nil {
		return err
	}
	for i := range changes {
		c := &changes[i]
		input := refs[c.Address]
		if input == nil {
			continue
		}
		before := doc.ValueAt(c.Address, b.SourceValue(c.Address))
		if t.effectiveNumber(before) != 0 {
			continue
		}
		if !input.resolved {
			input.winner, _ = t.formulaNumberFormat(input.hints, func(h calculator.EntryFormatOperand) editdoc.Value {
				sheet := h.Sheet
				if sheet == "" {
					sheet = c.Sheet
				}
				a := editdoc.Address{Sheet: sheet, Row: h.Row, Col: h.Col}
				if index := b.layout.index; index != nil {
					if source, ok := index.cells[a]; ok {
						return doc.ValueAt(a, source.value)
					}
				}
				return doc.ValueAt(a, b.SourceValue(a))
			})
			input.resolved = true
		}
		winner := input.winner
		if winner == (editdoc.Value{}) {
			continue
		}
		c.Value.Style = before.Style
		if c.Value.Style == "" {
			c.Value.Style = "0"
		}
		if winner.BuiltinFormat != 0 {
			c.Value.BuiltinFormat = winner.BuiltinFormat
		} else {
			c.Value.NumberStyle = winner.NumberFormatStyle()
			if c.Value.NumberStyle == "" {
				c.Value.NumberStyle = "0"
			}
		}
	}
	return nil
}

func (t entryFormatTable) formulaNumberFormat(hints []calculator.EntryFormatOperand, cell func(calculator.EntryFormatOperand) editdoc.Value) (editdoc.Value, bool) {
	var first editdoc.Value
	found, firstDate, conflict := false, false, false
	for _, h := range hints {
		if h.AfterArithmetic && (conflict || found && !firstDate) {
			found, firstDate, conflict = false, false, false
			continue
		}
		if conflict {
			continue
		}
		origin := editdoc.Value{BuiltinFormat: h.Builtin}
		if h.Row > 0 {
			origin = cell(h)
		}
		id := t.effectiveNumber(origin)
		if id <= 0 || h.Sheet != "" && id >= 164 {
			continue
		}
		if !found {
			if h.SumTime && id == 21 {
				origin = editdoc.Value{BuiltinFormat: 46}
			}
			first, found, firstDate = origin, true, t.calendarDate(id)
		} else if firstDate && t.calendarDate(id) {
			conflict = true
		}
	}
	if !found || conflict {
		return editdoc.Value{}, false
	}
	return first, true
}

func (p *xlsxPackage) materializeNumberStyles(ctx context.Context, edits map[editdoc.Address]editdoc.Value) (map[editdoc.Address]editdoc.Value, []xmlPatch, error) {
	var t entryFormatTable
	var ids map[xfNumber]string
	seeded, originalBytes := false, 0
	for _, v := range edits {
		if v.NumberStyle == "" && v.BuiltinFormat == 0 {
			continue
		}
		if ids == nil {
			var err error
			t, err = entryFormats(ctx, p)
			if err != nil {
				return nil, nil, err
			}
			if len(t.xfs) == 0 {
				originalBytes = len(t.data)
				t, err = seedEntryFormats(ctx, t.data)
				if err != nil {
					return nil, nil, err
				}
				p.styles = len(t.xfs)
				seeded = true
			}
			ids = make(map[xfNumber]string)
		}
		ids[xfNumber{v.Style, t.effectiveNumber(v)}] = ""
	}
	if ids == nil {
		return edits, nil, nil
	}
	patches, err := p.composeNumberFormats(ctx, t, ids)
	if err != nil {
		return nil, nil, err
	}
	if seeded {
		data, err := applyXML(ctx, t.data, patches)
		if err != nil {
			return nil, nil, err
		}
		patches = []xmlPatch{{0, originalBytes, string(data)}}
	}
	physical := maps.Clone(edits)
	p.numberStyleSources = edits
	for a, v := range physical {
		if v.NumberStyle != "" || v.BuiltinFormat != 0 {
			v.Style, v.NumberStyle, v.BuiltinFormat = ids[xfNumber{v.Style, t.effectiveNumber(v)}], "", 0
			physical[a] = v
		}
	}
	return physical, patches, nil
}

type xfNumber struct {
	base   string
	number int
}

func (p *xlsxPackage) logicalEdit(a editdoc.Address, physical editdoc.Value) editdoc.Value {
	if v, ok := p.numberStyleSources[a]; ok {
		return v
	}
	return physical
}

// Retain the destination's font, border, alignment and protection. All cells
// needing the same combination share one existing or newly composed XF.
func (p *xlsxPackage) composeNumberFormats(ctx context.Context, t entryFormatTable, ids map[xfNumber]string) ([]xmlPatch, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(t.xfs) != p.styles || t.closeAt == 0 {
		return nil, ErrBinding
	}
	pairs := slices.Collect(maps.Keys(ids))
	slices.SortFunc(pairs, func(a, b xfNumber) int {
		if a.base != b.base {
			return strings.Compare(a.base, b.base)
		}
		return cmp.Compare(a.number, b.number)
	})
	var additions strings.Builder
	keys, err := t.keys(ctx)
	if err != nil {
		return nil, err
	}
	known := map[xfFormatKey]string{}
	for i, k := range keys {
		if _, ok := known[k]; !ok {
			known[k] = strconv.Itoa(i)
		}
	}
	added := 0
	for _, key := range pairs {
		i, err := strconv.Atoi(key.base)
		n := key.number
		if err != nil || i < 0 || i >= len(t.xfs) || n < 0 {
			return nil, ErrBinding
		}
		if t.xfs[i].number == n {
			ids[key] = key.base
			continue
		}
		slot := xfFormatKey{body: keys[i].body, number: n}
		if id, ok := known[slot]; ok {
			ids[key] = id
			continue
		}
		// Excel allows 65,490 unique cell formats in a workbook.
		if len(t.xfs)+added >= min(65490, formats.EditXLSXStyleIndex+1) {
			return nil, ErrXLSX
		}
		x := t.xfs[i]
		source := t.data[x.start:x.end]
		if len(t.data)+additions.Len()+len(source)+64 > formats.EditXLSXPartBytes {
			return nil, ErrXLSX
		}
		ps, err := removeXMLAttributes(source, 0, x.tagEnd-x.start, "numFmtId", "applyNumberFormat")
		if err != nil {
			return nil, err
		}
		at := x.tagEnd - x.start - 1
		if source[at-1] == '/' {
			at--
		}
		ps = append(ps, xmlPatch{at, at, ` numFmtId="` + strconv.Itoa(n) + `" applyNumberFormat="1"`})
		if err = writeXMLPatches(ctx, &additions, source, ps); err != nil {
			return nil, err
		}
		id := strconv.Itoa(len(t.xfs) + added)
		added++
		known[slot], ids[key] = id, id
	}
	if added == 0 {
		return nil, nil
	}
	p.styles += added
	ps, err := setXMLAttribute(t.data, t.start, t.tagEnd, "count", strconv.Itoa(p.styles))
	return append(ps, xmlPatch{t.closeAt, t.closeAt, additions.String()}), err
}
