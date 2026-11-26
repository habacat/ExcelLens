package workbook

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"strconv"

	"excellens/internal/styles"
)

// readSheetTabColor reads only the XML prefix of one worksheet, stopping at
// sheetData. Worksheet tab colors are optional presentation metadata: malformed
// color attributes do not block opening the workbook or change cell values.
func (w *Workbook) readSheetTabColor(f *zip.File) string {
	r, err := f.Open()
	if err != nil {
		return ""
	}
	defer r.Close()
	d := xml.NewDecoder(io.LimitReader(r, 2<<20))
	sheetPrDepth, depth := 0, 0
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if t.Name.Local == "sheetData" {
				return ""
			}
			if t.Name.Local == "sheetPr" {
				sheetPrDepth = depth
			}
			if t.Name.Local != "tabColor" || sheetPrDepth == 0 || depth != sheetPrDepth+1 {
				continue
			}
			var c styles.ColorXML
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "rgb":
					c.RGB = a.Value
				case "theme":
					if n, e := parseUnsignedColorAttr(a.Value); e {
						c.Theme = &n
					}
				case "indexed":
					if n, e := parseUnsignedColorAttr(a.Value); e {
						c.Indexed = &n
					}
				case "tint":
					c.Tint = parseColorTint(a.Value)
				}
			}
			return w.resolveColor(&c, nil)
		case xml.EndElement:
			if depth == sheetPrDepth {
				sheetPrDepth = 0
			}
			if depth > 0 {
				depth--
			}
		}
	}
}

func parseUnsignedColorAttr(v string) (int, bool) {
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}
func parseColorTint(v string) float64 {
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < -1 || n > 1 {
		return 0
	}
	return n
}
