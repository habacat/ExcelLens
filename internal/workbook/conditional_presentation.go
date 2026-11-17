package workbook

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"excellens/internal/formats"
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
type conditionalDependencyUnavailable struct{ reason string }

func (e *conditionalDependencyUnavailable) Error() string { return e.reason }

type conditionalRead struct {
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
	cells                            map[int]CellData
	cache                            map[conditionalAddress]CellData
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
	r := &conditionalRead{w: w, idx: idx, ctx: ctx, index: index, evaluator: index.NewEvaluator(ctx), stream: indexedStream{f: file, idx: idx}, cache: make(map[conditionalAddress]CellData)}
	r.columns = append([]int(nil), options.Columns...)
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
	r.remainingBytes, r.remainingCells = formats.ConditionalReadBytes, formats.ConditionalReadCells
	r.dependencyReads, r.dependencyBytes = formats.ConditionalDependencyReads, formats.ConditionalDependencyXMLBytes
	r.evaluator.work, r.evaluator.textBytes = formats.ConditionalEvalWork, formats.ConditionalEvalTextBytes
}

func (r *conditionalRead) resolve(row, col int) (CellData, bool, error) {
	if err := r.ctx.Err(); err != nil {
		return CellData{}, false, err
	}
	if row == r.row.Number && col >= r.first && col <= r.last {
		if _, included := r.selected[col]; r.selected == nil || included {
			return r.cells[col], true, nil // an omitted, read column is a proven blank
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
		return CellData{}, false, &conditionalDependencyUnavailable{reason: "条件格式引用超过本次读取容量，未用空白或零替代"}
	}
	r.dependencyReads--
	r.dependencyBytes -= int(size)
	data, err := r.stream.readWithOptions(r.w, position, col, col, nil, RowReadOptions{})
	if err != nil {
		if r.ctx.Err() != nil {
			return CellData{}, false, r.ctx.Err()
		}
		return CellData{}, false, &conditionalDependencyUnavailable{reason: "条件格式引用读取失败，原值保留：" + conditionalNotice(err.Error())}
	}
	cell := CellData{}
	for _, candidate := range data.Cells {
		if candidate.Col == col {
			cell = candidate
			break
		}
	}
	bytes := 512 + 2*(len(cell.Raw)+len(cell.Ref)+len(cell.Type))
	if len(r.cache) < formats.ConditionalDependencyReads && bytes <= formats.ConditionalDependencyCacheBytes-r.cacheBytes {
		r.cache[key] = cell
		r.cacheBytes += bytes
	}
	return cell, true, nil
}

func (r *conditionalRead) apply(row *RowData, first, last int, selected map[int]struct{}) error {
	r.row, r.first, r.last, r.selected = *row, first, last, selected
	r.cells = make(map[int]CellData, len(row.Cells))
	for _, cell := range row.Cells {
		r.cells[cell.Col] = cell
	}
	positions := make(map[int]int, len(row.Cells))
	for i, cell := range row.Cells {
		positions[cell.Col] = i
	}
	added := false
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
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.remainingCells == 0 {
			row.PresentationNotice = "条件格式超过本次显示格数容量，原文完整保留"
			break
		}
		r.remainingCells--
		cell, exists := r.cells[col]
		if !exists {
			cell = CellData{Ref: ColumnName(col) + strconv.Itoa(row.Number), Col: col, Style: row.Style, PresentationOnly: true}
		}
		result, err := r.evaluator.Evaluate(row.Number, col, cell, r.resolve)
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
		if position, ok := positions[col]; ok {
			row.Cells[position] = cell
		} else {
			row.Cells = append(row.Cells, cell)
			added = true
		}
	}
	if added {
		sort.SliceStable(row.Cells, func(i, j int) bool { return row.Cells[i].Col < row.Cells[j].Col })
	}
	return nil
}

