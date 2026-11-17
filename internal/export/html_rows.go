package export

import (
	"excellens/internal/workbook"
	"fmt"
	"html"
	"io"
)

func writeHTMLHead(out io.Writer, wb *workbook.Workbook, s Selection, opts Options) error {
	if _, err := io.WriteString(out, "<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\"><title>"+html.EscapeString(s.Sheet.Name)+" · ExcelLens</title><style>body{font:13px 'Microsoft YaHei',sans-serif;margin:16px}table{border-collapse:collapse}td,th{border:1px solid #d8dfe8;padding:4px 8px;white-space:pre-wrap;min-width:40px}th{background:#f5f7fa;position:sticky;top:0;color:#536173}</style></head><body>"); err != nil {
		return err
	}
	if opts.Styles && wb.RichTextNotice() != "" {
		if _, err := fmt.Fprintf(out, `<p role="status">%s</p>`, html.EscapeString(wb.RichTextNotice())); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(out, "<table><tbody>"); err != nil {
		return err
	}

	return nil
}
func writeHTMLTail(out io.Writer, incomplete int64) error {
	if _, err := io.WriteString(out, "</tbody></table>"); err != nil {
		return err
	}
	if incomplete > 0 {
		if _, err := fmt.Fprintf(out, `<p role="status">%d 个单元格的呈现不完整，原文保留；具体原因见单元格提示。</p>`, incomplete); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(out, "</body></html>"); err != nil {
		return err
	}

	return nil
}
func writeHTMLRow(out io.Writer, wb *workbook.Workbook, s Selection, opts Options, outRow int, row workbook.RowData) (int, error) {
	incomplete := 0

	if _, err := fmt.Fprintf(out, "<tr><th>%d</th>", outRow); err != nil {
		return incomplete, err
	}
	cells := make(map[int]workbook.CellData, len(row.Cells))
	for _, c := range row.Cells {
		cells[c.Col] = c
	}
	for col := s.FirstCol; col <= s.LastCol; col++ {
		c := cells[col]
		v := workbook.VisualStyle{}
		if opts.Styles && c.Style >= 0 && c.Style < len(wb.VisualStyles) {
			v = wb.VisualStyles[c.Style]
		}
		text := textOf(c, opts.Mode)
		if opts.Styles && c.Conditional != nil {
			if c.Conditional.Visual != nil {
				v = *c.Conditional.Visual
			}
			if opts.Mode == "display" && c.Conditional.Display != nil {
				text = *c.Conditional.Display
			}
		}
		rich := c.RichText
		if !opts.Styles {
			rich = nil
		}
		notice := richHTMLNotice(c.Raw, text, rich)
		if opts.Styles {
			notice = conditionalHTMLNotice(notice, c, row.PresentationNotice)
		}
		if notice != "" {
			incomplete++
		}
		useRuns := rich != nil && text == c.Raw && workbook.ValidateRichText(text, rich) == nil && len(rich.Runs) > 0
		valueColor := ""
		if opts.Styles {
			valueColor = c.ValueColor
			if c.Conditional != nil && c.Conditional.ValueColor != nil {
				valueColor = *c.Conditional.ValueColor
			}
		}
		styles := htmlCellStyle(v, valueColor, useRuns)
		font := workbook.RichFont{Vertical: v.FontVertical, Underline: v.UnderlineStyle}
		if opts.Styles && c.Conditional != nil {
			font = overlayRichFont(font, c.Conditional.Style.Font)
		}
		if !useRuns && (font.Underline == "double" || font.Underline == "doubleAccounting") {
			styles += "text-decoration-style:double;"
		}
		if _, err := fmt.Fprintf(out, `<td style="%s" title="%s">`, html.EscapeString(styles), html.EscapeString(notice)); err != nil {
			return incomplete, err
		}
		if useRuns {
			if err := writeRichHTML(out, text, rich, v, font); err != nil {
				return incomplete, err
			}
		} else if font.Vertical == "superscript" || font.Vertical == "subscript" {
			tag := "sup"
			if font.Vertical == "subscript" {
				tag = "sub"
			}
			if _, err := fmt.Fprintf(out, "<%s>%s</%s>", tag, html.EscapeString(text), tag); err != nil {
				return incomplete, err
			}
		} else if _, err := io.WriteString(out, html.EscapeString(text)); err != nil {
			return incomplete, err
		}
		if _, err := io.WriteString(out, `</td>`); err != nil {
			return incomplete, err
		}
	}
	_, err := io.WriteString(out, "</tr>\n")
	return incomplete, err

}
