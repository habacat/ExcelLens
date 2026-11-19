package editfile

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Each contiguous piece remembers its position in the original uncompressed
// part. A patch must lie entirely inside a retained piece. Insertions at the
// boundary between pieces belong immediately before the following source row.
type officePiece struct{ local, source, length int }
type officeWindow struct {
	data      []byte
	pieces    []officePiece
	structure worksheetStructure
	targets   *sheetTargets
}

func (w *officeWindow) rebase(change xmlPatch) (xmlPatch, error) {
	i, _ := slices.BinarySearchFunc(w.pieces, change.from, func(p officePiece, pos int) int {
		if p.local+p.length <= pos {
			return -1
		}
		if p.local > pos {
			return 1
		}
		return 0
	})
	if i == len(w.pieces) || change.from < w.pieces[i].local || change.to > w.pieces[i].local+w.pieces[i].length {
		return change, ErrBinding
	}
	delta := w.pieces[i].source - w.pieces[i].local
	change.from += delta
	change.to += delta
	return change, nil
}

func officeWalk(ctx context.Context, f *zip.File, visit func(xmlEvent) error) error {
	raw, err := f.Open()
	if err != nil {
		return err
	}
	defer raw.Close()
	r := bufio.NewReaderSize(raw, 64<<10)
	skip := 0
	if first, _ := r.Peek(3); bytes.Equal(first, []byte{0xef, 0xbb, 0xbf}) {
		skip, _ = r.Discard(3)
	}
	return walkXMLReader(ctx, r, skip, visit)
}

func (t *sheetTargets) intersects(a cellRect) bool {
	i, _ := slices.BinarySearchFunc(t.rows, a.firstRow, func(r rowTarget, n int) int { return r.row - n })
	for ; i < len(t.rows) && t.rows[i].row <= a.lastRow; i++ {
		cols := t.rows[i].cells
		j, _ := slices.BinarySearchFunc(cols, a.firstCol, func(c cellTarget, n int) int { return c.address.Col - n })
		if j < len(cols) && cols[j].address.Col <= a.lastCol {
			return true
		}
	}
	return false
}

func (p *xlsxPackage) officeWindow(ctx context.Context, sheet XLSXSheet, cells []editdoc.Address) (officeWindow, error) {
	var w officeWindow
	targets, err := newSheetTargets(sheet, cells)
	if err != nil {
		return w, err
	}
	w.targets = targets
	w.structure.window = true
	w.structure.groups = map[int]sharedGroup{}
	f := p.files[sheet.Part]
	cursor, selected, lastRow, next := 0, true, 0, 0
	used := 0
	keep := func(lo, hi int) error {
		if hi <= lo {
			return nil
		}
		if used+hi-lo > formats.EditXLSXPartBytes {
			return fmt.Errorf("%w：单次选区的工作表数据过大", ErrXLSX)
		}
		w.pieces = append(w.pieces, officePiece{used, lo, hi - lo})
		used += hi - lo
		return nil
	}
	var dynamic bool
	var ownerGroup int
	var ownerArea cellRect
	var body strings.Builder
	ownerGroup = -1
	err = officeWalk(ctx, f, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 3 && e.stack[1] == (xml.Name{Space: mainNS, Local: "sheetData"}) {
				n, ok := positiveInt(attribute(t, "", "r"), formats.ExcelRows)
				if t.Name != (xml.Name{Space: mainNS, Local: "row"}) || !ok || n <= lastRow {
					return fmt.Errorf("%w：工作表行坐标", ErrXLSX)
				}
				lastRow = n
				_, needed := targets.spanBlocks[(n-1)/16]
				for next < len(targets.rows) && targets.rows[next].row <= n {
					needed = true
					next++
				}
				if selected && !needed {
					if err := keep(cursor, e.from); err != nil {
						return err
					}
				}
				if !selected && needed {
					cursor = e.from
				}
				selected = needed
			}
			if e.local() == "c" && e.parent() == "row" {
				dynamic = attribute(t, "", "cm") != ""
			}
			if len(e.stack) != 5 || e.stack[1] != (xml.Name{Space: mainNS, Local: "sheetData"}) || e.stack[3] != (xml.Name{Space: mainNS, Local: "c"}) || t.Name != (xml.Name{Space: mainNS, Local: "f"}) {
				return nil
			}
			ref := attribute(t, "", "ref")
			if ref == "" {
				return nil
			}
			kind := attribute(t, "", "t")
			area, ok := parseRect(ref)
			if !ok {
				return fmt.Errorf("%w：公式所属范围", ErrXLSX)
			}
			if !targets.intersects(area) {
				return nil
			}
			switch kind {
			case "array", "dataTable":
				if len(w.structure.arrayRanges) >= formats.EditXLSXRangeLimit {
					return ErrXLSX
				}
				if kind == "array" {
					w.structure.arrayRanges = append(w.structure.arrayRanges, area)
					if dynamic {
						if w.structure.arrayDynamic == nil {
							w.structure.arrayDynamic = map[cellRect]bool{}
						}
						w.structure.arrayDynamic[area] = true
					}
				}
				if kind == "dataTable" {
					w.structure.blocks = append(w.structure.blocks, ownedRange{area: area, reason: "模拟运算表的结果区域"})
				}
			case "shared":
				group, ok := nonnegativeInt(attribute(t, "", "si"))
				if !ok {
					return ErrXLSX
				}
				if _, duplicate := w.structure.groups[group]; duplicate {
					return fmt.Errorf("%w：共享公式组重复", ErrXLSX)
				}
				ownerGroup, ownerArea = group, area
				body.Reset()
			}
		case xml.CharData:
			if ownerGroup >= 0 && e.local() == "f" {
				if body.Len()+len(t) > formats.EditFormulaBytes {
					return ErrXLSX
				}
				body.Write(t)
			}
		case xml.EndElement:
			if ownerGroup >= 0 && e.local() == "f" {
				parsed, err := p.parseFormula(ctx, body.String(), editdoc.Address{Sheet: sheet.Name, Row: ownerArea.firstRow, Col: ownerArea.firstCol}, nil)
				group := sharedGroup{area: ownerArea, formula: parsed}
				if err == nil {
					_, err = parsed.Shift(ownerArea.lastRow-ownerArea.firstRow, ownerArea.lastCol-ownerArea.firstCol)
				}
				if err != nil {
					group.readOnly = "该共享公式组的相对引用需由 Excel 展开"
				}
				w.structure.groups[ownerGroup] = group
				ownerGroup = -1
			}
			if len(e.stack) == 2 && t.Name == (xml.Name{Space: mainNS, Local: "sheetData"}) && !selected {
				selected = true
				cursor = e.from
			}
		}
		return nil
	})
	if err != nil {
		return w, err
	}
	if err = keep(cursor, int(f.UncompressedSize64)); err != nil {
		return w, err
	}
	r, err := f.Open()
	if err != nil {
		return w, err
	}
	defer r.Close()
	w.data = make([]byte, used)
	pos := 0
	for _, piece := range w.pieces {
		if err = ctx.Err(); err != nil {
			return w, err
		}
		if _, err = io.CopyN(io.Discard, r, int64(piece.source-pos)); err != nil {
			return w, err
		}
		if _, err = io.ReadFull(r, w.data[piece.local:piece.local+piece.length]); err != nil {
			return w, err
		}
		pos = piece.source + piece.length
	}
	_, err = p.scanSheet(ctx, w.data, sheet, targets, nil, nil, &w.structure)
	return w, err
}

