package workbook

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode"

	"excellens/internal/formats"
	"excellens/internal/rowindex"
)

// Byte ranges refer to the already-verified, unchanged worksheet XML cache.
type SourceXMLRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type sourceMetadataCapture struct {
	depth            int
	root             bool
	container        string
	extDepth         int
	conditionalStart int64
	conditionalBytes int64
	columnStates     []SourceAxisRange
}

func validSourceAxisRanges(ranges []SourceAxisRange, maximum int) bool {
	return rowindex.ValidSourceAxisRanges(ranges, maximum)
}

func validSourceXMLRanges(ranges []SourceXMLRange) bool {
	if len(ranges) > formats.ConditionalBlocks {
		return false
	}
	var previous, total int64
	for _, r := range ranges {
		if r.Start < previous || r.End <= r.Start || r.End-r.Start > formats.ConditionalMetadataBytes {
			return false
		}
		total += r.End - r.Start
		if total > formats.ConditionalMetadataBytes {
			return false
		}
		previous = r.End
	}
	return true
}

func validConditionalNamespaces(s SourceLayout) bool {
	if len(s.ConditionalNamespaces) > formats.ConditionalNamespaces || len(s.ConditionalMainNamespace) > formats.ConditionalNamespaceBytes {
		return false
	}
	for prefix, uri := range s.ConditionalNamespaces {
		if len(prefix) > formats.ConditionalNamespacePrefixBytes || len(uri) > formats.ConditionalNamespaceBytes {
			return false
		}
		for i, r := range prefix {
			if r == '_' || unicode.IsLetter(r) {
				continue
			}
			if i > 0 && (r == '-' || r == '.' || unicode.IsDigit(r) || unicode.IsMark(r)) {
				continue
			}
			return false
		}
	}
	return true
}

func (s *SourceLayout) captureNamespaces(tag []byte) {
	d := xml.NewDecoder(bytes.NewReader(tag))
	token, err := d.Token()
	if err != nil {
		s.ConditionalNotice = "工作表命名空间声明无效"
		return
	}
	root, ok := token.(xml.StartElement)
	if !ok {
		return
	}
	if len(root.Name.Space) > formats.ConditionalNamespaceBytes {
		s.ConditionalNotice = "工作表命名空间声明超过读取容量"
		return
	}
	s.ConditionalMainNamespace = root.Name.Space
	s.ConditionalNamespacesReady = true
	for _, a := range root.Attr {
		prefix := ""
		if a.Name.Space == "xmlns" {
			prefix = a.Name.Local
		} else if a.Name.Local != "xmlns" {
			continue
		}
		if prefix == "xml" {
			continue
		}
		if len(s.ConditionalNamespaces) >= formats.ConditionalNamespaces || len(prefix) > formats.ConditionalNamespacePrefixBytes || len(a.Value) > formats.ConditionalNamespaceBytes {
			s.ConditionalNotice = "工作表命名空间声明超过读取容量"
			continue
		}
		if s.ConditionalNamespaces == nil {
			s.ConditionalNamespaces = make(map[string]string)
		}
		s.ConditionalNamespaces[prefix] = a.Value
	}
}

func (s SourceLayout) conditionalWrapper() string {
	var b strings.Builder
	b.WriteString("<worksheet")
	for prefix, uri := range s.ConditionalNamespaces {
		b.WriteString(" xmlns")
		if prefix != "" {
			b.WriteByte(':')
			b.WriteString(prefix)
		}
		b.WriteString(`="`)
		_ = xml.EscapeText(&b, []byte(uri))
		b.WriteByte('"')
	}
	b.WriteByte('>')
	return b.String()
}

func (s SourceLayout) conditionalTagNamespace(tag []byte) (string, error) {
	d := xml.NewDecoder(io.MultiReader(strings.NewReader(s.conditionalWrapper()), bytes.NewReader(tag)))
	if _, err := d.Token(); err != nil {
		return "", err
	}
	token, err := d.Token()
	if err != nil {
		return "", err
	}
	start, ok := token.(xml.StartElement)
	if !ok {
		return "", io.ErrUnexpectedEOF
	}
	return start.Name.Space, nil
}

func sourceBool(raw []byte) (bool, bool) {
	switch string(raw) {
	case "1", "true":
		return true, true
	case "0", "false":
		return false, true
	default:
		return false, false
	}
}

func sameAxisState(a, b SourceAxisRange) bool {
	return a.Hidden == b.Hidden && a.HiddenSet == b.HiddenSet && a.Level == b.Level && a.Collapsed == b.Collapsed && a.ZeroSize == b.ZeroSize && a.PositiveSize == b.PositiveSize
}

