package server

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/workbook"
)

// Preserve a read-only tab's typed values without starting an edit document.
func (a *App) readerClipboard(ctx context.Context, wb *workbook.Workbook, meta workbook.SheetMeta, idx *workbook.SheetIndex, rows []int, col, cols int, byRow map[int]workbook.RowData) (*portableClipboard, error) {
	f, err := workbook.OpenEditSource(wb.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = wb.CheckEditSource(ctx, f); err != nil {
		return nil, err
	}
	layout, err := wb.SourceLayoutContext(ctx, meta, idx)
	if err != nil {
		return nil, err
	}
	values := make([]editdoc.Value, len(rows)*cols)
	for i, r := range rows {
		row := byRow[r]
		slice := values[i*cols : (i+1)*cols]
		for j := range slice {
			slice[j] = editdoc.Value{Kind: "blank", Style: strconv.Itoa(row.Style)}
		}
		for _, c := range row.Cells {
			j := c.Col - col
			if j < 0 || j >= cols || c.PresentationOnly {
				continue
			}
			v := editdoc.Value{Style: strconv.Itoa(c.Style), Raw: c.Raw}
			switch {
			case c.HasFormula:
				if c.Formula == "" {
					return nil, errors.New("来源公式缺少表达式")
				}
				v.Kind, v.Raw = "formula", "="+strings.TrimPrefix(c.Formula, "=")
			case c.Type == "s" || c.Type == "inlineStr" || c.Type == "str" || c.Type == "text":
				v.Kind = "text"
			case c.Type == "e":
				v.Kind = "error"
			case c.Type == "b":
				v.Kind = "boolean"
				if v.Raw == "1" || strings.EqualFold(v.Raw, "TRUE") {
					v.Raw = "TRUE"
				} else if v.Raw == "0" || strings.EqualFold(v.Raw, "FALSE") {
					v.Raw = "FALSE"
				}
			case c.Type == "d":
				v.Kind = "date"
			case v.Raw == "":
				v.Kind = "blank"
			case c.Type == "" || c.Type == "n":
				v.Kind = "number"
			default:
				return nil, editfile.ErrTarget
			}
			slice[j] = v
		}
	}
	clip, err := editfile.ReaderClipboardRange(ctx, f, editdoc.Address{Sheet: meta.Name, Row: rows[0], Col: col}, rows, cols, values, layout.Merges)
	if err != nil {
		return nil, err
	}
	if err = f.Check(); err != nil {
		return nil, err
	}
	clip.Origin = sourceBookID(wb)
	return a.signedRange(clip)
}
