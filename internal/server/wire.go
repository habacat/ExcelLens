package server

import (
	"strconv"

	"excellens/internal/workbook"
)

// Optional viewport transport v1. Duplicate display text and derivable cell
// references are reconstructed before rendering or using the cell model.
type wireRow struct {
	Number             int        `json:"n"`
	Style              int        `json:"s,omitempty"`
	Cells              []wireCell `json:"c"`
	PresentationNotice string     `json:"p,omitempty"`
}
type wireCell struct {
	Col         int                               `json:"c"`
	Raw         string                            `json:"v"`
	Display     *string                           `json:"d,omitempty"`
	Ref         *string                           `json:"r,omitempty"`
	Type        string                            `json:"t,omitempty"`
	Style       int                               `json:"s,omitempty"`
	Formula     string                            `json:"f,omitempty"`
	Flags       byte                              `json:"k,omitempty"`
	Color       string                            `json:"o,omitempty"`
	Hyperlink   *workbook.CellHyperlink           `json:"h,omitempty"`
	RichText    *workbook.RichText                `json:"x,omitempty"`
	Conditional *workbook.ConditionalPresentation `json:"q,omitempty"`
}

var wireColumns = func() [16385]string {
	var names [16385]string
	for i := 1; i < len(names); i++ {
		names[i] = workbook.ColumnName(i)
	}
	return names
}()

func packViewportRows(rows []workbook.RowData) []wireRow {
	packed := make([]wireRow, len(rows))
	for i, row := range rows {
		out := wireRow{Number: row.Number, Style: row.Style, Cells: make([]wireCell, len(row.Cells)), PresentationNotice: row.PresentationNotice}
		suffix := strconv.Itoa(row.Number)
		for j := range row.Cells {
			cell := &row.Cells[j]
			value := wireCell{Col: cell.Col, Raw: cell.Raw, Type: cell.Type, Style: cell.Style, Formula: cell.Formula, Color: cell.ValueColor, Hyperlink: cell.Hyperlink, RichText: cell.RichText, Conditional: cell.Conditional}
			if cell.Display != cell.Raw {
				value.Display = &cell.Display
			}
			if cell.Col < 1 || cell.Col > 16384 || cell.Ref != wireColumns[cell.Col]+suffix {
				value.Ref = &cell.Ref
			}
			if cell.HasFormula {
				value.Flags |= 1
			}
			if cell.CacheMissing {
				value.Flags |= 2
			}
			if cell.SharedFormula {
				value.Flags |= 4
			}
			if cell.Style == 0 && row.Style != 0 {
				value.Flags |= 8
			}
			if cell.PresentationOnly {
				value.Flags |= 16
			}
			out.Cells[j] = value
		}
		packed[i] = out
	}
	return packed
}