func (s *SourceLayout) axisState(tag []byte, first, last int, column bool) {
	r := SourceAxisRange{Min: first, Max: last}
	if raw := xmlAttribute(tag, "hidden"); len(raw) > 0 {
		var valid bool
		r.Hidden, valid = sourceBool(raw)
		r.HiddenSet = true
		if !valid {
			s.StateNotice = "部分源显隐或大纲属性无效，未完整还原"
			return
		}
	}
	if raw := xmlAttribute(tag, "outlineLevel"); len(raw) > 0 {
		v, err := strconv.Atoi(string(raw))
		if err != nil || v < 0 || v > 7 {
			s.StateNotice = "部分源大纲层级无效，未完整还原"
			return
		}
		r.Level = uint8(v)
	}
	if raw := xmlAttribute(tag, "collapsed"); len(raw) > 0 {
		var valid bool
		r.Collapsed, valid = sourceBool(raw)
		if !valid {
			s.StateNotice = "部分源折叠属性无效，未完整还原"
			return
		}
	}
	sizeName := "ht"
	if column {
		sizeName = "width"
	}
	if raw := xmlAttribute(tag, sizeName); len(raw) > 0 {
		v, err := strconv.ParseFloat(string(raw), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			s.StateNotice = "部分源行列尺寸无效，未完整还原"
		} else {
			r.ZeroSize = v == 0
			r.PositiveSize = v > 0
		}
	}
	if column {
		s.captureColumnAxisState(r, formats.ExcelColumns)
		return
	}
	s.appendRowAxisState(r)
}

func (s *SourceLayout) captureColumnAxisState(r SourceAxisRange, maximum int) {
	if s.capture == nil {
		s.capture = &sourceMetadataCapture{conditionalStart: -1}
	}
	c := s.capture
	if c.columnStates == nil {
		c.columnStates = make([]SourceAxisRange, maximum+1)
	}
	for col := r.Min; col <= r.Max; col++ {
		c.columnStates[col] = r
	}
}

func (s *SourceLayout) appendRowAxisState(r SourceAxisRange) {
	if !r.HiddenSet && r.Level == 0 && !r.Collapsed && !r.ZeroSize && !r.PositiveSize {
		return
	}
	if len(s.RowStates) > 0 {
		last := &s.RowStates[len(s.RowStates)-1]
		if r.Min <= last.Max {
			s.StateNotice = "源行大纲顺序异常，未完整还原"
			return
		}
		if r.Min == last.Max+1 && sameAxisState(*last, r) {
			last.Max = r.Max
			return
		}
	}
	if len(s.RowStates) >= formats.SourceAxisRanges {
		s.StateNotice = "源行大纲区间超过读取容量，未完整还原"
		return
	}
	s.RowStates = append(s.RowStates, r)
}

