package workbook

import (
	"context"
	"strings"

	"excellens/internal/formats"
)

// Read names only: never load table parts, worksheet values or another sheet's
// index. Existing bounded catalogue parsing/resolution preserves source scope.
func (w *Workbook) readPrintNames(ctx context.Context, meta SheetMeta, reader *catalogueReader, b *printMetadataBuilder) error {
	var names Catalogue
	var err error
	if w.Format == "xlsb" {
		names, err = w.xlsbCatalogue(ctx, false)
		if err != nil {
			return err
		}
	} else if w.source != nil {
		if w.source.xlsCatalogue == nil {
			b.value.NamesComplete = false
			b.notice("源适配器未提供完整打印名称，不能推断默认打印范围")
			return nil
		}
		names = *w.source.xlsCatalogue
	} else {
		var data []byte
		data, err = reader.part("xl/workbook.xml")
		if err == nil {
			var sheets []workbookSheetXML
			sheets, err = catalogueSheetsXML(data)
			if err == nil {
				names, err = catalogueNamesXML(data, sheets)
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			b.value.NamesComplete = false
			b.notice("打印名称未完整读取，不能推断默认整表范围：" + err.Error())
			return nil
		}
	}
	b.value.NamesComplete = names.Complete && !names.NamesIncomplete
	if !b.value.NamesComplete {
		b.notice("源名称列表不完整，不能据空列表推断没有打印区域或重复标题")
	}
	names, err = w.resolveCatalogue(ctx, names)
	if err != nil {
		return err
	}
	counts := make(map[string]int)
	source, _ := formats.Lookup(w.Format)
	for _, name := range names.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		kind := ""
		if strings.EqualFold(name.Name, "_xlnm.Print_Area") {
			kind = "area"
		} else if strings.EqualFold(name.Name, "_xlnm.Print_Titles") {
			kind = "titles"
		}
		if kind == "" || name.Scope != "" && !strings.EqualFold(name.Scope, meta.Name) {
			continue
		}
		item := SourcePrintName{Kind: kind, ID: name.ID, Scope: name.Scope, ScopeID: name.ScopeID, Definition: name.Definition, TokenHex: name.TokenHex, Status: name.Status, Reason: name.Reason, Areas: name.Areas}
		if name.Scope == "" {
			item.Status, item.Reason = "unresolved", "打印名称缺少可靠的工作表作用域，不能默认应用到当前表"
		}
		for _, area := range item.Areas {
			if !strings.EqualFold(area.Sheet, meta.Name) {
				item.Status, item.Reason = "unresolved", "打印名称引用了其他工作表，未改作当前表范围"
			}
			if kind == "titles" && !(area.Col == 1 && area.EndCol == source.MaxColumns || area.Row == 1 && area.EndRow == source.MaxRows) {
				item.Status, item.Reason = "unresolved", "打印重复标题不是完整行或列，原定义保留"
			}
		}
		counts[kind]++
		if err := b.reserve(item); err != nil {
			return err
		}
		b.value.Names = append(b.value.Names, item)
		if item.Status != "ready" {
			b.notice(item.Reason)
		}
	}
	for kind, count := range counts {
		if count > 1 {
			b.notice("同一工作表含重复的打印名称，不能任选一个生效：" + kind)
			for at := range b.value.Names {
				if b.value.Names[at].Kind == kind {
					b.value.Names[at].Status, b.value.Names[at].Reason = "unresolved", "同一工作表的打印名称重复"
				}
			}
		}
	}
	return nil
}
