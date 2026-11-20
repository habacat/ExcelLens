package editfile

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

// A structural operation consumes a materialized revision. All parts that do
// not receive a splice retain their original compressed representation.
func RewriteStructure(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure) (opc.Receipt, error) {
	return rewriteStructureFrom(ctx, dst, src, op, nil)
}

func rewriteStructureFrom(ctx context.Context, dst io.Writer, src *workbook.EditSourceFile, op editdoc.Structure, prior *xlsxPackage) (opc.Receipt, error) {
	p, err := openStructurePackage(ctx, src)
	if err != nil {
		return opc.Receipt{}, err
	}
	switch op.Kind {
	case "pasteRange":
		return p.pastePortableRange(ctx, dst, src, op)
	case "newSheet", "copySheet", "deleteSheet":
		return p.editSheetCollection(ctx, dst, op)
	case "resizeTable":
		return p.resizeTable(ctx, dst, src, op, nil)
	case "cutRange", "copyRange":
		return p.transferRange(ctx, dst, src, op, prior)
	case "fixedArray", "spillFormula", "clearArray":
		return p.rewriteArray(ctx, dst, src, op)
	}
	if op.Kind == "mergeCells" || op.Kind == "unmergeCells" {
		return p.rewriteMerge(ctx, dst, src, op)
	}
	if op.Kind == "refreshPivot" || op.Kind == "configurePivot" || op.Kind == "pivotValueFilter" || op.Kind == "pivotCalculatedItem" || op.Kind == "pivotLabelFilter" || op.Kind == "pivotDateGroup" || op.Kind == "pivotDateFilter" {
		return p.rewritePivot(ctx, dst, src, op)
	}
	if op.Kind == "refreshCharts" {
		return p.refreshCharts(ctx, dst, src, op.Sheet)
	}
	layout := p.layout
	edit, err := newAxisEdit(op, layout)
	if err != nil {
		return opc.Receipt{}, err
	}
	owners := map[string]string{}
	for _, s := range layout.Sheets {
		owners[s.Part] = s.Name
		for _, part := range p.links[s.Part] {
			owners[part] = s.Name
			if classifyPart(part).role == "drawing" {
				for _, child := range p.links[part] {
					owners[child] = s.Name
				}
			}
		}
	}
	return opc.Rewrite(ctx, dst, p.zip, func(f *zip.File) (opc.Change, error) {
		part := f.Name
		role := classifyPart(part).role
		if edit.action != "rename" && part == calcChainPart {
			return opc.Change{Remove: true}, nil
		}
		if edit.action == "delete" && part == "xl/sharedStrings.xml" {
			data, err := p.readPart(ctx, part)
			if err != nil {
				return opc.Change{}, err
			}
			ps, err := sharedCountPatches(ctx, data)
			if err != nil {
				return opc.Change{}, err
			}
			return patchedChange(ctx, data, ps)
		}
		if edit.action != "rename" && p.files[calcChainPart] != nil && (part == "[Content_Types].xml" || part == "xl/_rels/workbook.xml.rels") {
			data, e := p.readPart(ctx, part)
			if e != nil {
				return opc.Change{}, e
			}
			ps, e := calcChainReferencePatch(ctx, data, part)
			if e != nil {
				return opc.Change{}, e
			}
			return patchedChange(ctx, data, ps)
		}
		switch role {
		case "sheet", "book", "app", "table", "pivotCache", "pivotTable", "drawing", "chart", "comments", "notesDrawing":
		default:
			return opc.Change{}, nil
		}
		if f.UncompressedSize64 > formats.EditXLSXPartBytes {
			return opc.Change{}, fmt.Errorf("%s 超过结构编辑的单部件容量", part)
		}
		data, e := p.readPart(ctx, part)
		if e != nil {
			return opc.Change{}, e
		}
		original := data
		if role == "sheet" {
			data, e = p.expandStructureGroups(ctx, data, owners[part], edit)
			if e != nil {
				return opc.Change{}, e
			}
		}
		if role == "sheet" && owners[part] == edit.Sheet && !edit.rows && edit.action != "rename" {
			data, e = edit.filterColumns(ctx, data)
			if e != nil {
				return opc.Change{}, e
			}
		}
		out, e := edit.editPart(ctx, data, role, owners[part], layout)
		if e != nil {
			return opc.Change{}, fmt.Errorf("%s：%w", part, e)
		}
		if bytes.Equal(original, out) {
			return opc.Change{}, nil
		}
		return opc.Change{Write: func(_ io.Reader, w io.Writer) error { _, e := w.Write(out); return e }}, nil
	})
}

