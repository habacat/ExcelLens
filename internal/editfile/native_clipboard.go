package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"slices"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Excel's HTML and its XML Spreadsheet clipboard can both omit a formula.
// The embedded OOXML has the original coordinates and a workbook oleSize
// identifying the copied rectangle. Only that rectangle leaves this reader.
type NativeSelection struct {
	Sheet                    string
	VisibleRows, VisibleCols int
	RowRecords, Shadow       []byte
}

func NativeClipboardRange(ctx context.Context, ole []byte, selection NativeSelection) (*editdoc.PortableRange, error) {
	stream, err := workbook.EmbeddedWorkbook(bytes.NewReader(ole), int64(len(ole)))
	if err != nil {
		return nil, err
	}
	p, err := openXLSXPackage(stream, stream.Size())
	if err != nil {
		return nil, err
	}
	raw, err := p.readPart(ctx, workbookPart)
	if err != nil {
		return nil, err
	}
	var info struct {
		Size struct {
			Ref string `xml:"ref,attr"`
		} `xml:"oleSize"`
		View []struct {
			Active int `xml:"activeTab,attr"`
		} `xml:"bookViews>workbookView"`
	}
	if err = xml.Unmarshal(raw, &info); err != nil {
		return nil, err
	}
	area, ok := parseRect(info.Size.Ref)
	if !ok {
		return nil, errors.New("Excel 剪贴板缺少复制范围")
	}
	raw, err = removeElements(ctx, raw, func(el xml.StartElement) bool { return el.Name == (xml.Name{Space: mainNS, Local: "oleSize"}) })
	if err != nil {
		return nil, err
	}
	p.revisedParts = map[string][]byte{workbookPart: raw}
	p.layout.Native, p.layout.Structured = true, true
	if err = p.loadWorkbook(ctx); err != nil {
		return nil, err
	}
	active := 0
	if len(info.View) > 0 {
		active = info.View[0].Active
	}
	if selection.Sheet != "" {
		active = slices.IndexFunc(p.layout.Sheets, func(s XLSXSheet) bool { return s.Name == selection.Sheet })
	}
	if active < 0 || active >= len(p.layout.Sheets) {
		return nil, ErrTarget
	}
	sheet := p.layout.Sheets[active]
	data, err := p.readPart(ctx, sheet.Part)
	if err != nil {
		return nil, err
	}
	sourceArea := area
	var original, visibleRows, visibleCols []int
	h, w := area.lastRow-area.firstRow+1, area.lastCol-area.firstCol+1
	if (selection.VisibleRows > 0 && selection.VisibleRows < h) || (selection.VisibleCols > 0 && selection.VisibleCols < w) {
		filtered := false
		hidden := map[int]bool{}
		hiddenCols := map[int]bool{}
		err = walkXML(ctx, data, func(e xmlEvent) error {
			if el, ok := e.token.(xml.StartElement); ok && el.Name.Space == mainNS {
				if e.local() == "row" && e.parent() == "sheetData" && isTrue(attribute(el, "", "hidden")) {
					row, ok := positiveInt(attribute(el, "", "r"), formats.ExcelRows)
					if ok && row >= area.firstRow && row <= area.lastRow {
						hidden[row] = true
					}
				}
				if e.local() == "col" && e.parent() == "cols" && isTrue(attribute(el, "", "hidden")) {
					lo, ok1 := positiveInt(attribute(el, "", "min"), formats.ExcelColumns)
					hi, ok2 := positiveInt(attribute(el, "", "max"), formats.ExcelColumns)
					if ok1 && ok2 {
						for col := max(lo, area.firstCol); col <= min(hi, area.lastCol); col++ {
							hiddenCols[col] = true
						}
					}
				}
				if e.local() == "sheetPr" && isTrue(attribute(el, "", "filterMode")) || e.parent() == "autoFilter" && e.local() == "filterColumn" {
					filtered = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if selection.VisibleCols > 0 && selection.VisibleCols < w {
			if selection.VisibleCols != w-len(hiddenCols) {
				return nil, errors.New("无法核对复制区域中的隐藏列")
			}
			for col := area.firstCol; col <= area.lastCol; col++ {
				if !hiddenCols[col] {
					visibleCols = append(visibleCols, col)
				}
			}
		}
		if selection.VisibleRows > 0 && selection.VisibleRows < h {
			if filtered {
				original, err = workbook.ClipboardSourceRows(ctx, selection.RowRecords, area.firstRow, area.lastRow, area.firstCol, area.lastCol, selection.VisibleRows)
				if err != nil {
					return nil, err
				}
				area.lastRow = area.firstRow + len(original) - 1
			} else {
				if selection.VisibleRows != h-len(hidden) {
					return nil, errors.New("无法核对复制区域中的隐藏行")
				}
				if h > formats.EditBatchCells/w {
					return nil, ErrTarget
				}
				for row := area.firstRow; row <= area.lastRow; row++ {
					if !hidden[row] {
						visibleRows = append(visibleRows, row)
					}
				}
			}
		}
	}
	addrs, err := area.addresses(sheet.Name)
	if err != nil {
		return nil, err
	}
	var sourceRow func(int) int
	if len(original) > 0 {
		sourceRow = func(r int) int {
			n := r - area.firstRow
			if n >= 0 && n < len(original) {
				return original[n]
			}
			return r
		}
	}
	data, err = p.expandMappedGroups(ctx, data, sheet.Name, axisEdit{Structure: editdoc.Structure{Sheet: sheet.Name}}, sourceRow)
	if err != nil {
		return nil, err
	}
	targets, err := newSheetTargets(sheet, addrs)
	if err != nil {
		return nil, err
	}
	if _, err = p.scanSheet(ctx, data, sheet, targets, nil, nil, nil); err != nil {
		return nil, err
	}
	var rows []int
	var values []editdoc.Value
	for _, row := range targets.rows {
		rows = append(rows, row.row)
		for _, c := range row.cells {
			values = append(values, c.original.value)
		}
	}
	styles, err := p.clipStyleCatalog(ctx)
	if err != nil {
		return nil, err
	}
	owners, err := sheetOwners(ctx, data, sheet.Name)
	if err != nil {
		return nil, err
	}
	clip, err := clipFromCells(ctx, addrs[0], rows, w, values, styles, owners.merges)
	if err != nil {
		return nil, err
	}
	clip.VisibleSourceRows, clip.VisibleSourceCols = visibleRows, visibleCols
	if len(original) != 0 {
		clip.SourceRows = original
		clip.Start.Row = original[0]
	}
	return resolveClipboardVisibility(ctx, selection.Shadow, sourceArea, clip)
}
