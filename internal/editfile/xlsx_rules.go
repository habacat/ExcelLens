package editfile

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type ruleElement struct {
	rule     editdoc.Rule
	attrs    []xml.Attr
	from, to int
}

type xlsxRuleScan struct {
	sheet                         string
	count                         string
	from, tagEnd, closeAt, insert int
	nodes                         []ruleElement
	node                          ruleElement
	first, second                 bool
}

func areaRef(a editdoc.Area) string {
	first := workbook.ColumnName(a.Start.Col) + strconv.Itoa(a.Start.Row)
	last := workbook.ColumnName(a.EndCol) + strconv.Itoa(a.EndRow)
	if first == last {
		return first
	}
	return first + ":" + last
}

func parseRuleArea(sheet, ref string) (editdoc.Area, error) {
	first, last, ranged := strings.Cut(ref, ":")
	if !ranged {
		last = first
	}
	c1, r1, e1 := workbook.ParseCellRef(first)
	c2, r2, e2 := workbook.ParseCellRef(last)
	a := editdoc.Area{Start: editdoc.Address{Sheet: sheet, Row: r1, Col: c1}, EndRow: r2, EndCol: c2}
	if e1 != nil || e2 != nil || !a.Valid() || first != workbook.ColumnName(c1)+strconv.Itoa(r1) || last != workbook.ColumnName(c2)+strconv.Itoa(r2) {
		return a, fmt.Errorf("%w：规则范围必须是单个矩形", ErrXLSX)
	}
	return a, nil
}

func ruleBool(value string) bool {
	return value == "0" || value == "1" || value == "false" || value == "true"
}

func (s *xlsxRuleScan) visit(e xmlEvent) error {
	local := e.local()
	switch t := e.token.(type) {
	case xml.StartElement:
		switch local {
		case "dataValidations":
			s.from, s.tagEnd = e.from, e.to
			for _, a := range t.Attr {
				if nsDeclaration(a) {
					continue
				}
				if a.Name.Space != "" {
					return errRule
				}
				switch a.Name.Local {
				case "count":
					s.count = a.Value
					if s.count == "" {
						return errRule
					}
				case "disablePrompts":
					if !ruleBool(a.Value) {
						return errRule
					}
				case "xWindow", "yWindow":
					if _, err := strconv.ParseUint(a.Value, 10, 32); err != nil {
						return errRule
					}
				default:
					return errRule
				}
			}
		case "dataValidation":
			if len(s.nodes) >= formats.EditValidationRules {
				return errRule
			}
			area, err := parseRuleArea(s.sheet, attribute(t, "", "sqref"))
			if err != nil {
				return err
			}
			s.node = ruleElement{from: e.from, rule: editdoc.Rule{Area: area, Operator: "between"}}
			s.first, s.second = false, false
			for _, a := range t.Attr {
				if nsDeclaration(a) {
					continue
				}
				if a.Name.Space != "" || len(a.Value) > formats.EditRuleTextBytes {
					return errRule
				}
				switch a.Name.Local {
				case "sqref":
				case "type":
					s.node.rule.Type = a.Value
				case "operator":
					s.node.rule.Operator = a.Value
				case "allowBlank":
					if !ruleBool(a.Value) {
						return errRule
					}
					s.node.rule.AllowBlank = a.Value == "1" || a.Value == "true"
				case "showDropDown", "showInputMessage", "showErrorMessage":
					if !ruleBool(a.Value) {
						return errRule
					}
					s.node.attrs = append(s.node.attrs, a)
				case "errorStyle", "imeMode", "promptTitle", "prompt", "errorTitle", "error":
					s.node.attrs = append(s.node.attrs, a)
				default:
					return errRule
				}
			}
			if s.node.rule.Type == "list" && s.node.rule.Operator == "between" {
				s.node.rule.Operator = ""
			}
		case "formula1", "formula2":
			for _, a := range t.Attr {
				if !nsDeclaration(a) {
					return errRule
				}
			}
			if local == "formula1" {
				if s.first || s.second {
					return errRule
				}
				s.first = true
			} else {
				if !s.first || s.second {
					return errRule
				}
				s.second = true
			}
		case "printOptions", "pageMargins", "pageSetup", "headerFooter":
			if s.insert == 0 {
				s.insert = e.from
			}
		}
	case xml.CharData:
		var dst *string
		switch local {
		case "formula1":
			dst = &s.node.rule.First
		case "formula2":
			dst = &s.node.rule.Second
		}
		if dst != nil {
			if len(*dst)+len(t) > formats.EditRuleTextBytes {
				return errRule
			}
			*dst += string(t)
		} else if (local == "dataValidations" || local == "dataValidation") && len(bytes.TrimSpace(t)) != 0 {
			return errRule
		}
	case xml.EndElement:
		switch local {
		case "dataValidation":
			s.node.to = e.to
			s.node.rule.Reference = s.node.rule.Type == "list" && !strings.HasPrefix(s.node.rule.First, `"`)
			if s.second && s.node.rule.Second == "" {
				return errRule
			}
			if _, err := compileRule(s.node.rule); err != nil {
				return err
			}
			for _, node := range s.nodes {
				if node.rule.Intersects(s.node.rule.Area) {
					return fmt.Errorf("%w：原数据规则范围重叠", ErrXLSX)
				}
			}
			s.nodes = append(s.nodes, s.node)
		case "dataValidations":
			s.closeAt = e.from
			if s.count != "" && s.count != strconv.Itoa(len(s.nodes)) {
				return errRule
			}
		case "worksheet":
			if s.insert == 0 {
				s.insert = e.from
			}
		}
	}
	return nil
}

