package workbook

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

// Only direct workbook/definedNames/definedName children belong to the name
// directory. Extensions with the same local tag name are never source names.
func catalogueNamesXML(data []byte, original []workbookSheetXML) (Catalogue, error) {
	b := newCatalogueBuilder()
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, rootNS, names := 0, "", false
	ordinal := 0
	root := false
	for {
		token, err := d.Token()
		if err == io.EOF {
			if !root || depth != 0 {
				return b.value, errors.New("名称目录的工作簿XML未完整结束")
			}
			return b.value, nil
		}
		if err != nil {
			return b.value, err
		}
		if err := validateCatalogueXMLToken(token, depth); err != nil {
			return b.value, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if root || t.Name.Local != "workbook" || !catalogueNamespace(t.Name.Space) {
					return b.value, errors.New("名称目录的工作簿根或命名空间无效")
				}
				root, rootNS = true, t.Name.Space
			}
			if depth == 2 {
				names = t.Name.Local == "definedNames" && t.Name.Space == rootNS
			}
			if depth != 3 || !names || t.Name.Local != "definedName" || t.Name.Space != rootNS {
				continue
			}
			ordinal++
			item := CatalogueItem{ID: "name-" + strconv.Itoa(ordinal), Kind: "name", Status: "unresolved"}
			scopeSet := false
			for _, attr := range t.Attr {
				if attr.Name.Space != "" {
					continue
				}
				switch attr.Name.Local {
				case "name":
					item.Name = attr.Value
				case "comment":
					item.Comment = attr.Value
				case "localSheetId":
					item.ScopeID, scopeSet = attr.Value, true
				case "hidden":
					v, valid := sourceBool([]byte(attr.Value))
					item.Hidden = v
					if !valid {
						item.Status, item.Reason = "invalid", "名称隐藏标志无效"
					}
				case "function", "vbProcedure", "xlm":
					v, valid := sourceBool([]byte(attr.Value))
					if !valid {
						item.Status, item.Reason = "invalid", "名称函数或宏标志无效"
					} else if v && item.Status != "invalid" {
						item.Status, item.Reason = "unavailable", "函数或宏名称只列出定义，不执行或定位"
					}
				}
			}
			var content strings.Builder
			oversize := false
			inner := 1
			for inner > 0 {
				value, err := d.Token()
				if err != nil {
					return b.value, err
				}
				if err := validateCatalogueXMLToken(value, depth+inner-1); err != nil {
					return b.value, err
				}
				switch value := value.(type) {
				case xml.StartElement:
					inner++
					item.Status, item.Reason = "invalid", "名称定义中含嵌套结构，不能作为固定地址解释"
				case xml.EndElement:
					inner--
				case xml.CharData:
					if inner == 1 {
						if len(value) > formats.CatalogueDefinitionMax-content.Len() {
							oversize = true
						} else if !oversize {
							content.Write(value)
						}
					}
				}
			}
			depth--
			if oversize {
				b.value.NamesIncomplete = true
				b.notice("部分名称定义超过单项读取容量，原定义未截断为有效名称")
				continue
			}
			item.Definition = content.String()
			if item.Name == "" || item.Name != strings.TrimSpace(item.Name) || strings.TrimSpace(item.Definition) == "" {
				item.Status, item.Reason = "invalid", "名称或定义为空，或名称带有非法前后空白"
			}
			if scopeSet {
				scope, err := strconv.Atoi(item.ScopeID)
				if err != nil || scope < 0 || scope >= len(original) || original[scope].Name == "" {
					item.Status, item.Reason = "invalid", "名称作用域工作表编号无效"
				} else {
					item.Scope = original[scope].Name
				}
			}
			b.add(item)
		case xml.EndElement:
			if depth == 2 {
				names = false
			}
			depth--
		}
	}
}