func (w *Workbook) conditionalPresentation(cell CellData, result ConditionalFormatResult) *ConditionalPresentation {
	p := &ConditionalPresentation{ConditionalFormatResult: result}
	if result.Style.empty() {
		return p
	}
	visual := VisualStyle{}
	if cell.Style >= 0 && cell.Style < len(w.VisualStyles) {
		visual = w.VisualStyles[cell.Style]
	}
	d := result.Style
	f := d.Font
	if f.Name != "" {
		visual.FontName = f.Name
	}
	if f.Size != 0 {
		visual.FontSize = f.Size
	}
	if f.Color != "" || f.AutoColor {
		visual.Color = f.Color
		empty := ""
		p.ValueColor = &empty
	}
	if f.Bold != nil {
		visual.Bold = *f.Bold
	}
	if f.Italic != nil {
		visual.Italic = *f.Italic
	}
	if f.Strike != nil {
		visual.Strike = *f.Strike
	}
	if f.Underline != "" {
		visual.Underline = f.Underline != "none"
		visual.UnderlineStyle = f.Underline
	}
	if f.Vertical != "" {
		visual.FontVertical = f.Vertical
	}
	if d.Background != nil {
		visual.Background = *d.Background
	}
	if d.Horizontal != nil {
		visual.Horizontal = *d.Horizontal
	}
	if d.Vertical != nil {
		visual.Vertical = *d.Vertical
	}
	if d.Wrap != nil {
		visual.Wrap = *d.Wrap
	}
	if d.Rotation != nil {
		visual.Rotation = *d.Rotation
	}
	if d.Border != nil {
		for _, side := range []struct {
			target *string
			diff   *DifferentialBorderSide
		}{{&visual.BorderTop, d.Border.Top}, {&visual.BorderBottom, d.Border.Bottom}, {&visual.BorderLeft, d.Border.Left}, {&visual.BorderRight, d.Border.Right}} {
			*side.target = conditionalBorder(*side.target, side.diff, visual.Color)
		}
	}
	if d.NumberFormat != nil && !cell.CacheMissing && (cell.Type == "" || cell.Type == "n") && cell.Raw != "" {
		format := d.NumberFormat.Code
		display := cell.Raw
		if !strings.EqualFold(strings.TrimSpace(format), "General") {
			value, err := strconv.ParseFloat(cell.Raw, 64)
			if err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && (value != 0 || isDecimalZero(cell.Raw)) {
				if value == 0 {
					value = 0
				} // normalize an exact signed zero like the source formatter
				if exact, ok := formatDecimalDisplay(cell.Raw, format, d.NumberFormat.ID); ok {
					display = exact
				} else {
					display = formatCustomNumber(value, format, w.Date1904)
				}
			} else {
				p.Notice = joinConditionalNotice(p.Notice, "条件数字格式未应用：源数值超出显示计算范围")
			}
		}
		p.Display = &display
		color := conditionalNumberColor(cell.Raw, sectionTextColors(d.NumberFormat.ID, format))
		if f.Color != "" || f.AutoColor {
			color = ""
		}
		p.ValueColor = &color
	}
	p.Visual = &visual
	return p
}

func conditionalNumberColor(raw string, colors [3]string) string {
	n, ok := parseConditionalDecimal(raw)
	if !ok {
		return ""
	}
	if n.digits == "0" && colors[2] != "" {
		return colors[2]
	}
	if n.negative {
		return colors[1]
	}
	return colors[0]
}

func joinConditionalNotice(a, b string) string {
	if a == "" {
		return b
	}
	return conditionalNotice(a + "；" + b)
}

func conditionalBorder(base string, diff *DifferentialBorderSide, foreground string) string {
	if diff == nil {
		return base
	}
	color := foreground
	if color == "" {
		color = "#000000"
	}
	if at := strings.LastIndexByte(base, ' '); at >= 0 {
		color = base[at+1:]
	}
	if diff.Color != nil {
		color = *diff.Color
	} else if diff.AutoColor {
		color = foreground
		if color == "" {
			color = "#000000"
		}
	}
	if diff.Style != nil {
		if *diff.Style == "" || *diff.Style == "none" {
			return ""
		}
		return borderCSS(styleSideXML{Style: *diff.Style}, color)
	}
	if base != "" && (diff.Color != nil || diff.AutoColor) {
		if at := strings.LastIndexByte(base, ' '); at >= 0 {
			return base[:at+1] + color
		}
	}
	return base
}
