package workbook

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// HiddenColumnRange represents source workbook visibility. Columns use
// 1-based OOXML indices, and ranges are inclusive. This is DISPLAY metadata:
// hiding a column never drops its data from searches, exports or formulas.
type HiddenColumnRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

const maxColumnMetadataBytes = 32 << 20

// ReadHiddenColumns reads only the worksheet header, stopping at sheetData.
// OOXML puts cols before sheetData, so even multi-gigabyte sheets need only a
// small sequential read. Later <col> entries override earlier overlaps.
func ReadHiddenColumns(xmlPath string) ([]HiddenColumnRange, error) {
	file, err := os.Open(xmlPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	dec := xml.NewDecoder(io.LimitReader(file, maxColumnMetadataBytes))
	hidden := make([]bool, 16385)
	inCols := false
	for {
		token, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析列显隐元数据失败：%w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "sheetData":
				return compressHiddenColumns(hidden), nil
			case "cols":
				inCols = true
			case "col":
				if !inCols {
					continue
				}
				min, max, hasMin, hasMax, visible := 0, 0, false, false, true
				for _, attr := range element.Attr {
					switch attr.Name.Local {
					case "min":
						min, err = strconv.Atoi(attr.Value)
						hasMin = true
					case "max":
						max, err = strconv.Atoi(attr.Value)
						hasMax = true
					case "hidden":
						visible = !(attr.Value == "1" || strings.EqualFold(attr.Value, "true"))
					}
					if err != nil {
						return nil, fmt.Errorf("列范围含有无效数字：%w", err)
					}
				}
				if !hasMin || !hasMax || min < 1 || max > formats.ExcelColumns || max < min {
					return nil, fmt.Errorf("工作簿列显隐范围无效：%d:%d", min, max)
				}
				for i := min; i <= max; i++ {
					hidden[i] = !visible
				}
			}
		case xml.EndElement:
			if element.Name.Local == "cols" {
				inCols = false
			}
		}
	}
	return compressHiddenColumns(hidden), nil
}

func compressHiddenColumns(hidden []bool) []HiddenColumnRange {
	var ranges []HiddenColumnRange
	for i := 1; i < len(hidden); {
		if !hidden[i] {
			i++
			continue
		}
		start := i
		for i < len(hidden) && hidden[i] {
			i++
		}
		ranges = append(ranges, HiddenColumnRange{Min: start, Max: i - 1})
	}
	return ranges
}

// SourceHiddenColumns returns the *source* visibility, not the user's local
// overrides. External text/SQLite data has no hidden-column metadata.
func (w *Workbook) SourceHiddenColumns(meta SheetMeta, idx *SheetIndex) ([]HiddenColumnRange, error) {
	if w.Format == "xlsb" {
		data, err := w.xlsbSheetMeta(context.Background(), meta)
		if err != nil {
			return nil, err
		}
		hidden := make([]bool, 16385)
		for _, c := range data.Layout.ColStates {
			for at := c.Min; at <= c.Max; at++ {
				hidden[at] = c.Hidden || c.ZeroSize
			}
		}
		return compressHiddenColumns(hidden), nil
	}
	if w.Format == "xls" {
		return w.xlsHiddenColumns(meta)
	}
	if w.source != nil || idx == nil {
		return nil, nil
	}
	return ReadHiddenColumns(idx.XMLPath)
}