// Materialize only the members of a shared group whose formula was replaced.
// Offsets come from the source stream, including members outside the window.
func (p *xlsxPackage) officeGroupPatches(ctx context.Context, sheet XLSXSheet, w *officeWindow, edits map[editdoc.Address]editdoc.Value) ([]xmlPatch, error) {
	var changes []xmlPatch
	if len(w.targets.changedGroups) == 0 {
		return nil, nil
	}
	var address editdoc.Address
	var change *xmlPatch
	materialized := 0
	err := officeWalk(ctx, p.files[sheet.Part], func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if len(e.stack) == 4 && e.parent() == "row" && t.Name == (xml.Name{Space: mainNS, Local: "c"}) {
				col, row, err := workbook.ParseCellRef(attribute(t, "", "r"))
				if err != nil {
					return err
				}
				address = editdoc.Address{Sheet: sheet.Name, Row: row, Col: col}
			}
			if len(e.stack) != 5 || e.parent() != "c" || t.Name != (xml.Name{Space: mainNS, Local: "f"}) || attribute(t, "", "t") != "shared" {
				return nil
			}
			group, ok := nonnegativeInt(attribute(t, "", "si"))
			if !ok {
				return ErrXLSX
			}
			if !w.targets.changedGroups[group] || edits[address].Kind != "" {
				return nil
			}
			master, ok := w.structure.groups[group]
			if !ok || master.readOnly != "" || !master.area.contains(address.Row, address.Col) {
				return ErrBinding
			}
			value, err := master.formula.Shift(address.Row-master.area.firstRow, address.Col-master.area.firstCol)
			if err != nil {
				return err
			}
			var buf strings.Builder
			buf.WriteString(`<f xmlns="` + mainNS + `">`)
			_ = xml.EscapeText(&buf, []byte(value[1:]))
			buf.WriteString("</f>")
			materialized += buf.Len()
			if materialized > formats.EditXLSXPartBytes {
				return fmt.Errorf("%w：共享公式展开超限", ErrXLSX)
			}
			change = &xmlPatch{from: e.from, data: buf.String()}
		case xml.EndElement:
			if change != nil && len(e.stack) == 5 && t.Name == (xml.Name{Space: mainNS, Local: "f"}) {
				change.to = e.to
				changes = append(changes, *change)
				change = nil
			}
		}
		return nil
	})
	return changes, err
}