// ParseTableDefinition reads the source table identity, geometry and column IDs.
func ParseTableDefinition(data []byte, sheet, part, relationship string) (CatalogueItem, error) {
	item := CatalogueItem{Kind: "table", Sheet: sheet, Status: "unresolved", Table: &CatalogueTable{Part: part, RelationID: relationship, HeaderRows: 1}}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, rootNS, container := 0, "", ""
	root, expectedColumns, seenColumns := false, -1, false
	for {
		token, err := d.Token()
		if err == io.EOF {
			if !root || depth != 0 {
				return item, errors.New("表对象XML未完整结束")
			}
			break
		}
		if err != nil {
			return item, err
		}
		if err := validateCatalogueXMLToken(token, depth); err != nil {
			return item, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if root || t.Name.Local != "table" || !catalogueNamespace(t.Name.Space) {
					return item, errors.New("表对象根或命名空间无效")
				}
				root, rootNS = true, t.Name.Space
				fallbackName := ""
				for _, attr := range t.Attr {
					if attr.Name.Space != "" {
						continue
					}
					switch attr.Name.Local {
					case "displayName":
						item.Name = attr.Value
					case "name":
						fallbackName = attr.Value
					case "ref":
						item.Table.Ref, item.Definition = attr.Value, attr.Value
					case "headerRowCount", "totalsRowCount":
						n, err := strconv.Atoi(attr.Value)
						if err != nil || n < 0 || n > 1 {
							return item, errors.New("表对象表头或合计行数量无效")
						}
						if attr.Name.Local == "headerRowCount" {
							item.Table.HeaderRows = n
						} else {
							item.Table.TotalRows = n
						}
					}
				}
				if item.Name == "" {
					item.Name = fallbackName
				}
			}
			if depth == 2 {
				container = ""
				if t.Name.Space == rootNS && t.Name.Local == "tableColumns" {
					if seenColumns {
						return item, errors.New("表对象含重复的列定义容器")
					}
					seenColumns = true
					container = "tableColumns"
					for _, attr := range t.Attr {
						if attr.Name.Space == "" && attr.Name.Local == "count" {
							n, err := strconv.Atoi(attr.Value)
							if err != nil || n < 1 || n > formats.ExcelColumns {
								return item, errors.New("表对象列数量无效")
							}
							expectedColumns = n
						}
					}
				}
			}
			if depth == 3 && container == "tableColumns" && t.Name.Space == rootNS && t.Name.Local == "tableColumn" {
				column := CatalogueColumn{}
				for _, attr := range t.Attr {
					if attr.Name.Space == "" {
						switch attr.Name.Local {
						case "id":
							column.ID = attr.Value
						case "name":
							column.Name = attr.Value
						}
					}
				}
				if column.ID == "" || column.Name == "" || len(column.Name) > formats.CatalogueDefinitionMax || len(item.Table.Columns) >= formats.ExcelColumns {
					return item, errors.New("表对象列定义无效或超过容量")
				}
				item.Table.Columns = append(item.Table.Columns, column)
			}
		case xml.EndElement:
			if depth == 2 {
				container = ""
			}
			depth--
		}
	}
	if item.Name == "" || item.Table.Ref == "" || expectedColumns != len(item.Table.Columns) {
		return item, errors.New("表对象名称、范围或列数量不完整")
	}
	col1, row1, col2, row2, ok := catalogueTableCoordinates(item.Table.Ref)
	if !ok || col2-col1+1 != len(item.Table.Columns) || row2-row1+1 < item.Table.HeaderRows+item.Table.TotalRows {
		return item, errors.New("表对象范围与列或表头/合计数量不一致")
	}
	ids := make(map[uint64]bool)
	for _, column := range item.Table.Columns {
		id, err := strconv.ParseUint(column.ID, 10, 32)
		if err != nil || id == 0 {
			return item, errors.New("表对象列ID不是有效的正整数")
		}
		if ids[id] {
			return item, fmt.Errorf("表对象列ID重复：%s", column.ID)
		}
		ids[id] = true
	}
	return item, nil
}

func validateCatalogueXMLToken(token xml.Token, depth int) error {
	switch value := token.(type) {
	case xml.StartElement:
		if depth >= formats.CatalogueXMLDepth {
			return errors.New("目录XML嵌套层数超过读取容量")
		}
		seen := make(map[xml.Name]bool, len(value.Attr))
		for _, attr := range value.Attr {
			if seen[attr.Name] {
				return errors.New("目录XML含重复属性，不能确定唯一源定义")
			}
			seen[attr.Name] = true
		}
	case xml.CharData:
		if depth == 0 && strings.TrimSpace(string(value)) != "" {
			return errors.New("目录XML根元素外含有无效内容")
		}
	}
	return nil
}

func catalogueTableCoordinates(ref string) (col1, row1, col2, row2 int, ok bool) {
	parts := strings.Split(ref, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return
	}
	var err error
	col1, row1, err = ParseCellRef(parts[0])
	if err != nil {
		return
	}
	col2, row2 = col1, row1
	if len(parts) == 2 {
		col2, row2, err = ParseCellRef(parts[1])
		if err != nil {
			return
		}
	}
	ok = col2 >= col1 && row2 >= row1
	return
}
