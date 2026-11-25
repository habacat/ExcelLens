package workbook

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
)

var ErrPreviewUnavailable = errors.New("preview unavailable")
var errPreviewDone = errors.New("preview complete")

// PreviewRows reads only an initial prefix. It never creates an index, reports
// a complete row count, or enables full-sheet operations. Full indexing still
// validates the entire source and controls when the normal grid is enabled.
func (w *Workbook) PreviewRows(ctx context.Context, meta SheetMeta, limit, columns int) ([]RowData, error) {
	limit = min(max(limit, 1), 120)
	columns = min(max(columns, 1), 40)
	if idx := w.GetCachedIndex(meta.Path); idx != nil {
		return w.ReadRowsContext(ctx, idx, 0, min(limit, idx.RowCount()), 1, columns)
	}
	rows := make([]RowData, 0, limit)
	used := 0
	appendRow := func(row RowData) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, cell := range row.Cells {
			used += len(cell.Raw) + len(cell.Display)
			if used > 2<<20 {
				return errPreviewDone
			}
		}
		rows = append(rows, row)
		if len(rows) >= limit {
			return errPreviewDone
		}
		return nil
	}
	var err error
	if w.source != nil {
		if err := w.checkExternalUnchanged(); err != nil {
			return nil, err
		}
		if w.Format == "xlsb" {
			_, err := w.walkXLSBRows(meta, limit, columns, ctx.Done(), nil, func(number, style int, cells []externalCell) error {
				r := RowData{Number: number, Style: style, Cells: make([]CellData, 0, len(cells))}
				suffix := strconv.Itoa(number)
				for _, cell := range cells {
					r.Cells = append(r.Cells, w.externalCellValue(suffix, cell, true))
				}
				return appendRow(r)
			})
			if errors.Is(err, errPreviewDone) {
				err = nil
			}
			return rows, err
		}
		number := 0
		emitStyled := func(fields []string, styles []int) error {
			number++
			row := RowData{Number: number, Cells: make([]CellData, 0, min(columns, len(fields)))}
			for i, raw := range fields[:min(columns, len(fields))] {
				style := 0
				if i < len(styles) {
					style = styles[i]
				}
				if raw != "" || style != 0 {
					cell := CellData{Col: i + 1, Style: style, Ref: fmt.Sprintf("%s%d", ColumnName(i+1), number), Raw: raw, Display: raw, Type: "inlineStr"}
					if w.Format == "xls-html" {
						converted := htmlExternalCell(raw, style, i+1)
						cell.Raw = converted.Raw
						cell.Display = converted.Raw
						cell.Formula = converted.Formula
						cell.HasFormula = converted.HasFormula
					}
					row.Cells = append(row.Cells, cell)
				}
			}
			return appendRow(row)
		}
		emit := func(fields []string) error { return emitStyled(fields, nil) }
		switch w.Format {
		case "csv", "tsv", "txt":
			err = w.streamTextRows(emit, nil)
		case "xls-html":
			err = w.streamHTMLXLSStyled(meta, emitStyled, nil)
		case "sqlite":
			err = sqliteStreamRows(w.Path, w.source.sqliteNames[meta.Path], emit)
		default:
			return nil, ErrPreviewUnavailable
		}
	} else {
		zr, e := zip.OpenReader(w.Path)
		if e != nil {
			return nil, e
		}
		defer zr.Close()
		if fingerprintZip(zr.File) != w.Fingerprint {
			return nil, errors.New("工作簿已改变，请重新打开")
		}
		var source io.ReadCloser
		for _, file := range zr.File {
			if path.Clean(file.Name) == meta.Path {
				source, err = file.Open()
				break
			}
		}
		if err != nil {
			return nil, err
		}
		if source == nil {
			return nil, ErrPreviewUnavailable
		}
		defer source.Close()
		dec := xml.NewDecoder(io.LimitReader(source, 16<<20))
		number := 0
		anchors := make(map[string]SharedFormulaAnchor)
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			token, e := dec.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			start, ok := token.(xml.StartElement)
			if !ok || start.Name.Local != "row" {
				continue
			}
			var parsed xmlRow
			if err = dec.DecodeElement(&parsed, &start); err != nil {
				return nil, err
			}
			number++
			if parsed.Number > 0 {
				number = parsed.Number
			}
			if number > limit {
				break
			}
			style := 0
			for _, attr := range start.Attr {
				if attr.Name.Local == "s" {
					if n, e := parseNonNegativeInt(attr.Value); e == nil && n < len(w.Styles) {
						style = n
					}
				}
			}
			row := RowData{Number: number, Style: style, Cells: make([]CellData, 0, min(columns, len(parsed.Cells)))}
			previous := 0
			for _, cell := range parsed.Cells {
				col := previous + 1
				if cell.Ref != "" {
					var e error
					col, _, e = ParseCellRef(cell.Ref)
					if e != nil {
						return nil, e
					}
				}
				previous = col
				if col > columns {
					continue
				}
				if cell.Ref == "" {
					cell.Ref = fmt.Sprintf("%s%d", ColumnName(col), number)
				}
				if !cell.StyleDefined {
					cell.Style = style
				}
				if cell.Formula != nil && cell.Formula.Type == "shared" && cell.Formula.Text != "" {
					anchors[cell.Formula.SharedIndex] = SharedFormulaAnchor{Ref: cell.Ref, Formula: cell.Formula.Text}
				}
				resolved := w.resolveCell(cell, col, anchors, true, true)
				if resolved.Ref == "" {
					resolved.Ref = fmt.Sprintf("%s%d", ColumnName(col), number)
				}
				row.Cells = append(row.Cells, resolved)
			}
			err = appendRow(row)
			if err != nil {
				break
			}
		}
	}
	if errors.Is(err, errPreviewDone) {
		err = nil
	}
	return rows, err
}
