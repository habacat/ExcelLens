package export

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// XLSX output is a new, macro-free, read-only snapshot assembled with the Go
// standard library. Original style records and theme retain their source IDs;
// proven conditional differences append deduplicated ordinary style records.
func writeXLSX(ctx context.Context, dst io.Writer, wb *workbook.Workbook, selections []Selection, opts Options, report Tick) error {
	prepared, err := prepareXLSX(ctx, wb, selections, opts)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(dst)
	if err := writeXLSXMetadata(zw, wb, selections, prepared.theme); err != nil {
		return err
	}
	for i, s := range selections {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := zw.Create(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1))
		if err != nil {
			return err
		}
		if err := writeXLSXSheet(ctx, f, wb, s, opts, report, prepared.palette); err != nil {
			return err
		}
	}
	styles, err := zw.Create("xl/styles.xml")
	if err != nil {
		return err
	}
	if err := prepared.palette.write(styles); err != nil {
		return err
	}
	if err := writeXLSXTheme(zw, prepared.theme); err != nil {
		return err
	}
	return zw.Close()
}

type xlsxWriteContext struct {
	palette *xlsxStylePalette
	theme   []byte
}

func prepareXLSX(ctx context.Context, wb *workbook.Workbook, selections []Selection, opts Options) (*xlsxWriteContext, error) {
	if opts.Styles && wb.RichTextNotice() != "" {
		return nil, fmt.Errorf("富文本样式不可用，未生成带格式快照：%s；可关闭原始格式后导出", wb.RichTextNotice())
	}
	for _, s := range selections {
		if opts.Mode == "formula" && s.Compact {
			return nil, errors.New("筛选或选区抽取会改变单元格位置，不能安全原样保留公式；请选择缓存值导出")
		}
	}
	styleRaw := ""
	var theme []byte
	if capability, ok := formats.Lookup(wb.Format); opts.Styles && ok && capability.ID == "xlsx" {
		var err error
		styleRaw, theme, err = loadXLSXStyleSource(ctx, wb)
		if err != nil {
			return nil, fmt.Errorf("读取原始格式失败：%w", err)
		}
	}
	if styleRaw == "" {
		styleRaw = nativeStylesXML(wb, opts.Styles)
	}

	return &xlsxWriteContext{palette: newXLSXStylePalette(ctx, styleRaw), theme: theme}, nil
}
func writeXLSXMetadata(zw *zip.Writer, wb *workbook.Workbook, selections []Selection, theme []byte) error {
	hasTheme := len(theme) > 0
	var types strings.Builder
	types.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	if hasTheme {
		types.WriteString(`<Override PartName="/xl/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`)
	}
	for i := range selections {
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1)
	}
	types.WriteString(`</Types>`)
	if err := zipString(zw, "[Content_Types].xml", types.String()); err != nil {
		return err
	}
	if err := zipString(zw, "_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`); err != nil {
		return err
	}
	var book strings.Builder
	book.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">`)
	if wb.Date1904 {
		book.WriteString(`<workbookPr date1904="1"/>`)
	}
	book.WriteString(`<sheets>`)
	for i, s := range selections {
		fmt.Fprintf(&book, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlAttr(s.Sheet.Name), i+1, i+1)
	}
	book.WriteString(`</sheets><calcPr calcMode="manual" fullCalcOnLoad="0"/></workbook>`)
	if err := zipString(zw, "xl/workbook.xml", book.String()); err != nil {
		return err
	}
	var rels strings.Builder
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := range selections {
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
	}
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, len(selections)+1)
	if hasTheme {
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>`, len(selections)+2)
	}
	rels.WriteString(`</Relationships>`)
	if err := zipString(zw, "xl/_rels/workbook.xml.rels", rels.String()); err != nil {
		return err
	}

	return nil
}
func writeXLSXTheme(zw *zip.Writer, theme []byte) error {
	if len(theme) == 0 {
		return nil
	}
	f, err := zw.Create("xl/theme/theme1.xml")
	if err != nil {
		return err
	}
	_, err = f.Write(theme)
	return err
}
func zipString(zw *zip.Writer, name, content string) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, content)
	return err
}
func xmlAttr(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(strings.ReplaceAll(b.String(), `"`, `&quot;`), `'`, `&apos;`)
}
func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
func writeXLSXSheet(ctx context.Context, dst io.Writer, wb *workbook.Workbook, s Selection, opts Options, report Tick, palette *xlsxStylePalette) error {
	out := bufio.NewWriterSize(dst, 256<<10)
	if _, err := io.WriteString(out, `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`); err != nil {
		return err
	}
	err := forEachRowWithOptions(ctx, wb, s, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: opts.Styles, ConditionalFormats: opts.Styles}, func(_ int, outRow int, row workbook.RowData) error {
		if row.PresentationNotice != "" {
			return fmt.Errorf("%s!%d：%s；可关闭原始格式后导出", s.Sheet.Name, row.Number, row.PresentationNotice)
		}
		if len(row.Cells) == 0 {
			return nil
		} // sparse rows in XLSX do not need explicit blanks
		if _, err := fmt.Fprintf(out, `<row r="%d">`, outRow); err != nil {
			return err
		}
		for _, c := range row.Cells {
			col := c.Col
			if s.Compact {
				col -= s.FirstCol - 1
			}
			if col < 1 || col > formats.ExcelColumns {
				continue
			}
			ref := workbook.ColumnName(col) + strconv.Itoa(outRow)
			style := ""
			styleID := c.Style
			derived := false
			if opts.Styles && c.Conditional != nil {
				var err error
				styleID, err = palette.styleID(c)
				if err != nil {
					return fmt.Errorf("%s!%s：%w；可关闭原始格式后导出", s.Sheet.Name, c.Ref, err)
				}
				derived = c.Conditional.Visual != nil
			}
			if opts.Styles && styleID > 0 && (derived || styleID < max(len(wb.Styles), len(wb.VisualStyles))) {
				style = ` s="` + strconv.Itoa(styleID) + `"`
			}
			value := c.Raw
			if opts.Mode == "display" {
				value = c.Display
				if opts.Styles && c.Conditional != nil && c.Conditional.Display != nil {
					value = *c.Conditional.Display
				}
			}
			if opts.Styles && c.RichText != nil {
				if err := validateRichSnapshot(c, value); err != nil {
					return fmt.Errorf("%s!%s：%w；可关闭原始格式后导出", s.Sheet.Name, c.Ref, err)
				}
				if _, err := fmt.Fprintf(out, `<c r="%s"%s t="inlineStr"><is>`, ref, style); err != nil {
					return err
				}
				font := workbook.RichFont{}
				if c.Conditional != nil {
					font = c.Conditional.Style.Font
				}
				if err := writeRichXLSX(out, value, c.RichText, font); err != nil {
					return err
				}
				if _, err := io.WriteString(out, `</is></c>`); err != nil {
					return err
				}
				continue
			}
			if opts.Mode == "formula" && c.HasFormula && !c.ArrayFormulaMember {
				if !strings.HasPrefix(c.Formula, "=") || strings.TrimSpace(strings.TrimPrefix(c.Formula, "=")) == "" {
					return fmt.Errorf("%s!%s 的公式原文无法可靠还原，请改用缓存值导出", s.Sheet.Name, c.Ref)
				}
				f := strings.TrimPrefix(c.Formula, "=")
				formulaAttrs := ""
				if array := c.ArrayFormula; array != nil {
					if row.Number != outRow || col != c.Col || array.EndRow > s.LastRow || array.EndRow > formats.ExcelRows || array.EndCol > s.LastCol || s.Filtered {
						return fmt.Errorf("%s!%s 的数组范围超出导出区域，请改用缓存值导出", s.Sheet.Name, c.Ref)
					}
					formulaAttrs = fmt.Sprintf(` t="array" ref="%s:%s%d"`, ref, workbook.ColumnName(array.EndCol), array.EndRow)
				}
				kind := ""
				cache := c.Raw
				if c.Type == "e" {
					kind = ` t="e"`
				} else if c.Type == "b" {
					kind = ` t="b"`
					cache = "0"
					if strings.EqualFold(c.Raw, "TRUE") || c.Raw == "1" {
						cache = "1"
					}
				} else if c.Type == "str" || c.Type == "s" || c.Type == "inlineStr" {
					kind = ` t="str"`
				}
				if _, err := fmt.Fprintf(out, `<c r="%s"%s%s><f%s>%s</f>`, ref, style, kind, formulaAttrs, xmlText(f)); err != nil {
					return err
				}
				if !c.CacheMissing {
					if _, err := io.WriteString(out, "<v>"+xmlText(cache)+"</v>"); err != nil {
						return err
					}
				}
				if _, err := io.WriteString(out, `</c>`); err != nil {
					return err
				}
				continue
			}
			if s.Index.SourceFormat == "ods" && c.HasFormula && c.CacheMissing && opts.Mode == "values" {
				return fmt.Errorf("%s!%s 缺少 ODS 公式缓存，无法导出数值。可用 JSONL 保留缺缓存状态", s.Sheet.Name, c.Ref)
			}
			if value == "" && !c.HasFormula && c.SourceType != "string" { // preserve intentionally styled blank cells
				if style != "" {
					if _, err := fmt.Fprintf(out, `<c r="%s"%s/>`, ref, style); err != nil {
						return err
					}
				}
				continue
			}
			if opts.Mode != "display" {
				if c.Type == "d" {
					if _, err := fmt.Fprintf(out, `<c r="%s"%s t="d"><v>%s</v></c>`, ref, style, xmlText(value)); err != nil {
						return err
					}
					continue
				}
				if c.Type == "b" {
					v := "0"
					if strings.EqualFold(value, "TRUE") || value == "1" {
						v = "1"
					}
					if _, err := fmt.Fprintf(out, `<c r="%s"%s t="b"><v>%s</v></c>`, ref, style, v); err != nil {
						return err
					}
					continue
				}
				if c.Type == "e" {
					if _, err := fmt.Fprintf(out, `<c r="%s"%s t="e"><v>%s</v></c>`, ref, style, xmlText(value)); err != nil {
						return err
					}
					continue
				}
				if (c.Type == "" || c.Type == "n") && isNumeric(value) {
					if _, err := fmt.Fprintf(out, `<c r="%s"%s><v>%s</v></c>`, ref, style, xmlText(value)); err != nil {
						return err
					}
					continue
				}
			}
			if _, err := fmt.Fprintf(out, `<c r="%s"%s t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, xmlText(value)); err != nil {
				return err
			}
		}
		_, err := io.WriteString(out, `</row>`)
		return err
	}, report)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(out, `</sheetData>`); err != nil {
		return err
	}
	if opts.Styles && !s.Filtered {
		var refs []string
		layout, err := wb.SourceLayoutContext(ctx, s.Sheet, s.Index)
		if err != nil {
			return err
		}
		for _, m := range layout.Merges {
			if m.Row < s.FirstRow || m.EndRow > s.LastRow || m.Col < s.FirstCol || m.EndCol > s.LastCol {
				continue
			}
			r1, r2, c1, c2 := m.Row, m.EndRow, m.Col, m.EndCol
			if s.Compact {
				r1 -= s.FirstRow - 1
				r2 -= s.FirstRow - 1
				c1 -= s.FirstCol - 1
				c2 -= s.FirstCol - 1
			}
			refs = append(refs, workbook.ColumnName(c1)+strconv.Itoa(r1)+":"+workbook.ColumnName(c2)+strconv.Itoa(r2))
		}
		if len(refs) > 0 {
			if _, err := fmt.Fprintf(out, `<mergeCells count="%d">`, len(refs)); err != nil {
				return err
			}
			for _, ref := range refs {
				if _, err := fmt.Fprintf(out, `<mergeCell ref="%s"/>`, ref); err != nil {
					return err
				}
			}
			if _, err := io.WriteString(out, `</mergeCells>`); err != nil {
				return err
			}
		}
	}
	if _, err := io.WriteString(out, `</worksheet>`); err != nil {
		return err
	}
	return out.Flush()
}
