package workbook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/conditional"
	"excellens/internal/formats"
	"excellens/internal/styles"
)

// ConditionalPresentation is a derived view. Source Raw, Display, Type,
// Style, Ref, formula/cache state and rich text remain unchanged.
type ConditionalPresentation struct {
	ConditionalFormatResult
	Visual     *VisualStyle `json:"visual,omitempty"`
	Display    *string      `json:"display,omitempty"`
	ValueColor *string      `json:"valueColor,omitempty"`
}

type conditionalAddress struct{ row, col int }
type conditionalRangeKey struct{ r1, c1, r2, c2 int }

type conditionalRead struct {
	statBindings                     map[boundStatKey]boundStatQuery
	statistics                       *conditional.Index
	statNotice                       string
	statPending                      bool
	statNeedsBuild                   bool
	statDamaged                      bool
	readOnlyStats                    bool
	statsStatus                      string
	statProgress                     func(int, int)
	w                                *Workbook
	idx                              *SheetIndex
	ctx                              context.Context
	index                            *SourceConditionalFormats
	evaluator                        *ConditionalEvaluator
	stream                           indexedStream
	row                              RowData
	first, last                      int
	selected                         map[int]struct{}
	columns                          []int
	active                           conditionalSourceRow
	batchRows                        []conditionalSourceRow
	batchStart                       int
	batchPositions                   map[int]int
	operandResolver                  ConditionalCellResolver
	batchFirst, batchLast            int
	batchSelected                    map[int]struct{}
	cache                            map[conditionalAddress]CellData
	foreignValues                    map[conditionalForeignAddress]CellData
	foreignOpens                     int
	rangeCache                       map[conditionalRangeKey]conditional.RangeData
	cacheBytes                       int
	remainingBytes, remainingCells   int
	dependencyReads, dependencyBytes int
}

func (w *Workbook) newConditionalRead(ctx context.Context, idx *SheetIndex, file *os.File, options RowReadOptions) (*conditionalRead, error) {
	if !options.ConditionalFormats {
		return nil, nil
	}
	capability, ok := formats.Lookup(w.Format)
	if !ok || !capability.ConditionalMetadata {
		return nil, nil
	}
	var meta SheetMeta
	for _, candidate := range w.Sheets {
		if candidate.Path == idx.SheetPath {
			meta = candidate
			break
		}
	}
	if meta.Path == "" {
		return nil, errors.New("条件呈现的工作表与源索引不匹配")
	}
	index, err := w.SourceConditionalFormats(ctx, meta, idx)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		index = &SourceConditionalFormats{root: -1, notice: "条件格式元数据无法读取，原值保留：" + conditionalNotice(err.Error())}
	}
	if len(index.groups) == 0 && index.notice == "" {
		return nil, nil
	}
	r := &conditionalRead{
		w:             w,
		idx:           idx,
		ctx:           ctx,
		index:         index,
		evaluator:     index.NewEvaluator(ctx),
		stream:        indexedStream{f: file, idx: idx},
		cache:         make(map[conditionalAddress]CellData),
		rangeCache:    make(map[conditionalRangeKey]conditional.RangeData),
		readOnlyStats: options.ConditionalStatsReadOnly,
		statsStatus:   options.ConditionalStatsStatus,
	}
	r.operandResolver = r.resolve
	r.evaluator.SetRangeResolver(r.resolveRange)
	r.evaluator.engine.ForeignCell = func(sheet string, row, col int) (conditional.CellOperand, string, error) {
		cell, notice, err := r.resolveForeignCell(sheet, row, col)
		return toConditionalOperand(cell), notice, err
	}
	if len(index.statistics) > 0 && idx.RowCount() >= formats.ConditionalStatsMinRows {
		r.evaluator.engine.StatHandler = r.indexedStatistic
	}
	r.columns = slices.Clone(options.Columns)
	if !slices.IsSorted(r.columns) {
		slices.Sort(r.columns)
	}
	r.columns = slices.Compact(r.columns)
	r.resetBudget()
	return r, nil
}

func conditionalNotice(s string) string {
	if len(s) <= formats.ConditionalNoticeBytes {
		return s
	}
	return strings.ToValidUTF8(s[:formats.ConditionalNoticeBytes], "") + "…"
}

func (r *conditionalRead) resetBudget() {
	r.foreignOpens = formats.ConditionalForeignOpens
	r.remainingBytes, r.remainingCells = formats.ConditionalReadBytes, formats.ConditionalReadCells
	r.dependencyReads, r.dependencyBytes = formats.ConditionalDependencyReads, formats.ConditionalDependencyXMLBytes
	r.evaluator.engine.ResetBudget()
}

func conditionalOperand(cell CellData) CellData {
	return CellData{Raw: cell.Raw, Type: cell.Type, CacheMissing: cell.CacheMissing}
}

func (r *conditionalRead) batchHasColumn(col int) bool {
	if col < r.batchFirst || col > r.batchLast {
		return false
	}
	if r.batchSelected == nil {
		return true
	}
	_, ok := r.batchSelected[col]
	return ok
}

