package editfile

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

func (p *xlsxPackage) clipStyleCatalog(ctx context.Context) (*clipStyleBook, error) {
	var raw []byte
	var err error
	if p.files[entryStylePart] != nil {
		raw, err = p.readPart(ctx, entryStylePart)
		if err != nil {
			return nil, err
		}
	}
	return readClipStyleBook(ctx, raw)
}

// Reader copy uses its existing row projection and merge index. Only the
// style part needs package access, independently of edit eligibility.
func ReaderClipboardRange(ctx context.Context, file *workbook.EditSourceFile, first editdoc.Address, rows []int, cols int, values []editdoc.Value, sourceMerges []workbook.CellMerge) (*editdoc.PortableRange, error) {
	z, err := zip.NewReader(file, file.Size())
	if err != nil {
		return nil, err
	}
	var raw []byte
	for _, f := range z.File {
		if f.Name == entryStylePart {
			store := xlsxPackage{files: map[string]*zip.File{entryStylePart: f}}
			raw, err = store.readPart(ctx, entryStylePart)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	merges := make([]cellRect, 0, len(sourceMerges))
	for _, m := range sourceMerges {
		merges = append(merges, cellRect{m.Row, m.Col, m.EndRow, m.EndCol})
	}
	book, err := readClipStyleBook(ctx, raw)
	if err != nil {
		return nil, err
	}
	return clipFromCells(ctx, first, rows, cols, values, book, merges)
}
func clipFromCells(ctx context.Context, first editdoc.Address, rows []int, cols int, values []editdoc.Value, book *clipStyleBook, merges []cellRect) (*editdoc.PortableRange, error) {
	if len(rows) == 0 || cols < 1 || len(rows) > formats.EditBatchCells/cols || len(values) != len(rows)*cols {
		return nil, ErrTarget
	}
	clip := &editdoc.PortableRange{Start: first, Rows: len(rows), Cols: cols, Cells: make([]editdoc.PortableCell, len(values))}
	rowIndex := map[int]int{}
	for i, r := range rows {
		rowIndex[r] = i
		if r != first.Row+i {
			clip.SourceRows = rows
		}
	}
	type styleKey struct {
		style, number string
		builtin       int
	}
	slots := map[styleKey]int{}
	ids := map[editdoc.PortableStyle]int{}
	for i, v := range values {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := styleKey{v.Style, v.NumberStyle, v.BuiltinFormat}
		idx, ok := slots[key]
		if !ok {
			style, err := book.style(v)
			if err != nil {
				return nil, err
			}
			idx, ok = ids[style]
			if !ok {
				idx = len(clip.Styles)
				ids[style] = idx
				clip.Styles = append(clip.Styles, style)
			}
			slots[key] = idx
		}
		v.Style, v.NumberStyle, v.BuiltinFormat = "", "", 0
		if v.Kind == "missing" {
			v = editdoc.Value{Kind: "blank"}
		}
		if !v.Valid() {
			return nil, ErrTarget
		}
		clip.Cells[i] = editdoc.PortableCell{Value: v, Style: idx}
	}
	for _, m := range merges {
		i, ok := rowIndex[m.firstRow]
		if !ok || m.firstCol < first.Col || m.lastCol >= first.Col+cols {
			continue
		}
		h := m.lastRow - m.firstRow + 1
		if i+h > len(rows) {
			continue
		}
		contiguous := true
		for off := 1; off < h; off++ {
			if rows[i+off] != m.firstRow+off {
				contiguous = false
				break
			}
		}
		if contiguous {
			clip.Merges = append(clip.Merges, editdoc.Area{Start: editdoc.Address{Sheet: first.Sheet, Row: i + 1, Col: m.firstCol - first.Col + 1}, EndRow: i + h, EndCol: m.lastCol - first.Col + 1})
		}
	}
	return clip, nil
}

func (l Layout) PortableSelection(ctx context.Context, f *workbook.EditSourceFile, first editdoc.Address, rows []int, cols int, values []editdoc.Value) (*editdoc.PortableRange, error) {
	c := l.cache
	var styles *clipStyleBook
	var merges []cellRect
	var cached bool
	if c != nil {
		c.mu.Lock()
		if c.key == l.cacheKey {
			styles = c.copyStyles
			merges, cached = c.copyMerges[first.Sheet]
		}
		c.mu.Unlock()
	}
	if !cached || styles == nil {
		p, err := openXLSXPackage(f, f.Size())
		if err != nil {
			return nil, err
		}
		if styles == nil {
			styles, err = p.clipStyleCatalog(ctx)
			if err != nil {
				return nil, err
			}
		}
		if !cached {
			i := slices.IndexFunc(l.XLSX.Sheets, func(s XLSXSheet) bool { return s.Name == first.Sheet })
			if i < 0 {
				return nil, ErrTarget
			}
			part := l.XLSX.Sheets[i].Part
			// Merges follow sheetData. The existing owner scan also handles prefixes
			// and bounds; keep only the coordinates in the revision-scoped cache.
			raw, err := p.readPart(ctx, part)
			if err != nil {
				return nil, err
			}
			metadata, err := sheetOwners(ctx, raw, first.Sheet)
			if err != nil {
				return nil, err
			}
			merges = metadata.merges
		}
		if err := f.Check(); err != nil {
			return nil, err
		}
		if c != nil {
			c.mu.Lock()
			if c.key == l.cacheKey {
				if len(styles.raw) < 512<<10 && len(styles.xfs) <= 2048 {
					c.copyStyles = styles
				}
				if !cached && len(merges) <= 4096 {
					if len(c.copyMerges) >= 8 {
						clear(c.copyMerges)
					}
					if c.copyMerges == nil {
						c.copyMerges = map[string][]cellRect{}
					}
					c.copyMerges[first.Sheet] = merges
				}
			}
			c.mu.Unlock()
		}
	}
	return clipFromCells(ctx, first, rows, cols, values, styles, merges)
}

type portablePaste struct {
	sheet       rangeSheet
	target      cellRect
	ordered     []int
	merges      []cellRect
	updates     map[string][]byte
	changes     []editdoc.Change
	addresses   []editdoc.Address
	values      map[editdoc.Address]editdoc.Value
	scalarCells bool
}

func (p *xlsxPackage) planPortable(ctx context.Context, op editdoc.Structure) (portablePaste, error) {
	var empty portablePaste
	clip := op.Paste
	target, ok := parseRect(op.Range)
	if !ok || clip == nil || clip.Rows < 1 || clip.Cols < 1 || clip.Rows > formats.EditBatchCells/clip.Cols || len(clip.Cells) != clip.Rows*clip.Cols || len(clip.Styles) == 0 {
		return empty, ErrTarget
	}
	if op.PasteVisibleOnly {
		var err error
		clip, err = visibleClipboardRange(clip)
		if err != nil {
			return empty, err
		}
	}
	rows := op.TargetRows
	if len(rows) == 0 {
		count := target.lastRow - target.firstRow + 1
		if count > formats.EditBatchCells {
			return empty, ErrTarget
		}
		rows = make([]int, count)
		for i := range rows {
			rows[i] = target.firstRow + i
		}
	}
	width := target.lastCol - target.firstCol + 1
	if len(rows) > formats.EditBatchCells/width || len(rows)%clip.Rows != 0 || width%clip.Cols != 0 || len(clip.SourceRows) != 0 && len(clip.SourceRows) != clip.Rows || len(clip.SourceCols) != 0 && len(clip.SourceCols) != clip.Cols {
		return empty, ErrTarget
	}
	sheet, err := p.rangeSheet(ctx, op.Sheet)
	if err != nil {
		return empty, err
	}
	rowIndex := map[int]int{}
	for i, row := range rows {
		if _, dup := rowIndex[row]; dup || row < 1 || row > formats.ExcelRows {
			return empty, ErrTarget
		}
		rowIndex[row] = i
	}
	// Check each physical run, including descending or sorted visible rows,
	// without treating filtered-out cells as part of the destination.
	ordered := slices.Sorted(maps.Keys(rowIndex))
	merges := make([]cellRect, 0, len(sheet.owners.merges)+len(clip.Merges))
	for _, m := range sheet.owners.merges {
		first, _ := slices.BinarySearch(ordered, m.firstRow)
		last, _ := slices.BinarySearch(ordered, m.lastRow+1)
		covered := last - first
		if covered == 0 || m.lastCol < target.firstCol || m.firstCol > target.lastCol {
			merges = append(merges, m)
			continue
		}
		if covered != m.lastRow-m.firstRow+1 || m.firstCol < target.firstCol || m.lastCol > target.lastCol {
			return empty, errors.New("粘贴区域只覆盖了一部分合并单元格")
		}
	}
	for _, m := range clip.Merges {
		if !m.Valid() || m.EndRow > clip.Rows || m.EndCol > clip.Cols {
			return empty, ErrTarget
		}
		for i := 0; i < len(rows); i += clip.Rows {
			first := i + m.Start.Row - 1
			for j := first + 1; j <= i+m.EndRow-1; j++ {
				if rows[j] != rows[j-1]+1 {
					return empty, errors.New("合并单元格跨过筛选后的隐藏行，请选择连续的目标行")
				}
			}
			for col := target.firstCol; col <= target.lastCol; col += clip.Cols {
				merges = append(merges, cellRect{rows[first], col + m.Start.Col - 1, rows[i+m.EndRow-1], col + m.EndCol - 1})
			}
		}
	}
	updates := map[string][]byte{}
	styles := make([]string, len(clip.Styles))
	if !sheet.owners.protected {
		styles, err = p.importClipStyles(ctx, clip.Styles, updates)
	}
	if err != nil {
		return empty, err
	}
	changes := make([]editdoc.Change, 0, len(rows)*width)
	addresses := make([]editdoc.Address, 0, len(rows)*width)
	roots := map[editdoc.Address]editdoc.Value{}
	for i, row := range rows {
		r := i % clip.Rows
		sourceRow := clip.Start.Row + r
		if len(clip.SourceRows) > 0 {
			sourceRow = clip.SourceRows[r]
		}
		for j := 0; j < width; j++ {
			col := j % clip.Cols
			cell := clip.Cells[r*clip.Cols+col]
			v := cell.Value
			if !v.Valid() || cell.Style < 0 || cell.Style >= len(styles) {
				return empty, ErrTarget
			}
			addr := editdoc.Address{Sheet: op.Sheet, Row: row, Col: target.firstCol + j}
			if v.Kind == "formula" {
				sourceCol := clip.Start.Col + col
				if len(clip.SourceCols) > 0 {
					sourceCol = clip.SourceCols[col]
				}
				v.Raw, err = copyRangeFormula(v.Raw, addr.Row-sourceRow, addr.Col-sourceCol)
				if err != nil {
					return empty, err
				}
				if _, err = p.parseFormula(ctx, strings.TrimPrefix(v.Raw, "="), addr, nil); err != nil {
					return empty, errors.New("粘贴公式的引用或函数无法在目标工作簿中解析")
				}
			}
			if !sheet.owners.protected {
				v.Style = styles[cell.Style]
			}
			changes = append(changes, editdoc.Change{Address: addr, Value: v})
			addresses = append(addresses, addr)
			roots[addr] = v
		}
	}
	return portablePaste{sheet: sheet, target: target, ordered: ordered, merges: merges, updates: updates, changes: changes, addresses: addresses, values: roots, scalarCells: len(clip.Merges) == 0 && len(merges) == len(sheet.owners.merges)}, nil
}

// Ordinary compatible formats can use the existing sparse change path. This
// preserves the filtered view and avoids writing an entire structural revision.
func (l XLSXLayout) PortableGridChanges(ctx context.Context, f *workbook.EditSourceFile, op editdoc.Structure) ([]editdoc.Change, error) {
	p, err := openXLSXPackage(f, f.Size())
	if err != nil {
		return nil, err
	}
	p.layout.Native, p.layout.Structured = l.Native, l.Structured
	if err = p.loadWorkbook(ctx); err != nil {
		return nil, err
	}
	p.layout = l
	plan, err := p.planPortable(ctx, op)
	if err != nil {
		return nil, err
	}
	if !plan.scalarCells || len(plan.updates) > 0 {
		return nil, nil
	}
	return plan.changes, f.Check()
}

func (p *xlsxPackage) pastePortableRange(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure) (opc.Receipt, error) {
	var empty opc.Receipt
	plan, err := p.planPortable(ctx, op)
	if err != nil {
		return empty, err
	}
	sheet, target, ordered, merges, updates, changes, addresses, roots := plan.sheet, plan.target, plan.ordered, plan.merges, plan.updates, plan.changes, plan.addresses, plan.values
	if plan.scalarCells {
		apply := func(q *xlsxPackage, file *workbook.EditSourceFile) error {
			if len(op.TargetRows) == 0 && !sheet.owners.protected {
				growth, err := q.layout.TableTail(changes)
				if err != nil {
					return err
				}
				if growth != nil {
					empty, err = growth.write(ctx, "", q, dst, file, changes, false, nil)
					return err
				}
			}
			writes := map[string][]byte{}
			current := map[editdoc.Address]editdoc.Value{}
			entry := tableEntry{changes: changes}
			if err := entry.applyTo(ctx, q, file, writes, current); err != nil {
				return err
			}
			if len(current) == 0 {
				return editdoc.ErrNoChange
			}
			var err error
			empty, err = q.finishCellStructure(ctx, dst, file, writes, current)
			return err
		}
		if len(updates) > 0 {
			err = p.withRevisedFile(ctx, "", updates, apply)
		} else {
			err = apply(p, src)
		}
		return empty, err
	}
	if _, addsStyles := updates[entryStylePart]; addsStyles && p.files[entryStylePart] == nil {
		err = p.withRevisedFile(ctx, "", updates, func(q *xlsxPackage, file *workbook.EditSourceFile) error {
			var err error
			empty, err = q.pastePortableRange(ctx, dst, file, op)
			return err
		})
		return empty, err
	}
	for i := 0; i < len(ordered); {
		j := i + 1
		for j < len(ordered) && ordered[j] == ordered[j-1]+1 {
			j++
		}
		if err = p.checkTransferOwners(ctx, sheet, cellRect{ordered[i], target.firstCol, ordered[j-1], target.lastCol}); err != nil {
			return empty, err
		}
		i = j
	}
	sheet.data, err = p.expandStructureGroups(ctx, sheet.data, op.Sheet, axisEdit{Structure: editdoc.Structure{Sheet: op.Sheet}})
	if err != nil {
		return empty, err
	}
	raw, err := p.rangeCells(ctx, sheet, addresses, func(c cellTarget, _ []byte) (string, bool, error) {
		if err := transferCellAllowed(c); err != nil {
			return "", false, err
		}
		a := c.address
		v := roots[a]
		var out strings.Builder
		cellXML(&out, a, v.Style, v)
		return out.String(), true, nil
	})
	if err != nil {
		return empty, err
	}
	updates[sheet.sheet.Part], err = transferMerges(ctx, sheet, raw, merges)
	if err != nil {
		return empty, err
	}
	return p.finishCellStructure(ctx, dst, src, updates, roots)
}