// Called by the original index pass and the old-layout migration scanner.
// Metadata-only rows are deliberately captured even beyond the used extent.
func (s *SourceLayout) captureMetadataToken(tag []byte, info tagInfo, start, end int64, row int) {
	if !info.valid {
		return
	}
	if s.capture == nil {
		s.capture = &sourceMetadataCapture{conditionalStart: -1}
	}
	c := s.capture
	level := c.depth + 1
	if info.closing {
		level = c.depth
		c.depth--
	} else if !info.selfClosing {
		c.depth++
	}
	if s.catalogue != nil {
		s.catalogue.captureToken(tag, info, start, end, level)
	}
	switch string(info.name) {
	case "worksheet", "sheetData", "cols", "sheetPr", "sheetViews", "sheetView", "mergeCells", "mergeCell", "extLst", "conditionalFormatting", "sheetFormatPr", "outlinePr", "row", "col":
	default:
		return
	}
	if level == 1 && !info.closing && string(info.name) == "worksheet" {
		c.root = true
		s.captureNamespaces(tag)
	}
	if !c.root {
		return
	}
	if level == 2 {
		if info.closing || info.selfClosing {
			c.container = ""
		} else {
			c.container = string(info.name)
		}
	}
	if level == 2 && string(info.name) == "extLst" {
		if info.closing {
			c.extDepth = 0
		} else if !info.selfClosing {
			c.extDepth = level
		}
	}
	if string(info.name) == "conditionalFormatting" {
		if c.extDepth > 0 {
			s.ConditionalNotice = "工作表含扩展命名空间的条件格式，尚未完整还原"
			return
		}
		if level == 2 {
			if !info.closing {
				namespace, err := s.conditionalTagNamespace(tag)
				if err != nil || namespace != s.ConditionalMainNamespace {
					s.ConditionalNotice = "条件格式命名空间无法可靠解释"
					return
				}
				c.conditionalStart = start
				if !info.selfClosing {
					return
				}
			}
			if info.closing && c.conditionalStart < 0 {
				s.ConditionalNotice = "条件格式结束标签未匹配"
			}
			if c.conditionalStart >= 0 {
				size := end - c.conditionalStart
				if len(s.ConditionalBlocks) >= formats.ConditionalBlocks || size <= 0 || size > formats.ConditionalMetadataBytes-c.conditionalBytes {
					s.ConditionalNotice = "条件格式元数据超过读取容量，未完整还原"
				} else {
					s.ConditionalBlocks = append(s.ConditionalBlocks, SourceXMLRange{c.conditionalStart, end})
					c.conditionalBytes += size
				}
				c.conditionalStart = -1
			}
		}
	}
	if info.closing {
		return
	}
	switch string(info.name) {
	case "sheetFormatPr":
		if level == 2 {
			s.captureXML(tag, info.name, 0)
			for _, dimension := range []struct {
				name string
				zero *bool
				size *float64
			}{{"defaultRowHeight", &s.DefaultRowZeroSize, &s.DefaultRow}, {"defaultColWidth", &s.DefaultColZeroSize, &s.DefaultCol}} {
				if raw := xmlAttribute(tag, dimension.name); len(raw) > 0 {
					v, err := strconv.ParseFloat(string(raw), 64)
					if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
						s.StateNotice = "默认行列尺寸无效，未完整还原"
					} else {
						*dimension.zero = v == 0
						if v == 0 {
							*dimension.size = 0
						}
					}
				}
			}
			if v := xmlAttribute(tag, "zeroHeight"); len(v) > 0 {
				valid := false
				s.ZeroHeight, valid = sourceBool(v)
				if !valid {
					s.StateNotice = "默认行显隐属性无效，未完整还原"
				}
			}
		}
	case "sheetView":
		if level == 3 && c.container == "sheetViews" {
			s.captureXML(tag, info.name, 0)
		}
	case "mergeCell":
		if level == 3 && c.container == "mergeCells" {
			s.captureXML(tag, info.name, 0)
		}
	case "outlinePr":
		if level == 3 && c.container == "sheetPr" {
			for _, p := range []struct {
				name   string
				target **bool
			}{{"summaryBelow", &s.SummaryBelow}, {"summaryRight", &s.SummaryRight}} {
				if raw := xmlAttribute(tag, p.name); len(raw) > 0 {
					v, valid := sourceBool(raw)
					if valid {
						*p.target = &v
					} else {
						s.StateNotice = "大纲汇总位置无效，未完整还原"
					}
				}
			}
		}
	case "row":
		if level == 3 && c.container == "sheetData" && row > 0 && row <= formats.ExcelRows {
			s.captureXML(tag, info.name, row)
			s.axisState(tag, row, row, false)
		}
	case "col":
		if level == 3 && c.container == "cols" {
			first, last := parsePositiveIntBytes(xmlAttribute(tag, "min")), parsePositiveIntBytes(xmlAttribute(tag, "max"))
			if first < 1 || last < first || last > formats.ExcelColumns {
				s.StateNotice = "源列大纲范围无效，未完整还原"
				return
			}
			s.axisState(tag, first, last, true)
			s.captureXML(tag, info.name, 0)
		}
	}
}

func (s *SourceLayout) finishMetadataCapture() {
	if s.capture == nil {
		return
	}
	c := s.capture
	if c.conditionalStart >= 0 {
		s.ConditionalNotice = "条件格式XML未完整结束"
	}
	for col := 1; col < len(c.columnStates); col++ {
		r := c.columnStates[col]
		if !r.HiddenSet && r.Level == 0 && !r.Collapsed && !r.ZeroSize && !r.PositiveSize {
			continue
		}
		r.Min, r.Max = col, col
		if len(s.ColStates) > 0 {
			last := &s.ColStates[len(s.ColStates)-1]
			if last.Max+1 == col && sameAxisState(*last, r) {
				last.Max = col
				continue
			}
		}
		s.ColStates = append(s.ColStates, r)
	}
	s.capture = nil
	s.ConditionalNotice = strings.TrimSpace(s.ConditionalNotice)
}
