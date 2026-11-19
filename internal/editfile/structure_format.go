package editfile

import (
	"context"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Column ranges encode physical width, style and visibility independently of
// worksheet cells. Expand only the 16,384 column descriptors, never the grid.
func (a axisEdit) columnFormats(ctx context.Context, data []byte) ([]byte, error) {
	type column struct{ from, to int }
	cols := make([]*column, formats.ExcelColumns+1)
	var open, end int
	var current *column
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == "cols" && e.parent() == "worksheet" {
				open = e.to
			}
			if e.local() == "col" && e.parent() == "cols" {
				lo, err := strconv.Atoi(attribute(t, "", "min"))
				if err != nil {
					return err
				}
				hi, err := strconv.Atoi(attribute(t, "", "max"))
				if err != nil || lo < 1 || hi > formats.ExcelColumns || hi < lo {
					return ErrXLSX
				}
				current = &column{from: e.from}
				for i := lo; i <= hi; i++ {
					cols[i] = current
				}
			}
		case xml.EndElement:
			if e.local() == "col" && e.parent() == "cols" {
				current.to = e.to
			}
			if e.local() == "cols" && e.parent() == "worksheet" {
				end = e.from
			}
		}
		return nil
	})
	if err != nil || open == 0 || end <= open {
		return data, err
	}
	next := make([]*column, len(cols))
	for i := 1; i < len(cols); i++ {
		n := a.point(i)
		if n > 0 && n < len(next) {
			next[n] = cols[i]
		}
	}
	if a.action == "insert" {
		for i := a.At; i < a.At+a.Count; i++ {
			next[i] = cols[a.At-1]
		}
	}
	var out strings.Builder
	for first := 1; first < len(next); {
		c := next[first]
		last := first
		for last+1 < len(next) && next[last+1] == c {
			last++
		}
		if c != nil {
			raw := data[c.from:c.to]
			// Locate the end of the actual start token, including quoted attributes.
			d := xml.NewDecoder(strings.NewReader(string(raw)))
			if _, err := d.RawToken(); err != nil {
				return nil, err
			}
			tagEnd := int(d.InputOffset())
			p1, err := setXMLAttribute(raw, 0, tagEnd, "min", strconv.Itoa(first))
			if err != nil {
				return nil, err
			}
			p2, err := setXMLAttribute(raw, 0, tagEnd, "max", strconv.Itoa(last))
			if err != nil {
				return nil, err
			}
			b, err := applyXML(ctx, raw, append(p1, p2...))
			if err != nil {
				return nil, err
			}
			out.Write(b)
		}
		first = last + 1
	}
	return applyXML(ctx, data, []xmlPatch{{open, end, out.String()}})
}

func (a axisEdit) insertedFormats(ctx context.Context, data []byte) ([]byte, error) {
	if a.action != "insert" || a.At == 1 {
		return data, nil
	}
	var patches []xmlPatch
	var row, firstCell, nextRow int
	var rowStyle, colStyle string
	type cellStyle struct {
		col   int
		style string
	}
	var styles []cellStyle
	total := 0
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == "row" && e.parent() == "sheetData" {
				row, _ = strconv.Atoi(attribute(t, "", "r"))
				firstCell = 0
				colStyle = ""
				if a.rows && row == a.At-1 {
					var attributes strings.Builder
					for _, k := range []string{"s", "customFormat", "ht", "customHeight", "outlineLevel", "thickTop", "thickBot"} {
						if v := attribute(t, "", k); v != "" {
							fmt.Fprintf(&attributes, ` %s="%s"`, k, escapedXML(v))
						}
					}
					rowStyle = attributes.String()
				}
				if a.rows && row >= a.At && nextRow == 0 {
					nextRow = e.from
				}
			}
			if e.local() == "c" && e.parent() == "row" {
				col, _, err := workbook.ParseCellRef(attribute(t, "", "r"))
				if err != nil {
					return err
				}
				style := attribute(t, "", "s")
				if a.rows && row == a.At-1 && style != "" {
					styles = append(styles, cellStyle{col, style})
				}
				if !a.rows && col == a.At-1 {
					colStyle = style
				}
				if !a.rows && col >= a.At && firstCell == 0 {
					firstCell = e.from
				}
			}
		case xml.EndElement:
			if e.local() == "sheetData" && a.rows && nextRow == 0 {
				nextRow = e.from
			}
			if e.local() == "row" && !a.rows && colStyle != "" {
				if firstCell == 0 {
					firstCell = e.from
				}
				if a.Count > formats.EditCalcSourceCells-total {
					return ErrCalcCapacity
				}
				var b strings.Builder
				for i := 0; i < a.Count; i++ {
					cellXML(&b, editdoc.Address{Row: row, Col: a.At + i}, colStyle, editdoc.Value{Kind: "blank"})
				}
				total += a.Count
				patches = append(patches, xmlPatch{firstCell, firstCell, b.String()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if a.rows && nextRow > 0 && (rowStyle != "" || len(styles) > 0) {
		if a.Count > formats.EditCalcSourceCells/max(1, len(styles)) {
			return nil, ErrCalcCapacity
		}
		var b strings.Builder
		for i := 0; i < a.Count; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			row := a.At + i
			fmt.Fprintf(&b, `<row xmlns="%s" r="%d"%s>`, mainNS, row, rowStyle)
			for _, c := range styles {
				cellXML(&b, editdoc.Address{Row: row, Col: c.col}, c.style, editdoc.Value{Kind: "blank"})
			}
			b.WriteString("</row>")
		}
		patches = append(patches, xmlPatch{nextRow, nextRow, b.String()})
	}
	return applyXML(ctx, data, patches)
}
