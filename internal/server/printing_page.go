package server

import (
	"context"
	"encoding/json"

	"slices"
	"sort"
	"strconv"

	"excellens/internal/formats"
	"excellens/internal/i18n"
	"excellens/internal/printing"
	"excellens/internal/workbook"
)

var errPrintPageCapacity = i18n.Error("service.printingPage.contentOnThisPageExceeds")

type printPageRect struct {
	X      int64 `json:"x,string"`
	Y      int64 `json:"y,string"`
	Width  int64 `json:"width,string"`
	Height int64 `json:"height,string"`
}

// Start/Size describe the complete source dimension. ClipStart/ClipSize
// describe only its physical slice on this page. Neither is display-grid px.
type printPageAxis struct {
	Number    int   `json:"number"`
	Part      int   `json:"part"`
	Start     int64 `json:"start,string"`
	Size      int64 `json:"size,string"`
	ClipStart int64 `json:"clipStart,string"`
	ClipSize  int64 `json:"clipSize,string"`
	Repeated  bool  `json:"repeated"`
}

type printPageCell struct {
	Row          int                 `json:"row"`
	Col          int                 `json:"col"`
	Ref          string              `json:"ref"`
	Cell         *workbook.CellData  `json:"cell,omitempty"`
	Style        int                 `json:"style"`
	Box          printPageRect       `json:"box"`
	Clip         printPageRect       `json:"clip"`
	Merge        *workbook.CellMerge `json:"merge,omitempty"`
	HiddenAnchor bool                `json:"hiddenAnchor,omitempty"`
	Borders      *printCellBorders   `json:"borders,omitempty"`
}

type printCellBorders struct {
	Top    string `json:"borderTop,omitempty"`
	Right  string `json:"borderRight,omitempty"`
	Bottom string `json:"borderBottom,omitempty"`
	Left   string `json:"borderLeft,omitempty"`
}

type printPageContent struct {
	Page    printing.Page                `json:"page"`
	Rows    []printPageAxis              `json:"rows"`
	Columns []printPageAxis              `json:"columns"`
	Cells   []printPageCell              `json:"cells"`
	Styles  map[int]workbook.VisualStyle `json:"styles"`
	Notices []string                     `json:"notices"`
}

type printExtentReader interface {
	Extent(context.Context, string, int, int) (workbook.PrintGeometryExtent, error)
}

type printPageWork struct {
	ctx   context.Context
	steps int
	bytes int
}

func (work *printPageWork) spend() error {
	if err := work.ctx.Err(); err != nil {
		return err
	}
	work.steps++
	if work.steps > formats.PrintPreviewPageWork {
		return errPrintPageCapacity
	}
	return nil
}

func (work *printPageWork) extent(g printExtentReader, axis string, first, last int) (int64, error) {
	if last < first {
		return 0, nil
	}
	if err := work.spend(); err != nil {
		return 0, err
	}
	value, err := g.Extent(work.ctx, axis, first, last)
	return value.Units, err
}

func (work *printPageWork) reserve(bytes int) error {
	if bytes < 0 || bytes > formats.PrintPreviewPageBytes-work.bytes {
		return errPrintPageCapacity
	}
	work.bytes += bytes
	return nil
}