func writeRuleXML(out *strings.Builder, rule editdoc.Rule, attrs []xml.Attr) {
	fmt.Fprintf(out, `<dataValidation xmlns="%s" type="%s" sqref="%s" allowBlank="%t"`, mainNS, rule.Type, areaRef(rule.Area), rule.AllowBlank)
	if rule.Operator != "" {
		fmt.Fprintf(out, ` operator="%s"`, rule.Operator)
	}
	for _, a := range attrs {
		fmt.Fprintf(out, ` %s="`, a.Name.Local)
		_ = xml.EscapeText(out, []byte(a.Value))
		out.WriteByte('"')
	}
	out.WriteString(`><formula1>`)
	_ = xml.EscapeText(out, []byte(rule.First))
	out.WriteString(`</formula1>`)
	if rule.Second != "" {
		out.WriteString(`<formula2>`)
		_ = xml.EscapeText(out, []byte(rule.Second))
		out.WriteString(`</formula2>`)
	}
	out.WriteString(`</dataValidation>`)
}

func (s xlsxRuleScan) patches(data []byte, changes []editdoc.Rule) ([]xmlPatch, error) {
	var patches []xmlPatch
	var appended strings.Builder
	added := 0
	for _, rule := range changes {
		var replaced bool
		for _, node := range s.nodes {
			if node.rule.Area == rule.Area {
				var text strings.Builder
				writeRuleXML(&text, rule, node.attrs)
				patches = append(patches, xmlPatch{node.from, node.to, text.String()})
				replaced = true
				break
			}
		}
		if !replaced {
			added++
			writeRuleXML(&appended, rule, []xml.Attr{{Name: xml.Name{Local: "showErrorMessage"}, Value: "1"}})
		}
	}
	if added == 0 {
		return patches, nil
	}
	if s.tagEnd == 0 {
		text := fmt.Sprintf(`<dataValidations xmlns="%s" count="%d">%s</dataValidations>`, mainNS, added, appended.String())
		return append(patches, xmlPatch{s.insert, s.insert, text}), nil
	}
	end := s.tagEnd - 1
	if s.count != "" {
		counts, err := removeXMLAttributes(data, s.from, s.tagEnd, "count")
		if err != nil {
			return nil, err
		}
		patches = append(patches, counts...)
	}
	count := fmt.Sprintf(` count="%d"`, len(s.nodes)+added)
	if data[end-1] == '/' {
		name := strings.Fields(string(data[s.from+1 : end-1]))[0]
		return append(patches, xmlPatch{end - 1, s.tagEnd, count + ">" + appended.String() + "</" + name + ">"}), nil
	}
	return append(patches, xmlPatch{end, end, count}, xmlPatch{s.closeAt, s.closeAt, appended.String()}), nil
}