func openStructurePackage(ctx context.Context, src *workbook.EditSourceFile) (*xlsxPackage, error) {
	p, err := openXLSXPackage(src, src.Size())
	if err == nil {
		err = p.inspect(ctx)
	}
	if err == nil {
		err = p.inspectGrid(ctx)
	}
	return p, err
}

func patchedChange(ctx context.Context, data []byte, ps []xmlPatch) (opc.Change, error) {
	if len(ps) == 0 {
		return opc.Change{}, nil
	}
	sortPatches(ps)
	return opc.Change{Write: func(_ io.Reader, w io.Writer) error { return writeXMLPatches(ctx, w, data, ps) }}, nil
}
func sortPatches(ps []xmlPatch) {
	slices.SortStableFunc(ps, func(a, b xmlPatch) int {
		if c := cmp.Compare(a.from, b.from); c != 0 {
			return c
		}
		return cmp.Compare(a.to, b.to)
	})
}
func applyXML(ctx context.Context, data []byte, ps []xmlPatch) ([]byte, error) {
	if len(ps) == 0 {
		return data, nil
	}
	sortPatches(ps)
	var out bytes.Buffer
	if err := writeXMLPatches(ctx, &out, data, ps); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (a axisEdit) editPart(ctx context.Context, data []byte, role, sheet string, layout XLSXLayout) ([]byte, error) {
	var ps []xmlPatch
	target := strings.EqualFold(sheet, a.Sheet) && a.action != "rename"
	var suppressDepth, removeStart int
	var formulaCell, clearValue bool
	var cellTag xmlEvent
	var nameScope string
	var anchor, expression string
	var textFrom int
	var currentFormula string
	var semanticName string
	set := func(e xmlEvent, k, v string) error {
		p, err := setXMLAttribute(data, e.from, e.to, k, escapedXML(v))
		ps = append(ps, p...)
		return err
	}
	remove := func(e xmlEvent) { suppressDepth = len(e.stack); removeStart = e.from }
	err := walkXML(ctx, data, func(e xmlEvent) error {
		if suppressDepth != 0 {
			if _, ok := e.token.(xml.EndElement); ok && len(e.stack) == suppressDepth {
				ps = append(ps, xmlPatch{removeStart, e.to, ""})
				suppressDepth = 0
			}
			return nil
		}
		local := e.local()
		switch t := e.token.(type) {
		case xml.StartElement:
			if local == "workbookProtection" && isTrue(attribute(t, "", "lockStructure")) {
				return errors.New("工作簿结构受保护")
			}
			if target && local == "sheetProtection" && isTrue(attribute(t, "", "sheet")) {
				return errors.New("工作表保护限制行列结构操作")
			}
			if role == "book" && local == "sheet" && a.action == "rename" && attribute(t, "", "name") == a.Sheet {
				return set(e, "name", a.Name)
			}
			if local == "definedName" {
				nameScope = ""
				if id := attribute(t, "", "localSheetId"); id != "" {
					i, err := strconv.Atoi(id)
					if err != nil || i < 0 || i >= len(layout.Sheets) {
						return ErrXLSX
					}
					nameScope = layout.Sheets[i].Name
				}
			}
			if role == "sheet" && t.Name.Space == mainNS && local == "c" {
				cellTag = e
				formulaCell = false
			}
			if role == "sheet" && t.Name.Space == mainNS && local == "f" && e.parent() == "c" {
				formulaCell = true
			}
			if role == "sheet" && a.action != "rename" && formulaCell && local == "v" && e.parent() == "c" {
				remove(e)
				clearValue = true
				return nil
			}
			if local == "worksheetSource" {
				owner := attribute(t, "", "sheet")
				if strings.EqualFold(owner, a.Sheet) {
					if a.action == "rename" {
						return set(e, "sheet", a.Name)
					}
					if ref := attribute(t, "", "ref"); ref != "" {
						rect, ok := parseRect(ref)
						if !ok {
							return ErrXLSX
						}
						lo, hi := a.edges(rect)
						if !a.rows && a.changesMembers(lo, hi) ||
							a.rows && (a.action == "delete" && a.point(lo) == 0 || a.action == "move" && a.changesMembers(lo, hi) && a.point(lo) != lo) {
							return errors.New("此操作会改变透视数据源的字段或标题")
						}
						next, err := a.address(ref)
						if err != nil {
							return err
						}
						if next == "#REF!" {
							return errors.New("操作会删除整个透视源")
						}
						if next != ref {
							return set(e, "ref", next)
						}
					}
				}
			}
			if target && (role == "table" && local == "table" || role == "pivotTable" && local == "location" || role == "sheet" && local == "mergeCell" && a.action == "move") {
				if rect, ok := parseRect(attribute(t, "", "ref")); ok {
					if a.changesMembers(a.edges(rect)) {
						switch role {
						case "table":
							return errors.New("操作穿过表格内部，需同步变更字段或计算列")
						case "pivotTable":
							return errors.New("不能移动或删除数据透视表结果的一部分")
						default:
							return errors.New("不能仅移动合并单元格的一部分")
						}
					}
				}
			}
			if role == "drawing" && (local == "twoCellAnchor" || local == "oneCellAnchor") {
				anchor = attribute(t, "", "editAs")
			}
			if target && role == "notesDrawing" && local == "ClientData" {
				return errors.New("移动带 VML 批注或控件锚点的行列尚待核对")
			}
			if target && role == "sheet" && e.parent() == "sheetData" && a.rows && local == "row" {
				n, err := strconv.Atoi(attribute(t, "", "r"))
				if err != nil {
					return ErrXLSX
				}
				next := a.point(n)
				if next == 0 {
					remove(e)
					return nil
				}
				if next > a.limit {
					return ErrTarget
				}
				if next != n {
					if err = set(e, "r", strconv.Itoa(next)); err != nil {
						return err
					}
				}
			}
			if target && local == "row" && !a.rows && attribute(t, "", "spans") != "" {
				p, err := removeXMLAttributes(data, e.from, e.to, "spans")
				if err != nil {
					return err
				}
				ps = append(ps, p...)
			}
			for _, att := range t.Attr {
				if att.Name.Space != "" {
					continue
				}
				k, v := att.Name.Local, att.Value
				if k == "location" && local == "hyperlink" {
					next, err := a.formula(v, sheet)
					if err != nil {
						return err
					}
					if next != v {
						if err = set(e, k, next); err != nil {
							return err
						}
					}
					continue
				}
				if !target || !(k == "r" && local == "c" || k == "ref" && local != "worksheetSource" || k == "sqref" || k == "activeCell" || k == "topLeftCell") {
					continue
				}
				items := strings.Fields(v)
				for i, ref := range items {
					if _, ok := parseRect(ref); !ok {
						return fmt.Errorf("无法变换 %s 的坐标 %s", local, ref)
					}
					next, err := a.address(ref)
					if err != nil {
						return err
					}
					if next == "#REF!" {
						switch local {
						case "c", "hyperlink", "comment":
							remove(e)
							return nil
						case "dimension", "selection", "pane", "sheetView":
							next = "A1"
						default:
							return fmt.Errorf("操作会删除 %s 所占的整个范围", local)
						}
					}
					items[i] = next
				}
				if next := strings.Join(items, " "); next != v {
					if err := set(e, k, next); err != nil {
						return err
					}
				}
			}
			expression = ""
			switch local {
			case "sqref":
				if target && role == "sheet" && t.Name.Space == "http://schemas.microsoft.com/office/excel/2006/main" {
					expression, textFrom, currentFormula, semanticName = local, e.to, "", sheet
				}
			case "definedName", "formula", "formula1", "formula2", "f", "calculatedColumnFormula", "totalsRowFormula":
				expression = local
				textFrom = e.to
				currentFormula = ""
				semanticName = sheet
				if local == "definedName" {
					semanticName = nameScope
				}
			}
		case xml.CharData:
			if expression == local {
				currentFormula += string(t)
				return nil
			}
			if role == "app" && local == "lpstr" && a.action == "rename" && string(t) == a.Sheet {
				ps = append(ps, xmlPatch{e.from, e.to, escapedXML(a.Name)})
			}
			if target && role == "drawing" && anchor != "absolute" && (a.rows && local == "row" || !a.rows && local == "col") {
				n, err := strconv.Atoi(string(t))
				if err != nil {
					return err
				}
				v := a.point(n + 1)
				if v == 0 {
					return errors.New("操作会删除绘图锚点")
				}
				if v != n+1 {
					ps = append(ps, xmlPatch{e.from, e.to, strconv.Itoa(v - 1)})
				}
			}
		case xml.EndElement:
			if expression == local {
				if currentFormula != "" {
					var next string
					var err error
					if local == "sqref" {
						items := strings.Fields(currentFormula)
						for i, ref := range items {
							items[i], err = a.address(ref)
							if err != nil {
								return err
							}
							if items[i] == "#REF!" {
								return errors.New("操作会删除扩展规则所占的区域")
							}
						}
						next = strings.Join(items, " ")
					} else {
						next, err = a.formula(currentFormula, semanticName)
					}
					if err != nil {
						return err
					}
					if next != currentFormula {
						ps = append(ps, xmlPatch{textFrom, e.from, escapedXML(next)})
					}
				}
				expression = ""
			}
			if local == "c" && role == "sheet" && clearValue {
				p, err := removeXMLAttributes(data, cellTag.from, cellTag.to, "t", "vm")
				if err != nil {
					return err
				}
				ps = append(ps, p...)
				clearValue = false
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if role == "book" && a.action != "rename" {
		p, err := calcPropertiesPatches(ctx, data)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p...)
	}
	out, err := applyXML(ctx, data, ps)
	if err != nil {
		return nil, err
	}
	if role == "sheet" && target {
		if !a.rows {
			out, err = a.columnFormats(ctx, out)
			if err != nil {
				return nil, err
			}
		}
		if a.action == "insert" {
			out, err = a.insertedFormats(ctx, out)
			if err != nil {
				return nil, err
			}
		}
	}
	if role == "sheet" && target && a.action == "move" {
		return reorderGrid(ctx, out, a.rows)
	}
	return out, nil
}

type gridSpan struct{ key, from, to int }

func reorderGrid(ctx context.Context, data []byte, rows bool) ([]byte, error) {
	child, parent := "c", "row"
	if rows {
		child, parent = "row", "sheetData"
	}
	var spans []gridSpan
	var ps []xmlPatch
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if e.local() == parent {
				spans = spans[:0]
			}
			if e.local() == child && e.parent() == parent {
				ref := attribute(t, "", "r")
				key, err := strconv.Atoi(ref)
				if !rows {
					key, _, err = workbook.ParseCellRef(ref)
				}
				if err != nil {
					return err
				}
				spans = append(spans, gridSpan{key: key, from: e.from})
			}
		case xml.EndElement:
			if e.local() == child && e.parent() == parent {
				spans[len(spans)-1].to = e.to
			}
			if e.local() == parent && len(spans) > 1 && !slices.IsSortedFunc(spans, func(x, y gridSpan) int { return cmp.Compare(x.key, y.key) }) {
				orig := slices.Clone(spans)
				slices.SortFunc(spans, func(x, y gridSpan) int { return cmp.Compare(x.key, y.key) })
				for i, s := range spans {
					if s.key != orig[i].key {
						ps = append(ps, xmlPatch{orig[i].from, orig[i].to, string(data[s.from:s.to])})
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, data, ps)
}