// A zero-length interval is skipped with prefix queries and binary search.
// A million hidden source records never become a million page row objects.
func (work *printPageWork) axis(g printExtentReader, axis string, parts []printing.Part, maximum int) ([]printPageAxis, error) {
	items := []printPageAxis{}
	var start int64
	for partIndex, part := range parts {
		position, distance := part.First, -part.Offset
		for position <= part.Last {
			size, err := work.extent(g, axis, position, position)
			if err != nil {
				return nil, err
			}
			if size == 0 {
				remaining, err := work.extent(g, axis, position, part.Last)
				if err != nil {
					return nil, err
				}
				if remaining == 0 {
					break
				}
				low, high := position+1, part.Last
				for low < high {
					middle := low + (high-low)/2
					prefix, err := work.extent(g, axis, position, middle)
					if err != nil {
						return nil, err
					}
					if prefix > 0 {
						high = middle
					} else {
						low = middle + 1
					}
				}
				position = low
				continue
			}
			if len(items) >= maximum {
				return nil, errPrintPageCapacity
			}
			clipStart := max(distance, 0)
			// The last dimension can exceed the part's remaining slice. Avoid
			// adding its full size to a prefix, which could overflow int64.
			visible := min(size-max(-distance, 0), part.Length-clipStart)
			if visible > 0 {
				items = append(items, printPageAxis{Number: position, Part: partIndex, Start: start + distance, Size: size, ClipStart: start + clipStart, ClipSize: visible, Repeated: part.Repeated})
			}
			if size >= part.Length-distance {
				break
			}
			distance += size
			position++
		}
		start += part.Length
	}
	return items, nil
}

type printAxisLookup []printPageAxis

func printLookup(items []printPageAxis) printAxisLookup {
	values := slices.Clone(items)
	slices.SortFunc(values, func(a, b printPageAxis) int { return a.Number - b.Number })
	return values
}

func (items printAxisLookup) within(first, last int) []printPageAxis {
	start := sort.Search(len(items), func(at int) bool { return items[at].Number >= first })
	end := sort.Search(len(items), func(at int) bool { return items[at].Number > last })
	return items[start:end]
}

type printAxisFragment struct{ start, size, clipStart, clipSize int64 }

func (work *printPageWork) fragments(g printExtentReader, axis string, items []printPageAxis, first, last int) ([]printAxisFragment, error) {
	size, err := work.extent(g, axis, first, last)
	if err != nil {
		return nil, err
	}
	groups := make(map[int]printAxisFragment)
	for _, item := range items {
		if err := work.spend(); err != nil {
			return nil, err
		}
		group, found := groups[item.Part]
		if !found {
			prefix, err := work.extent(g, axis, first, item.Number-1)
			if err != nil {
				return nil, err
			}
			group = printAxisFragment{item.Start - prefix, size, item.ClipStart, item.ClipSize}
		} else {
			end := max(group.clipStart+group.clipSize, item.ClipStart+item.ClipSize)
			group.clipStart = min(group.clipStart, item.ClipStart)
			group.clipSize = end - group.clipStart
		}
		groups[item.Part] = group
	}
	result := make([]printAxisFragment, 0, len(groups))
	for _, fragment := range groups {
		result = append(result, fragment)
	}
	slices.SortFunc(result, func(a, b printAxisFragment) int {
		if a.clipStart < b.clipStart {
			return -1
		}
		if a.clipStart > b.clipStart {
			return 1
		}
		return 0
	})
	return result, nil
}