func (r *conditionalRead) cacheSingleCell(rowNum, col int, cell CellData) {
	key := conditionalAddress{rowNum, col}
	if _, exists := r.cache[key]; exists {
		return
	}
	cost := 512 + 2*(len(cell.Raw)+len(cell.Ref)+len(cell.Type))
	if len(r.cache) < formats.ConditionalDependencyReads && cost <= formats.ConditionalDependencyCacheBytes-r.cacheBytes {
		r.cache[key] = conditionalOperand(cell)
		r.cacheBytes += cost
	}
}

func (r *conditionalRead) resolve(row, col int) (CellData, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return CellData{}, false, err
	}
	if row == r.row.Number && col >= r.first && col <= r.last {
		if _, included := r.selected[col]; r.selected == nil || included {
			return r.active.cell(col), true, nil // an omitted, read column is a proven blank
		}
	}
	if r.batchRows != nil && r.batchHasColumn(col) {
		if source, ok := r.batchRow(row); ok {
			return conditionalOperand(source.cell(col)), true, nil
		}
	}
	key := conditionalAddress{row, col}
	if cell, ok := r.cache[key]; ok {
		return cell, true, nil
	}
	position := r.idx.FindRowOffset(row)
	if position == r.idx.RowCount() || r.idx.RowAt(position).Number != row {
		return CellData{}, true, nil
	}
	offset := r.idx.RowAt(position)
	size := offset.End - offset.Start
	if r.dependencyReads == 0 || size <= 0 || size > int64(r.dependencyBytes) {
		return CellData{}, false, &conditional.DependencyUnavailableError{Reason: "条件格式引用超过本次读取容量，未用空白或零替代"}
	}
	r.dependencyReads--
	r.dependencyBytes -= int(size)
	data, err := r.stream.readWithOptions(r.w, position, col, col, nil, RowReadOptions{})
	if err != nil {
		if r.ctx.Err() != nil {
			return CellData{}, false, r.ctx.Err()
		}
		return CellData{}, false, &conditional.DependencyUnavailableError{Reason: "条件格式引用读取失败，原值保留：" + conditionalNotice(err.Error())}
	}
	cell := CellData{}
	for _, candidate := range data.Cells {
		if candidate.Col == col {
			cell = candidate
			break
		}
	}
	r.cacheSingleCell(row, col, cell)
	return cell, true, nil
}

func (r *conditionalRead) resolveRange(r1, c1, r2, c2 int) (conditional.RangeData, string, error) {
	if err := r.ctx.Err(); err != nil {
		return conditional.RangeData{}, "", err
	}
	key := conditionalRangeKey{r1, c1, r2, c2}
	if cached, ok := r.rangeCache[key]; ok {
		return cached, "", nil
	}
	width := c2 - c1 + 1
	totalArea := int64(r2-r1+1) * int64(width)
	// Fast path: range entirely within the currently loaded row window.
	if r1 == r2 && r1 == r.row.Number && c1 >= r.first && c2 <= r.last && r.selected == nil {
		out := conditional.RangeData{R1: r1, C1: c1, R2: r2, C2: c2}
		r.active.appendRange(&out, r1, c1, c2)
		out.BlankCount = totalArea - int64(len(out.Cells))
		return out, "", nil
	}
	startPos := r.idx.FindRowOffset(r1)
	endPos := r.idx.FindRowOffset(r2 + 1)
	occupiedRows := endPos - startPos
	if occupiedRows > formats.ConditionalDependencyReads || int64(occupiedRows)*int64(width) > int64(formats.ConditionalRangeCells) {
		return conditional.RangeData{}, "条件格式引用范围超过本次读取容量，未扫描整表", nil
	}
	out := conditional.RangeData{R1: r1, C1: c1, R2: r2, C2: c2}
	estBytes := 128
	batchCoversCols := r.batchRows != nil && c1 >= r.batchFirst && c2 <= r.batchLast && r.batchSelected == nil
	for pos := startPos; pos < endPos; pos++ {
		if err := r.ctx.Err(); err != nil {
			return conditional.RangeData{}, "", err
		}
		offset := r.idx.RowAt(pos)
		rowNum := offset.Number
		if rowNum == r.row.Number && c1 >= r.first && c2 <= r.last && r.selected == nil {
			estBytes += r.active.appendRange(&out, rowNum, c1, c2)
			continue
		}
		if batchCoversCols {
			if source, ok := r.batchRow(rowNum); ok {
				estBytes += source.appendRange(&out, rowNum, c1, c2)
				continue
			}
		}
		if width == 1 {
			if cached, ok := r.cache[conditionalAddress{rowNum, c1}]; ok {
				if cached.CacheMissing || cached.Type != "" || cached.Raw != "" {
					out.Cells = append(out.Cells, conditional.RangeCell{Row: rowNum, Col: c1, Cell: toConditionalOperand(cached)})
					estBytes += len(cached.Raw) + len(cached.Type)
				}
				continue
			}
		}
		size := offset.End - offset.Start
		if r.dependencyReads == 0 || size <= 0 || size > int64(r.dependencyBytes) {
			return conditional.RangeData{}, "条件格式引用超过本次读取容量，未用空白或零替代", nil
		}
		r.dependencyReads--
		r.dependencyBytes -= int(size)
		data, err := r.stream.readWithOptions(r.w, pos, c1, c2, nil, RowReadOptions{})
		if err != nil {
			if r.ctx.Err() != nil {
				return conditional.RangeData{}, "", r.ctx.Err()
			}
			return conditional.RangeData{}, "条件格式引用读取失败，原值保留：" + conditionalNotice(err.Error()), nil
		}
		var singleFound CellData
		for _, cell := range data.Cells {
			if cell.Col < c1 || cell.Col > c2 {
				continue
			}
			if width == 1 && cell.Col == c1 {
				singleFound = cell
			}
			if !cell.CacheMissing && cell.Type == "" && cell.Raw == "" {
				continue
			}
			out.Cells = append(out.Cells, conditional.RangeCell{Row: rowNum, Col: cell.Col, Cell: toConditionalOperand(cell)})
			estBytes += len(cell.Raw) + len(cell.Type)
		}
		if width == 1 {
			r.cacheSingleCell(rowNum, c1, singleFound)
		}
	}
	// Count backing-array capacity and retained source text before caching.
	estBytes += cap(out.Cells) * 512
	out.BlankCount = totalArea - int64(len(out.Cells))
	if len(r.rangeCache) < formats.ConditionalRangeCacheEntries && estBytes <= formats.ConditionalDependencyCacheBytes-r.cacheBytes {
		r.rangeCache[key] = out
		r.cacheBytes += estBytes
	}
	return out, "", nil
}

