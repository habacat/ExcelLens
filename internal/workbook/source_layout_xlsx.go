package workbook

import (
	"math"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// Excel character widths depend on the installed normal font. Seven pixels
// per digit plus the five-pixel cell padding is the standard 11pt approximation.
func sourceColumnPixels(characters float64, digits ...float64) float64 {
	if !(characters > 0 && characters <= 255) {
		return 0
	}
	digit := 7.0
	if len(digits) > 0 && digits[0] > 0 {
		digit = digits[0]
	}
	return math.Floor(characters*digit + 5)
}

// Excel rounds an implicit width from the Normal font up to a whole
// eight-pixel boundary. An explicit col width already includes its margin.
func defaultColumnPixels(base, digit float64) float64 {
	if base <= 0 || base > 255 {
		base = 8
	}
	if digit <= 0 {
		digit = 7
	}
	return 8 * math.Ceil((base*digit+5)/8)
}

func storedColumnPixels(width, digit float64) float64 {
	if !(width > 0 && width <= 255) {
		return 0
	}
	if digit <= 0 {
		digit = 7
	}
	// Stored OOXML/BIFF widths already include Excel's five-pixel margin.
	return math.Floor((256*width + math.Floor(128/digit)) / 256 * digit)
}

func (s *SourceLayout) captureXML(tag, name []byte, row int) {
	number := func(name string) float64 { v, _ := strconv.ParseFloat(string(xmlAttribute(tag, name)), 64); return v }
	switch string(name) {
	case "mergeCell":
		ref := strings.Split(string(xmlAttribute(tag, "ref")), ":")
		if len(ref) != 2 || len(s.Merges) >= 100000 {
			return
		}
		c1, r1, e1 := ParseCellRef(ref[0])
		c2, r2, e2 := ParseCellRef(ref[1])
		if e1 == nil && e2 == nil && r2 >= r1 && c2 >= c1 {
			s.Merges = append(s.Merges, CellMerge{Row: r1, Col: c1, EndRow: r2, EndCol: c2})
		}
	case "sheetFormatPr":
		if v := number("defaultRowHeight") * 4 / 3; validSourceSize(v) {
			s.DefaultRow = v
		}
		if xmlAttribute(tag, "defaultColWidth") == nil {
			s.DefaultCol = defaultColumnPixels(number("baseColWidth"), s.digitWidth)
		}
		if v := sourceColumnPixels(number("defaultColWidth"), s.digitWidth); validSourceSize(v) {
			s.DefaultCol = v
		}
	case "sheetView":
		if s.GridLines == nil {
			v := string(xmlAttribute(tag, "showGridLines"))
			on := v != "0" && !strings.EqualFold(v, "false")
			s.GridLines = &on
		}
	case "col":
		first, last := parsePositiveIntBytes(xmlAttribute(tag, "min")), parsePositiveIntBytes(xmlAttribute(tag, "max"))
		size := storedColumnPixels(number("width"), s.digitWidth)
		if first < 1 || last < first || last > formats.ExcelColumns || !validSourceSize(size) {
			return
		}
		if s.Cols == nil {
			s.Cols = make(map[int]float64)
		}
		for c := first; c <= last; c++ {
			s.Cols[c] = size
		}
	case "row":
		if raw := xmlAttribute(tag, "ht"); len(raw) > 0 {
			v, _ := strconv.ParseFloat(string(raw), 64)
			appendSourceRowSize(&s.Rows, row, v*4/3)
		}
	}
}