func readPrintPage(ctx context.Context, job *printTask, snapshot *printSnapshot, index int) (*printPageContent, error) {
	page, err := snapshot.plan.Page(ctx, index)
	if err != nil {
		return nil, err
	}
	g, err := snapshot.plan.Geometry(ctx)
	if err != nil {
		return nil, err
	}
	work := &printPageWork{ctx: ctx, bytes: 4096}
	rows, err := work.axis(g, "row", page.Rows, formats.PrintPreviewPageRows)
	if err != nil {
		return nil, err
	}
	columns, err := work.axis(g, "col", page.Columns, formats.PrintPreviewPageColumns)
	if err != nil {
		return nil, err
	}
	if len(columns) != 0 && len(rows) > formats.PrintPreviewPageCells/len(columns) {
		return nil, errPrintPageCapacity
	}
	rowLookup, columnLookup := printLookup(rows), printLookup(columns)
	covered := make(map[[2]int]int)
	merges := []workbook.CellMerge{}
	rowSet, columnSet := map[int]bool{}, map[int]bool{}
	for _, row := range rows {
		rowSet[row.Number] = true
	}
	for _, col := range columns {
		columnSet[col.Number] = true
	}
	for _, merge := range snapshot.merges {
		if err := work.spend(); err != nil {
			return nil, err
		}
		matchingRows, matchingCols := rowLookup.within(merge.Row, merge.EndRow), columnLookup.within(merge.Col, merge.EndCol)
		if len(matchingRows) == 0 || len(matchingCols) == 0 {
			continue
		}
		for _, row := range matchingRows {
			for _, col := range matchingCols {
				if err := work.spend(); err != nil {
					return nil, err
				}
				key := [2]int{row.Number, col.Number}
				if _, exists := covered[key]; exists {
					return nil, i18n.Error("service.printingPage.sourceMergeRegionsOverlapMaking")
				}
				covered[key] = len(merges)
			}
		}
		rowSet[merge.Row], columnSet[merge.Col] = true, true
		rowSet[merge.EndRow], columnSet[merge.EndCol] = true, true
		merges = append(merges, merge)
	}
	if len(rowSet) > formats.PrintPreviewPageRows || len(columnSet) > formats.PrintPreviewPageColumns {
		return nil, errPrintPageCapacity
	}
	rowNumbers, colNumbers := make([]int, 0, len(rowSet)), make([]int, 0, len(columnSet))
	for number := range rowSet {
		rowNumbers = append(rowNumbers, number)
	}
	for number := range columnSet {
		colNumbers = append(colNumbers, number)
	}
	slices.Sort(rowNumbers)
	slices.Sort(colNumbers)
	if len(rowNumbers) == 0 || len(colNumbers) == 0 {
		return nil, i18n.Error("service.printingPage.noSourceRowsAvailableOn")
	}
	content := &printPageContent{Page: page, Rows: rows, Columns: columns, Cells: []printPageCell{}, Styles: make(map[int]workbook.VisualStyle), Notices: []string{}}
	notice := func(text string) {
		if text != "" && len(content.Notices) < formats.PrintNoticeCount && !slices.Contains(content.Notices, text) {
			content.Notices = append(content.Notices, text)
		}
	}
	data := make(map[[2]int]workbook.CellData)
	rowStyles := make(map[int]int)
	readCells := 0
	err = job.Workbook.VisitRowsByNumbersWithOptionsContext(ctx, job.Index, slices.Values(rowNumbers), colNumbers[0], colNumbers[len(colNumbers)-1], workbook.RowReadOptions{Columns: colNumbers, FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true}, func(row workbook.RowData) error {
		rowStyles[row.Number] = row.Style
		notice(row.PresentationNotice)
		for _, cell := range row.Cells {
			if err := work.spend(); err != nil {
				return err
			}
			readCells++
			if readCells > formats.PrintPreviewPageCells {
				return errPrintPageCapacity
			}
			// Bound even worst-case JSON escaping before allocating the
			// complete response. Presentation metadata has its own budget.
			textBytes := len(cell.Ref) + len(cell.Raw) + len(cell.Display) + len(cell.Formula) + len(cell.ValueColor)
			if textBytes > (formats.PrintPreviewPageBytes-work.bytes)/6 {
				return errPrintPageCapacity
			}
			if err := work.reserve(textBytes*6 + 512); err != nil {
				return err
			}
			presentation, err := json.Marshal(struct {
				Rich        *workbook.RichText
				Conditional *workbook.ConditionalPresentation
				Link        *workbook.CellHyperlink
			}{cell.RichText, cell.Conditional, cell.Hyperlink})
			if err != nil {
				return err
			}
			if err := work.reserve(len(presentation)); err != nil {
				return err
			}
			data[[2]int{row.Number, cell.Col}] = cell
		}
		return nil
	})
	if err != nil {
		return nil, i18n.Wrap("service.printingPage.failedReadPrintPageSource", err)
	}
	visual := func(row, col int) workbook.VisualStyle {
		cell := data[[2]int{row, col}]
		if cell.Conditional != nil && cell.Conditional.Visual != nil {
			return *cell.Conditional.Visual
		}
		style := cell.Style
		if style == 0 && rowStyles[row] != 0 {
			style = rowStyles[row]
		}
		if style >= 0 && style < len(job.Workbook.VisualStyles) {
			return job.Workbook.VisualStyles[style]
		}
		return workbook.VisualStyle{}
	}
	appendCell := func(row, col int, box, clip printPageRect, merge *workbook.CellMerge, hidden bool) error {
		if err := work.spend(); err != nil {
			return err
		}
		if len(content.Cells) >= formats.PrintPreviewPageCells {
			return errPrintPageCapacity
		}
		item := printPageCell{Row: row, Col: col, Ref: workbook.ColumnName(col) + strconv.Itoa(row), Style: rowStyles[row], Box: box, Clip: clip, Merge: merge, HiddenAnchor: hidden}
		if merge != nil {
			own, tr, bl, br := visual(row, col), visual(row, merge.EndCol), visual(merge.EndRow, col), visual(merge.EndRow, merge.EndCol)
			first := func(values ...string) string {
				for _, value := range values {
					if value != "" {
						return value
					}
				}
				return ""
			}
			item.Borders = &printCellBorders{Top: first(own.BorderTop, tr.BorderTop), Left: first(own.BorderLeft, bl.BorderLeft), Right: first(tr.BorderRight, br.BorderRight, own.BorderRight), Bottom: first(bl.BorderBottom, br.BorderBottom, own.BorderBottom)}
			encoded, err := json.Marshal(item.Borders)
			if err != nil {
				return err
			}
			if err := work.reserve(len(encoded)); err != nil {
				return err
			}
		}
		if cell, found := data[[2]int{row, col}]; found {
			// A merged anchor can be printed in several disconnected title
			// and body fragments. Charge each emitted copy, not only its read.
			encoded, err := json.Marshal(cell)
			if err != nil {
				return err
			}
			if err := work.reserve(len(encoded)); err != nil {
				return err
			}
			item.Cell = &cell
			if cell.Style != 0 || item.Style == 0 {
				item.Style = cell.Style
			}
		}
		if _, exists := content.Styles[item.Style]; !exists && item.Style >= 0 && item.Style < len(job.Workbook.VisualStyles) {
			style := job.Workbook.VisualStyles[item.Style]
			encoded, err := json.Marshal(style)
			if err != nil {
				return err
			}
			if err := work.reserve(len(encoded) + 32); err != nil {
				return err
			}
			content.Styles[item.Style] = style
		}
		if err := work.reserve(512); err != nil {
			return err
		}
		content.Cells = append(content.Cells, item)
		return nil
	}
	for _, row := range rows {
		for _, col := range columns {
			if _, found := covered[[2]int{row.Number, col.Number}]; found {
				continue
			}
			if err := appendCell(row.Number, col.Number, printPageRect{col.Start, row.Start, col.Size, row.Size}, printPageRect{col.ClipStart, row.ClipStart, col.ClipSize, row.ClipSize}, nil, false); err != nil {
				return nil, err
			}
		}
	}
	for at := range merges {
		merge := &merges[at]
		rowFragments, err := work.fragments(g, "row", rowLookup.within(merge.Row, merge.EndRow), merge.Row, merge.EndRow)
		if err != nil {
			return nil, err
		}
		colFragments, err := work.fragments(g, "col", columnLookup.within(merge.Col, merge.EndCol), merge.Col, merge.EndCol)
		if err != nil {
			return nil, err
		}
		rowSize, err := work.extent(g, "row", merge.Row, merge.Row)
		if err != nil {
			return nil, err
		}
		colSize, err := work.extent(g, "col", merge.Col, merge.Col)
		if err != nil {
			return nil, err
		}
		for _, row := range rowFragments {
			for _, col := range colFragments {
				if err := appendCell(merge.Row, merge.Col, printPageRect{col.start, row.start, col.size, row.size}, printPageRect{col.clipStart, row.clipStart, col.clipSize, row.clipSize}, merge, rowSize == 0 || colSize == 0); err != nil {
					return nil, err
				}
			}
		}
	}
	return content, ctx.Err()
}