func (r *conditionalRead) apply(row *RowData, first, last int, selected map[int]struct{}) error {
	return r.applySource(row, indexConditionalRow(row.Cells), first, last, selected)
}

func (r *conditionalRead) applySource(row *RowData, source conditionalSourceRow, first, last int, selected map[int]struct{}) error {
	r.row, r.first, r.last, r.selected = *row, first, last, selected
	r.active = source
	var extra []CellData
	columns := r.columns
	count := last - first + 1
	if selected != nil {
		if len(columns) == 0 {
			for col := range selected {
				columns = append(columns, col)
			}
			sort.Ints(columns)
		}
		count = len(columns)
	}
	for at := 0; at < count; at++ {
		col := first + at
		if selected != nil {
			col = columns[at]
			if col < first || col > last {
				continue
			}
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		notice := r.evaluator.prepare(row.Number, col)
		if notice == "" && len(r.evaluator.rules) == 0 {
			if selected == nil {
				at = r.evaluator.nextCol - first - 1
			}
			continue
		}
		if r.remainingCells == 0 {
			row.PresentationNotice = "条件格式超过本次显示格数容量，原文完整保留"
			break
		}
		r.remainingCells--
		position := source.position(col)
		var cell CellData
		if position >= 0 {
			cell = row.Cells[position]
		} else {
			cell = CellData{Ref: ColumnName(col) + strconv.Itoa(row.Number), Col: col, Style: row.Style, PresentationOnly: true}
		}
		result, err := r.evaluator.evaluatePrepared(row.Number, col, toConditionalOperand(cell), r.operandResolver, notice)
		if err != nil {
			return err
		}
		if result == nil {
			continue
		}
		view := r.w.conditionalPresentation(cell, *result)
		encoded, err := json.Marshal(view)
		if err != nil {
			return err
		}
		if len(encoded) > r.remainingBytes {
			row.PresentationNotice = "条件格式超过本次显示字节容量，原文完整保留"
			break
		}
		r.remainingBytes -= len(encoded)
		cell.Conditional = view
		if position >= 0 {
			row.Cells[position].Conditional = view
		} else {
			extra = append(extra, cell)
		}
	}
	row.Cells = source.withExtra(extra)
	return nil
}

func (w *Workbook) conditionalPresentation(cell CellData, result ConditionalFormatResult) *ConditionalPresentation {
	p := &ConditionalPresentation{ConditionalFormatResult: result}
	if result.Style.Empty() {
		return p
	}
	visual := VisualStyle{}
	if cell.Style >= 0 && cell.Style < len(w.VisualStyles) {
		visual = w.VisualStyles[cell.Style]
	}
	v, display, valueColor, notice := styles.ApplyDifferential(visual, result.Style, cell.Raw, cell.Type, cell.CacheMissing, w.Date1904)
	p.Visual = v
	p.Display = display
	p.ValueColor = valueColor
	if notice != "" {
		p.Notice = joinConditionalNotice(p.Notice, notice)
	}
	return p
}

func joinConditionalNotice(a, b string) string {
	if a == "" {
		return b
	}
	return conditionalNotice(a + "；" + b)
}
