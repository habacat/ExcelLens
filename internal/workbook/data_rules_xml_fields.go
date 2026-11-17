package workbook

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"strings"

	"excellens/internal/formats"
)

const (
	x14DataRulesNamespace    = "http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"
	xmDataRulesNamespace     = "http://schemas.microsoft.com/office/excel/2006/main"
	x12DataRuleListNamespace = "http://schemas.microsoft.com/office/spreadsheetml/2011/1/ac"
	x14DataRulesExtensionURI = "{CCE6A557-97BC-4B89-ADB6-D9C93CAAB3DF}"
)

func sourceDataRuleAttributes(start xml.StartElement) SourceDataRule {
	rule := SourceDataRule{Namespace: start.Name.Space, Type: "none", Operator: "between", ErrorStyle: "stop", IMEMode: "noControl", Status: "described", Attributes: make(map[string]string)}
	for _, attr := range start.Attr {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		rule.Attributes[dataRuleAttributeKey(attr.Name.Space, attr.Name.Local)] = attr.Value
		if attr.Name.Space != "" {
			if attr.Name.Space != "http://schemas.microsoft.com/office/spreadsheetml/2014/revision" || attr.Name.Local != "uid" {
				dataRuleIssue(&rule, "unresolved", "数据规则含扩展属性，源定义已保留")
			}
			continue
		}
		var flag *bool
		switch attr.Name.Local {
		case "type":
			rule.Type = attr.Value
		case "operator":
			rule.Operator = attr.Value
		case "errorStyle":
			rule.ErrorStyle = attr.Value
		case "imeMode":
			rule.IMEMode = attr.Value
		case "sqref":
			rule.SqRef = attr.Value
		case "promptTitle":
			rule.PromptTitle = attr.Value
		case "prompt":
			rule.Prompt = attr.Value
		case "errorTitle":
			rule.ErrorTitle = attr.Value
		case "error":
			rule.Error = attr.Value
		case "allowBlank":
			flag = &rule.AllowBlank
		case "showDropDown":
			flag = &rule.SuppressDropDown
		case "showInputMessage":
			flag = &rule.ShowInputMessage
		case "showErrorMessage":
			flag = &rule.ShowErrorMessage
		default:
			dataRuleIssue(&rule, "unresolved", "数据规则含尚未解释的属性，源定义已保留")
		}
		if flag != nil {
			value, valid := sourceBool([]byte(attr.Value))
			if !valid {
				dataRuleIssue(&rule, "invalid", "数据规则开关值无效，未猜测默认值")
			}
			*flag = value
		}
	}
	if start.Name.Space == x14DataRulesNamespace && rule.Type == "list" {
		rule.SuppressDropDown = !rule.SuppressDropDown
	}
	return rule
}

func dataRuleText(ctx context.Context, d *xml.Decoder, start xml.StartElement, depth int) (string, bool, error) {
	valid := true
	for _, attr := range start.Attr {
		if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
			valid = false
		}
	}
	var text strings.Builder
	for inner := 1; inner > 0; {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		token, err := d.Token()
		if err != nil {
			return "", false, err
		}
		if err := dataRuleXMLToken(token, depth+inner-1); err != nil {
			return "", false, err
		}
		switch node := token.(type) {
		case xml.StartElement:
			inner++
			valid = false
		case xml.EndElement:
			inner--
		case xml.CharData:
			if inner == 1 {
				if len(node) > formats.DataRuleTextBytes-text.Len() {
					return "", false, errors.New("数据规则条件或区域文字超过读取容量")
				}
				text.Write(node)
			}
		}
	}
	return text.String(), valid, nil
}

func dataRuleExtendedFormula(ctx context.Context, d *xml.Decoder, start xml.StartElement) (string, bool, error) {
	valid, count := true, 0
	for _, attr := range start.Attr {
		if attr.Name.Space != "xmlns" && attr.Name.Local != "xmlns" {
			valid = false
		}
	}
	text := ""
	for {
		if err := ctx.Err(); err != nil {
			return "", false, err
		}
		token, err := d.Token()
		if err != nil {
			return "", false, err
		}
		if err := dataRuleXMLToken(token, 6); err != nil {
			return "", false, err
		}
		switch node := token.(type) {
		case xml.StartElement:
			if node.Name.Local == "f" && node.Name.Space == xmDataRulesNamespace {
				value, complete, err := dataRuleText(ctx, d, node, 7)
				if err != nil {
					return "", false, err
				}
				count++
				if count == 1 {
					text = value
				}
				valid = valid && complete
			} else {
				valid = false
				if err := skipDataRuleElement(ctx, d, 7); err != nil {
					return "", false, err
				}
			}
		case xml.EndElement:
			if node.Name == start.Name {
				return text, valid && count == 1, nil
			}
		case xml.CharData:
			if len(bytes.TrimSpace(node)) > 0 {
				valid = false
			}
		}
	}
}

