package editfile

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/opc"
)

const workbookPart = "xl/workbook.xml"
const workbookRelsPart = "xl/_rels/workbook.xml.rels"

type sheetCollectionEdit struct {
	p                      *xlsxPackage
	op                     editdoc.Structure
	index                  int
	oldPart, newPart, rid  string
	updates                map[string][]byte
	viewPatches            map[string][]xmlPatch
	parts                  map[string]string
	tableNames, pivotNames map[string]string
	newTitles              []string
	position               int
	activeViews            []int
	addView                bool
	copyState              string
}

func (p *xlsxPackage) editSheetCollection(ctx context.Context, dst io.Writer, op editdoc.Structure) (opc.Receipt, error) {
	index := slices.IndexFunc(p.layout.Sheets, func(s XLSXSheet) bool { return s.Name == op.Sheet })
	if index < 0 {
		return opc.Receipt{}, ErrTarget
	}
	if op.Kind != "deleteSheet" {
		candidate := op
		candidate.Kind = "renameSheet"
		if _, err := newAxisEdit(candidate, p.layout); err != nil {
			return opc.Receipt{}, err
		}
		if p.layout.HasSheet(op.Name) {
			return opc.Receipt{}, errors.New("工作表名称已存在")
		}
	}
	a := sheetCollectionEdit{p: p, op: op, index: index, oldPart: p.layout.Sheets[index].Part, updates: map[string][]byte{}, parts: map[string]string{}, tableNames: map[string]string{}, pivotNames: map[string]string{}}
	var err error
	if op.Kind != "deleteSheet" {
		a.newPart = a.allocate("xl/worksheets/sheet1.xml")
	}
	if op.Kind == "copySheet" {
		if err = a.cloneParts(ctx); err != nil {
			return opc.Receipt{}, err
		}
	}
	if op.Kind == "newSheet" {
		a.updates[a.newPart] = []byte("<worksheet xmlns=\"" + mainNS + "\"><sheetData/></worksheet>")
		a.parts[a.oldPart] = a.newPart
	}
	if op.Kind == "deleteSheet" {
		if p.layout.MacroEnabled {
			raw, e := p.readPart(ctx, a.oldPart)
			if e != nil {
				return opc.Receipt{}, e
			}
			e = walkXML(ctx, raw, func(ev xmlEvent) error {
				if t, ok := ev.token.(xml.StartElement); ok && ev.local() == "sheetPr" && attribute(t, "", "codeName") != "" {
					return errors.New("此工作表包含 VBA 模块，无法删除")
				}
				return nil
			})
			if e != nil {
				return opc.Receipt{}, e
			}
		}
		if err = a.removeParts(ctx); err != nil {
			return opc.Receipt{}, err
		}
	}
	book, err := p.readPart(ctx, workbookPart)
	if err != nil {
		return opc.Receipt{}, err
	}
	if err = a.prepareViews(ctx, book); err != nil {
		return opc.Receipt{}, err
	}
	if err = a.selectViews(ctx); err != nil {
		return opc.Receipt{}, err
	}
	a.updates[workbookPart], err = a.workbook(ctx, book)
	if err != nil {
		return opc.Receipt{}, err
	}
	rels, err := p.readPart(ctx, workbookRelsPart)
	if err != nil {
		return opc.Receipt{}, err
	}
	if op.Kind == "deleteSheet" {
		a.updates[workbookRelsPart], err = removeElements(ctx, rels, func(el xml.StartElement) bool {
			return el.Name.Local == "Relationship" && attribute(el, "", "Id") == a.rid
		})
	} else {
		child := fmt.Sprintf("<Relationship xmlns=\"%s\" Id=\"%s\" Type=\"%s/worksheet\" Target=\"/%s\"/>", packageNS, a.rid, relationNS, a.newPart)
		a.updates[workbookRelsPart], err = appendXML(ctx, rels, "Relationships", child)
	}
	if err != nil {
		return opc.Receipt{}, err
	}
	types, err := p.readPart(ctx, "[Content_Types].xml")
	if err != nil {
		return opc.Receipt{}, err
	}
	if op.Kind == "deleteSheet" {
		types, err = removeElements(ctx, types, func(el xml.StartElement) bool {
			v, ok := a.updates[strings.TrimPrefix(attribute(el, "", "PartName"), "/")]
			return el.Name.Local == "Override" && ok && v == nil
		})
	} else {
		var body strings.Builder
		for _, part := range slices.Sorted(maps.Values(a.parts)) {
			fmt.Fprintf(&body, "<Override xmlns=\"%s\" PartName=\"/%s\" ContentType=\"%s\"/>", contentNS, escapedXML(part), classifyPart(part).mime)
		}
		types, err = appendXML(ctx, types, "Types", body.String())
	}
	if err != nil {
		return opc.Receipt{}, err
	}
	a.updates["[Content_Types].xml"] = types
	if p.files["docProps/app.xml"] != nil {
		data, err := p.readPart(ctx, "docProps/app.xml")
		if err != nil {
			return opc.Receipt{}, err
		}
		next, err := a.appTitles(ctx, data)
		if err != nil {
			return opc.Receipt{}, err
		}
		if !bytes.Equal(data, next) {
			a.updates["docProps/app.xml"] = next
		}
	}
	if op.Kind == "deleteSheet" {
		return p.writeSheetDeletion(ctx, dst, a.updates, a.viewPatches)
	}
	return p.writePatchedValueStructure(ctx, dst, a.updates, a.viewPatches)
}

