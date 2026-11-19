package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"excellens/internal/calculator"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

type nameKey struct{ sheet, name string }
type nameTarget struct {
	sheets     []string
	scalar     bool
	areas      []workbook.CatalogueArea
	definition string
}

var nameConstant = regexp.MustCompile(`^(?:"(?:[^"]|"")*"|(?i:TRUE|FALSE)|[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[Ee][+-]?[0-9]+)?)$`)

func (w *workbookExtensions) readDefinedName(e xmlEvent) error {
	switch t := e.token.(type) {
	case xml.StartElement:
		if len(e.stack) == 2 && plainAttributes(t) && !w.seen["definedNames"] {
			w.seen["definedNames"] = true
			return nil
		}
		if len(e.stack) != 3 || t.Name != (xml.Name{Space: mainNS, Local: "definedName"}) || len(w.names) >= formats.CatalogueEntries {
			return fmt.Errorf("%w：定义名称元素或数量无效", ErrXLSX)
		}
		item := workbook.CatalogueItem{Kind: "name", Name: attribute(t, "", "name")}
		if item.Name == "" || utf8.RuneCountInString(item.Name) > 255 || xlsxTextEscapes.MatchString(item.Name) {
			return fmt.Errorf("%w：定义名称标识无效", ErrXLSX)
		}
		builtin := strings.TrimPrefix(strings.ToUpper(item.Name), "_XLNM.")
		if slices.Contains([]string{"AUTO_OPEN", "AUTO_CLOSE", "AUTO_ACTIVATE", "AUTO_DEACTIVATE"}, builtin) {
			return fmt.Errorf("%w：名称 %s 包含自动运行入口", ErrXLSX, item.Name)
		}
		for _, a := range t.Attr {
			if nsDeclaration(a) {
				continue
			}
			if a.Name.Space != "" {
				return fmt.Errorf("%w：名称 %s 含扩展属性", ErrXLSX, item.Name)
			}
			switch a.Name.Local {
			case "name", "comment", "description", "help", "statusBar", "customMenu":
			case "localSheetId":
				i, ok := nonnegativeInt(a.Value)
				if !ok || i >= len(w.p.layout.Sheets) {
					return fmt.Errorf("%w：名称 %s 的工作表作用域无效", ErrXLSX, item.Name)
				}
				item.Scope = w.p.layout.Sheets[i].Name
			case "hidden":
				if !ruleBool(a.Value) {
					return fmt.Errorf("%w：名称 %s 的隐藏属性无效", ErrXLSX, item.Name)
				}
				item.Hidden = isTrue(a.Value)
			case "function", "vbProcedure", "xlm", "workbookParameter", "publishToServer":
				if a.Value == "false" || a.Value == "0" {
					continue
				}
				fallthrough
			default:
				return fmt.Errorf("%w：名称 %s 使用属性 %s", ErrXLSX, item.Name, a.Name.Local)
			}
		}
		w.names = append(w.names, item)
		w.nameText.Reset()
	case xml.CharData:
		if len(e.stack) == 3 {
			if w.nameText.Len()+len(t) > formats.EditFormulaBytes {
				return fmt.Errorf("%w：定义名称原文超出容量", ErrXLSX)
			}
			w.nameText.Write(t)
		} else if len(bytes.TrimSpace(t)) != 0 {
			return ErrXLSX
		}
	case xml.EndElement:
		if len(e.stack) == 3 {
			w.names[len(w.names)-1].Definition = w.nameText.String()
		}
	}
	return nil
}

func (w *workbookExtensions) resolveNames(ctx context.Context) error {
	if len(w.names) == 0 {
		return nil
	}
	sheets := make([]workbook.SheetMeta, len(w.p.layout.Sheets))
	for i, s := range w.p.layout.Sheets {
		sheets[i].Name = s.Name
	}
	list, err := workbook.ResolveXLSXNameRanges(ctx, sheets, w.names)
	if err != nil {
		return err
	}
	if !list.Complete || len(list.Items) != len(w.names) {
		return fmt.Errorf("%w：定义名称数量或展开范围超限", ErrXLSX)
	}
	w.p.names = make(map[nameKey]nameTarget, len(list.Items))
	for _, item := range list.Items {
		key := nameKey{item.Scope, strings.ToUpper(item.Name)}
		if _, exists := w.p.names[key]; exists {
			return fmt.Errorf("%w：定义名称 %s 在同一作用域重复", ErrXLSX, item.Name)
		}
		target := nameTarget{scalar: true, areas: item.Areas, definition: strings.TrimSpace(item.Definition)}
		builtin := key.name == "_XLNM.PRINT_AREA" || key.name == "_XLNM.PRINT_TITLES" || key.name == "_XLNM._FILTERDATABASE"
		if !builtin && calculator.DeletedReference(target.definition, w.p.layout.HasSheet) {
			target.definition = "#REF!"
		} else if item.Status != "ready" && (builtin || !nameConstant.MatchString(target.definition)) {
			if w.p.layout.Native {
				// Retain the definition verbatim. A new formula can only bind a
				// name that the local parser actually resolved.
				continue
			}
			return fmt.Errorf("%w：名称 %s：%s", ErrXLSX, item.Name, item.Reason)
		}
		for _, area := range item.Areas {
			if builtin && (item.Scope == "" || area.Sheet != item.Scope) {
				return fmt.Errorf("%w：名称 %s 的范围未绑定本工作表", ErrXLSX, item.Name)
			}
			switch key.name {
			case "_XLNM._FILTERDATABASE":
				if len(item.Areas) != 1 || !item.Hidden {
					return fmt.Errorf("%w：筛选定义范围或隐藏属性无效", ErrXLSX)
				}
			case "_XLNM.PRINT_TITLES":
				if !(area.Row == 1 && area.EndRow == formats.ExcelRows || area.Col == 1 && area.EndCol == formats.ExcelColumns) {
					return fmt.Errorf("%w：打印标题需要完整的行或列", ErrXLSX)
				}
			}
			target.scalar = target.scalar && len(item.Areas) == 1 && area.Row == area.EndRow && area.Col == area.EndCol
			if !slices.Contains(target.sheets, area.Sheet) {
				target.sheets = append(target.sheets, area.Sheet)
			}
		}
		w.p.names[key] = target
		// Print metadata alone does not change the normal editing capabilities.
		if key.name != "_XLNM.PRINT_AREA" && key.name != "_XLNM.PRINT_TITLES" {
			w.p.layout.Structured = true
		}
	}
	return nil
}