func readSourceDataRule(ctx context.Context, d *xml.Decoder, start xml.StartElement, maxRows, maxCols int) (SourceDataRule, error) {
	rule := sourceDataRuleAttributes(start)
	extended := start.Name.Space == x14DataRulesNamespace
	depth := 3
	if extended {
		depth = 5
	}
	var fields [4]string
	var seen, invalid [4]bool
	for {
		if err := ctx.Err(); err != nil {
			return SourceDataRule{}, err
		}
		token, err := d.Token()
		if err != nil {
			return SourceDataRule{}, err
		}
		if err := dataRuleXMLToken(token, depth); err != nil {
			return SourceDataRule{}, err
		}
		if end, ok := token.(xml.EndElement); ok && end.Name == start.Name {
			break
		}
		switch node := token.(type) {
		case xml.StartElement:
			field := -1
			if node.Name.Space == start.Name.Space {
				if node.Name.Local == "formula1" {
					field = 0
				}
				if node.Name.Local == "formula2" {
					field = 1
				}
			}
			if extended && node.Name.Space == xmDataRulesNamespace && node.Name.Local == "sqref" {
				field = 2
			}
			if node.Name.Space == x12DataRuleListNamespace && node.Name.Local == "list" {
				field = 3
			}
			if field < 0 {
				dataRuleIssue(&rule, "unresolved", "数据规则含未知子结构，仅保留原定义")
				if err := skipDataRuleElement(ctx, d, depth+1); err != nil {
					return SourceDataRule{}, err
				}
				continue
			}
			var value string
			var valid bool
			if extended && field < 2 {
				value, valid, err = dataRuleExtendedFormula(ctx, d, node)
			} else {
				value, valid, err = dataRuleText(ctx, d, node, depth+1)
			}
			if err != nil {
				return SourceDataRule{}, err
			}
			if seen[field] {
				invalid[field] = true
			} else {
				fields[field] = value
			}
			seen[field] = true
			invalid[field] = invalid[field] || !valid
		case xml.CharData:
			if len(bytes.TrimSpace(node)) > 0 {
				dataRuleIssue(&rule, "invalid", "数据规则定义含未知文字")
			}
		}
	}
	for at := 0; at < 2; at++ {
		if invalid[at] {
			fields[at] = ""
			dataRuleIssue(&rule, "invalid", "数据规则条件重复或含未知结构，未拼接为有效公式")
		}
	}
	rule.Formula1, rule.Formula2 = fields[0], fields[1]
	if extended {
		rule.SqRef = fields[2]
		if _, found := rule.Attributes["sqref"]; found {
			invalid[2] = true
		}
	}
	if invalid[2] {
		dataRuleIssue(&rule, "invalid", "数据规则适用范围重复或结构无效")
	} else {
		var err error
		rule.Ranges, err = dataRuleRanges(rule.SqRef, maxRows, maxCols)
		if err != nil {
			dataRuleIssue(&rule, "invalid", err.Error())
		}
	}
	if seen[3] {
		rule.ListDefinition = fields[3]
		if invalid[3] {
			rule.ListDefinition = ""
			dataRuleIssue(&rule, "invalid", "扩展列表定义重复或结构无效")
		} else {
			rule.ListSource = "extended"
			dataRuleIssue(&rule, "unresolved", "扩展列表保留原始定义，尚未确认列表项或替代关系")
		}
	} else if rule.Type == "list" && !invalid[0] {
		if values, valid := dataRuleInlineList(rule.Formula1, ","); valid {
			rule.InlineList, rule.ListSource = values, "inline"
		} else {
			rule.ListSource = "expression"
			dataRuleIssue(&rule, "unresolved", "列表来自引用或无法完整展开的表达式，仅保留定义，未读取候选值")
		}
	}
	if extended {
		if rule.Type == "none" && seen[0] {
			dataRuleIssue(&rule, "invalid", "扩展任意值规则含不应存在的第一个条件")
		}
		if (rule.Type == "none" || rule.Type == "list" || rule.Type == "custom" || rule.Operator != "between" && rule.Operator != "notBetween") && seen[1] {
			dataRuleIssue(&rule, "invalid", "扩展规则含不应存在的第二个条件")
		}
	}
	validateDataRule(&rule)
	return rule, nil
}