func appendXML(ctx context.Context, raw []byte, parent, child string) ([]byte, error) {
	var ps []xmlPatch
	var open xmlEvent
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		if e.local() != parent {
			return nil
		}
		switch e.token.(type) {
		case xml.StartElement:
			open = e
		case xml.EndElement:
			if e.from == e.to {
				ps = append(ps, xmlPatch{open.to - 2, open.to, ">" + child + closingTag(raw[open.from:open.to])})
			} else {
				ps = append(ps, xmlPatch{e.from, e.from, child})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, ErrXLSX
	}
	return applyXML(ctx, raw, ps)
}
func removeElements(ctx context.Context, raw []byte, remove func(xml.StartElement) bool) ([]byte, error) {
	var ps []xmlPatch
	depth, start := 0, 0
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		switch el := e.token.(type) {
		case xml.StartElement:
			if depth == 0 && remove(el) {
				depth, start = len(e.stack), e.from
			}
		case xml.EndElement:
			if depth == len(e.stack) {
				ps = append(ps, xmlPatch{start, e.to, ""})
				depth = 0
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applyXML(ctx, raw, ps)
}
func (a *sheetCollectionEdit) allocate(old string) string {
	ext := path.Ext(old)
	base := strings.TrimRight(strings.TrimSuffix(old, ext), "0123456789")
	for i := 1; ; i++ {
		s := base + strconv.Itoa(i) + ext
		if a.p.files[s] == nil {
			if _, used := a.updates[s]; !used {
				a.updates[s] = nil
				return s
			}
		}
	}
}
func (a *sheetCollectionEdit) expr(text string) (string, error) {
	for from, to := range a.tableNames {
		text = calculator.RewriteTableReferences(text, func(s string) string {
			i := strings.IndexByte(s, '[')
			if i > 0 && strings.EqualFold(s[:i], from) {
				if to == "" {
					return "#REF!"
				}
				return to + s[i:]
			}
			return s
		})
	}
	return calculator.RewriteReferences(text, func(sheet, qualifier, ref string) (string, error) {
		if sheet == "" || !strings.EqualFold(sheet, a.op.Sheet) {
			return qualifier + ref, nil
		}
		if a.op.Kind == "deleteSheet" {
			return "#REF!", nil
		}
		return "'" + strings.ReplaceAll(a.op.Name, "'", "''") + "'!" + ref, nil
	})
}
func (a *sheetCollectionEdit) workbook(ctx context.Context, raw []byte) ([]byte, error) {
	var ps []xmlPatch
	var start xmlEvent
	var text strings.Builder
	var sheetIndex, maxID, visible, viewIndex int
	var copies []string
	localNames := map[string]bool{}
	if a.op.Kind == "copySheet" {
		if err := walkXML(ctx, raw, func(e xmlEvent) error {
			if el, ok := e.token.(xml.StartElement); ok && e.local() == "definedName" && attribute(el, "", "localSheetId") == strconv.Itoa(a.index) {
				localNames[strings.ToUpper(attribute(el, "", "name"))] = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	var ridOld string
	drop := a.op.Kind == "deleteSheet"
	for i := 1; ; i++ {
		id := "rId" + strconv.Itoa(i)
		if _, ok := a.p.links[workbookPart][id]; !ok {
			a.rid = id
			break
		}
	}
	for id := range a.p.sheetIDs {
		v, _ := strconv.Atoi(id)
		maxID = max(maxID, v)
	}
	var scope int
	var views xmlEvent
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			switch e.local() {
			case "workbookProtection":
				if isTrue(attribute(t, "", "lockStructure")) {
					return errors.New("工作簿结构受保护")
				}
			case "sheet":
				start = e
				if attribute(t, "", "name") != a.op.Sheet && !slices.Contains([]string{"hidden", "veryHidden"}, attribute(t, "", "state")) {
					visible++
				}
				if sheetIndex == a.index {
					ridOld = attribute(t, relationNS, "id")
				}
			case "bookViews":
				if e.parent() == "workbook" && t.Name.Space == mainNS {
					views = e
				}
			case "sheets":
				if a.addView && views.to == 0 {
					node := fmt.Sprintf(`<bookViews xmlns="%s"><workbookView activeTab="%d"/></bookViews>`, mainNS, a.activeViews[0])
					ps = append(ps, xmlPatch{e.from, e.from, node})
				}
			case "workbookView":
				if viewIndex >= len(a.activeViews) {
					return ErrXLSX
				}
				desired := strconv.Itoa(a.activeViews[viewIndex])
				viewIndex++
				if attribute(t, "", "activeTab") != desired {
					ap, err := setXMLAttribute(raw, e.from, e.to, "activeTab", desired)
					if err != nil {
						return err
					}
					ps = append(ps, ap...)
				}
				if s := attribute(t, "", "firstSheet"); s != "" {
					v, err := strconv.Atoi(s)
					if err != nil || v < 0 {
						return ErrXLSX
					}
					if drop && v > a.index {
						v--
					} else if drop && v == a.index {
						v = min(v, len(a.p.layout.Sheets)-2)
					} else if !drop && v >= a.position {
						v++
					}
					ap, err := setXMLAttribute(raw, e.from, e.to, "firstSheet", strconv.Itoa(max(0, v)))
					if err != nil {
						return err
					}
					ps = append(ps, ap...)
				}
			case "definedName":
				start = e
				text.Reset()
				scope = -1
				if s := attribute(t, "", "localSheetId"); s != "" {
					v, err := strconv.Atoi(s)
					if err != nil || v < 0 {
						return ErrXLSX
					}
					scope = v
				}
			}
		case xml.CharData:
			if e.local() == "definedName" {
				text.Write(t)
			}
		case xml.EndElement:
			if a.addView && e.parent() == "workbook" && t.Name == (xml.Name{Space: mainNS, Local: "bookViews"}) {
				child := fmt.Sprintf(`<workbookView xmlns="%s" activeTab="%d"/>`, mainNS, a.activeViews[0])
				if e.from == e.to {
					ps = append(ps, xmlPatch{views.to - 2, views.to, ">" + child + closingTag(raw[views.from:views.to])})
				} else {
					ps = append(ps, xmlPatch{e.from, e.from, child})
				}
			}
			if e.local() == "sheet" {
				if drop && sheetIndex == a.index {
					ps = append(ps, xmlPatch{start.from, e.to, ""})
				}
				if !drop && sheetIndex+1 == a.position {
					state := ""
					if a.copyState == "hidden" || a.copyState == "veryHidden" {
						state = " state=\"" + a.copyState + "\""
					}
					node := fmt.Sprintf(`<sheet xmlns="%s" xmlns:r="%s" name="%s" sheetId="%d" r:id="%s"%s/>`, mainNS, relationNS, escapedXML(a.op.Name), maxID+1, a.rid, state)
					ps = append(ps, xmlPatch{e.to, e.to, node})
				}
				sheetIndex++
			}
			if e.local() == "definedName" {
				if drop && scope == a.index {
					ps = append(ps, xmlPatch{start.from, e.to, ""})
					return nil
				}
				if drop && scope > a.index || !drop && scope >= a.position {
					n := scope + 1
					if drop {
						n = scope - 1
					}
					ap, err := setXMLAttribute(raw, start.from, start.to, "localSheetId", strconv.Itoa(n))
					if err != nil {
						return err
					}
					ps = append(ps, ap...)
				}
				if drop {
					next, err := a.expr(text.String())
					if err != nil {
						return err
					}
					if next != text.String() {
						ps = append(ps, xmlPatch{start.to, e.from, escapedXML(next)})
					}
				}
				if a.op.Kind == "copySheet" && (scope == a.index || scope < 0) {
					next, err := a.expr(text.String())
					if err != nil {
						return err
					}
					name := attribute(start.token.(xml.StartElement), "", "name")
					if scope < 0 && (localNames[strings.ToUpper(name)] || next == text.String()) {
						return nil
					}
					if !isTrue(attribute(start.token.(xml.StartElement), "", "hidden")) {
						a.newTitles = append(a.newTitles, a.op.Name+"!"+strings.TrimPrefix(name, "_xlnm."))
					}

					tag := raw[start.from:start.to]
					ap, err := setXMLAttribute(tag, 0, len(tag), "localSheetId", strconv.Itoa(a.position))
					if err != nil {
						return err
					}
					tag, err = applyXML(ctx, tag, ap)
					if err != nil {
						return err
					}
					copies = append(copies, string(tag)+escapedXML(next)+string(raw[e.from:e.to]))
				}
			}
			if e.local() == "definedNames" && len(copies) > 0 {
				ps = append(ps, xmlPatch{e.from, e.from, strings.Join(copies, "")})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if drop {
		a.rid = ridOld
		if visible == 0 {
			return nil, errors.New("工作簿至少需要保留一张可见工作表")
		}
	}
	return applyXML(ctx, raw, ps)
}
func (a *sheetCollectionEdit) rewriteSheetReferences(ctx context.Context, raw []byte) ([]byte, error) {
	var ps []xmlPatch
	from := -1
	var data strings.Builder
	err := walkXML(ctx, raw, func(e xmlEvent) error {
		switch el := e.token.(type) {
		case xml.StartElement:
			switch e.local() {
			case "f", "formula", "formula1", "formula2", "calculatedColumnFormula", "totalsRowFormula":
				from = e.to
				data.Reset()
			case "hyperlink":
				s := attribute(el, "", "location")
				if s != "" {
					v, err := a.expr(s)
					if err != nil {
						return err
					}
					if v != s {
						ap, err := setXMLAttribute(raw, e.from, e.to, "location", escapedXML(v))
						if err != nil {
							return err
						}
						ps = append(ps, ap...)
					}
				}
			}
		case xml.CharData:
			if from >= 0 {
				data.Write(el)
			}
		case xml.EndElement:
			if from >= 0 {
				v, err := a.expr(data.String())
				if err != nil {
					return err
				}
				if v != data.String() {
					ps = append(ps, xmlPatch{from, e.from, escapedXML(v)})
				}
				from = -1
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return raw, nil
	}
	return applyXML(ctx, raw, ps)
}
func (a *sheetCollectionEdit) removeParts(ctx context.Context) error {
	// Other pivots using this worksheet as their source must retain their
	// cache. A broken source reference cannot be silently turned into new data.
	for name, s := range a.p.pivotSources {
		if s.sheet == a.op.Sheet {
			for part, cache := range a.p.pivotOutputs {
				if cache == name && a.p.pivotSheets[part] != a.op.Sheet {
					return errors.New("其他工作表的数据透视表仍使用此表作为来源")
				}
			}
		}
	}
	for _, t := range a.p.layout.Tables {
		if t.Sheet == a.op.Sheet {
			a.tableNames[t.Name] = ""
		}
	}
	incoming := map[string]int{}
	for _, links := range a.p.links {
		for _, v := range links {
			incoming[v]++
		}
	}
	var remove func(string)
	remove = func(part string) {
		a.updates[part] = nil
		rels := collectionRels(part)
		if a.p.files[rels] != nil {
			a.updates[rels] = nil
		}
		for _, child := range a.p.links[part] {
			if child == externalBookTarget {
				continue
			}
			incoming[child]--
			if incoming[child] == 0 {
				remove(child)
			}
		}
	}
	remove(a.oldPart)
	for name := range a.p.files {
		if _, gone := a.updates[name]; gone {
			continue
		}
		switch classifyPart(name).role {
		case "sheet", "chart", "table":
			raw, err := a.p.readPart(ctx, name)
			if err != nil {
				return err
			}
			next, err := a.rewriteSheetReferences(ctx, raw)
			if err != nil {
				return err
			}
			if !bytes.Equal(raw, next) {
				a.updates[name] = next
			}
		}
	}
	return nil
}
func collectionRels(part string) string {
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}
