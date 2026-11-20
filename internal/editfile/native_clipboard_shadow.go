package editfile

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
)

// Excel's optional ShadowWorkbook contains the cells as copied, starting at
// A1. It can replace formulas with cached results. Use it only to identify the
// selection; the embedded source remains authoritative for values and styles.
func resolveClipboardVisibility(ctx context.Context, shadow []byte, area cellRect, clip *editdoc.PortableRange) (*editdoc.PortableRange, error) {
	if len(shadow) == 0 || len(clip.VisibleSourceRows)+len(clip.VisibleSourceCols) == 0 {
		return clip, nil
	}
	visible, err := visibleClipboardRange(clip)
	if err != nil {
		return clip, nil
	}
	cells, ok := shadowSelection(ctx, shadow, area, clip.Rows, clip.Cols)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ok {
		return clip, nil
	}
	all, projected := matchesShadow(clip, cells), matchesShadow(visible, cells)
	if all && !projected {
		clip.VisibleSourceRows, clip.VisibleSourceCols = nil, nil
	} else if projected && !all {
		return visible, nil
	}
	return clip, nil
}

func matchesShadow(candidate *editdoc.PortableRange, cells map[[2]int]editdoc.Value) bool {
	for addr := range cells {
		if addr[0] > candidate.Rows || addr[1] > candidate.Cols {
			return false
		}
	}
	for i, cell := range candidate.Cells {
		v := cell.Value
		if v.Kind == "formula" {
			continue
		} // its cached result may even be empty
		got, ok := cells[[2]int{i/candidate.Cols + 1, i%candidate.Cols + 1}]
		if v.Kind == "blank" || v.Kind == "missing" {
			if ok {
				return false
			}
			continue
		}
		if !ok && v.Kind == "text" && v.Raw == "" {
			continue
		}
		if !ok || got.Kind != v.Kind {
			return false
		}
		if v.Raw == got.Raw {
			continue
		}
		if v.Kind != "number" {
			return false
		}
		a, ea := strconv.ParseFloat(v.Raw, 64)
		b, eb := strconv.ParseFloat(got.Raw, 64)
		if ea != nil || eb != nil || math.Abs(a-b) > 1e-12*max(1, math.Abs(a), math.Abs(b)) {
			return false
		}
	}
	return true
}

func shadowSelection(ctx context.Context, raw []byte, area cellRect, rows, cols int) (map[[2]int]editdoc.Value, bool) {
	var doc struct {
		Workbook []byte `json:"shadowWorkbookBase64"`
		FirstRow int    `json:"rangeRowStart"`
		LastRow  int    `json:"rangeRowEnd"`
		FirstCol int    `json:"rangeColumnStart"`
		LastCol  int    `json:"rangeColumnEnd"`
	}
	doc.FirstRow, doc.LastRow, doc.FirstCol, doc.LastCol = -1, -1, -1, -1
	if json.Unmarshal(bytes.TrimRight(raw, "\x00"), &doc) != nil ||
		doc.FirstRow != area.firstRow-1 || doc.LastRow != area.lastRow-1 ||
		doc.FirstCol != area.firstCol-1 || doc.LastCol != area.lastCol-1 {
		return nil, false
	}
	p, err := openXLSXPackage(bytes.NewReader(doc.Workbook), int64(len(doc.Workbook)))
	if err != nil {
		return nil, false
	}
	var size uint64
	for _, f := range p.files {
		size += f.UncompressedSize64
		if size > 2*formats.EditRequestBytes {
			return nil, false
		}
	}
	p.layout.Native, p.layout.Structured = true, true
	if p.loadWorkbook(ctx) != nil || len(p.layout.Sheets) != 1 {
		return nil, false
	}
	sheet := p.layout.Sheets[0]
	raw, err = p.readPart(ctx, sheet.Part)
	if err != nil {
		return nil, false
	}
	addrs, err := (cellRect{1, 1, rows, cols}).addresses(sheet.Name)
	if err != nil {
		return nil, false
	}
	targets, err := newSheetTargets(sheet, addrs)
	if err != nil {
		return nil, false
	}
	shape, err := p.scanSheet(ctx, raw, sheet, targets, nil, nil, nil)
	if err != nil || shape.Rows > rows || shape.Cols > cols {
		return nil, false
	}
	cells := map[[2]int]editdoc.Value{}
	for _, row := range targets.rows {
		for _, item := range row.cells {
			v := item.original.value
			if v.Kind != "blank" && v.Kind != "missing" {
				cells[[2]int{row.row, item.address.Col}] = v
			}
		}
	}
	return cells, true
}
