package workbook

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/formats"
)

func xlsbAreaRefText(a CellMerge) string {
	first := ColumnName(a.Col) + strconv.Itoa(a.Row)
	if a.Row == a.EndRow && a.Col == a.EndCol {
		return first
	}
	return first + ":" + ColumnName(a.EndCol) + strconv.Itoa(a.EndRow)
}

func (w *Workbook) xlsbHyperlinks(ctx context.Context, meta SheetMeta) (*SheetHyperlinkIndex, error) {
	m, err := w.xlsbSheetMeta(ctx, meta)
	if err != nil {
		return nil, err
	}
	relationships := make(map[string]relationship)
	for id, rel := range w.source.xlsb.sheets[meta.Path].relations {
		if strings.HasSuffix(rel.Type, "/hyperlink") {
			relationships[id] = rel
		}
	}
	index := &SheetHyperlinkIndex{single: make(map[int]map[int]CellHyperlink)}
	for _, record := range m.Records {
		if record.Kind != brtHLink {
			continue
		}
		f := xlsbFields{data: record.Data}
		area := f.area()
		raw := rawHyperlink{Ref: xlsbAreaRefText(area), RelID: f.wide(true, 255), Location: f.wide(false, 32767),
			Tooltip: f.wide(false, 32767), Display: f.wide(false, 32767)}
		if f.err != nil {
			return nil, f.err
		}
		if link, ok := w.resolveHyperlink(raw, relationships, meta.Name); ok {
			index.add(area.Row, area.EndRow, area.Col, area.EndCol, link)
		}
	}
	return index, nil
}

func (w *Workbook) xlsbNotes(ctx context.Context, meta SheetMeta) ([]SourceNote, bool, error) {
	sheet := w.source.xlsb.sheets[meta.Path]
	if sheet == nil {
		return nil, false, errors.New("XLSB 批注来源工作表无效")
	}
	target, err := xlsbRelatedPart(meta.Path, sheet.relations, "comments")
	if err != nil {
		return nil, false, err
	}
	threaded := false
	for _, rel := range sheet.relations {
		threaded = threaded || strings.Contains(strings.ToLower(rel.Type), "threadedcomment")
	}
	if target == "" {
		return []SourceNote{}, threaded, nil
	}
	if err := w.CheckSourceUnchanged(); err != nil {
		return nil, false, err
	}
	pkg, parts, err := openXLSBParts(w.Path)
	if err != nil {
		return nil, false, err
	}
	defer pkg.Close()
	var authors []string
	notes := []SourceNote{}
	seen := map[string]bool{}
	var note SourceNote
	var open, ended bool
	err = readXLSBPart(parts[target], maxMetadataPart, func(kind uint16, data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f := xlsbFields{data: data}
		switch kind {
		case brtCommentAuthor:
			authors = append(authors, f.wide(false, 65535))
		case brtBeginComment:
			id := f.u32()
			area := f.area()
			if open || id >= uint32(len(authors)) || area.Row != area.EndRow || area.Col != area.EndCol {
				return errors.New("XLSB 批注坐标或作者无效")
			}
			open = true
			note = SourceNote{Ref: xlsbAreaRefText(area), Author: authors[id]}
			if seen[note.Ref] {
				return errors.New("XLSB 有重复的批注坐标")
			}
			seen[note.Ref] = true
		case brtCommentText:
			if !open {
				return errors.New("XLSB 批注文本缺少坐标")
			}
			note.Text = f.richString().text
		case brtEndComment:
			if !open || len(notes) == formats.EditXLSXRangeLimit {
				return errors.New("XLSB 批注不完整或数量超限")
			}
			notes, open = append(notes, note), false
		case brtEndComments:
			ended = true
		}
		return f.err
	})
	if err != nil {
		return nil, false, err
	}
	if !ended || open {
		return nil, false, errors.New("XLSB 批注部件不完整")
	}
	return notes, threaded, w.CheckSourceUnchanged()
}

func (w *Workbook) xlsbCatalogue(ctx context.Context, tables bool) (Catalogue, error) {
	b := w.source.xlsb
	builder := newCatalogueBuilder()
	for index, n := range b.names {
		if err := ctx.Err(); err != nil {
			return Catalogue{}, err
		}
		name := n.name
		if n.flags&32 != 0 {
			name = "_xlnm." + name
		}
		definition := strings.TrimPrefix(n.definition, "=")
		if name == "_xlnm.Print_Titles" && strings.HasPrefix(definition, "(") && strings.HasSuffix(definition, ")") {
			definition = definition[1 : len(definition)-1]
		}
		item := CatalogueItem{ID: fmt.Sprintf("xlsb-name-%d", index+1), Kind: "name", Name: name,
			Hidden: n.flags&1 != 0, Definition: definition}
		if n.scope < uint32(len(b.sheetNames)) {
			item.Scope = b.sheetNames[n.scope]
			item.ScopeID = strconv.FormatUint(uint64(n.scope), 10)
		} else if n.scope != math.MaxUint32 {
			item.Status, item.Reason = "invalid", "XLSB 名称的作用域无效"
		}
		if !strings.HasPrefix(n.definition, "=") {
			item.Status, item.Reason = "unavailable", "XLSB 名称的二进制表达式无法完整还原"
		}
		if !builder.add(item) {
			break
		}
	}
	if tables {
		for _, meta := range w.Sheets {
			ids := slices.Clone(b.sheets[meta.Path].tables)
			slices.Sort(ids)
			for _, id := range ids {
				t := b.tables[id]
				table := CatalogueTable{Ref: xlsbAreaRefText(t.area), HeaderRows: t.headerRows, TotalRows: t.totalsRows,
					Part: t.part, RelationID: t.relation, SourceID: strconv.FormatUint(uint64(id), 10)}
				for i, column := range t.names {
					table.Columns = append(table.Columns, CatalogueColumn{ID: strconv.FormatUint(uint64(t.columnIDs[i]), 10), Name: column})
				}
				if !builder.add(CatalogueItem{ID: "xlsb-table-" + table.SourceID, Kind: "table", Name: t.display, Sheet: t.sheet,
					Definition: table.Ref, Table: &table, Status: "unverified", Reason: "需要核对实际工作表中的表声明"}) {
					break
				}
			}
		}
	}
	return builder.value, nil
}

func (w *Workbook) confirmXLSBCatalogue(ctx context.Context, sheet SheetMeta) (Catalogue, error) {
	m, err := w.xlsbSheetMeta(ctx, sheet)
	if err != nil {
		return Catalogue{}, err
	}
	var declarations worksheetCatalogueDeclarations
	seen := make(map[string]bool)
	for _, record := range m.Records {
		if record.Kind != brtListPart {
			continue
		}
		f := xlsbFields{data: record.Data}
		id := f.wide(false, 255)
		if f.err != nil || id == "" || seen[id] {
			return Catalogue{}, errors.New("XLSB 工作表表对象声明损坏或重复")
		}
		seen[id] = true
		declarations.IDs = append(declarations.IDs, id)
	}
	list, err := w.xlsbCatalogue(ctx, true)
	if err != nil {
		return Catalogue{}, err
	}
	applyWorksheetDeclarations(&list, sheet, declarations)
	return w.resolveCatalogue(ctx, list)
}
