package editfile

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"excellens/internal/editdoc"
	"excellens/internal/formats"
	"excellens/internal/opc"
	"excellens/internal/workbook"
)

type noteParts struct {
	p       *xlsxPackage
	updates map[string][]byte
	types   []byte
}

func (n *noteParts) allocate(pattern string) string {
	for i := 1; ; i++ {
		name := fmt.Sprintf(pattern, i)
		if n.p.files[name] == nil && n.updates[name] == nil {
			return name
		}
	}
}
func (n *noteParts) addType(ctx context.Context, name string) error {
	raw, err := appendXML(ctx, n.types, "Types", `<Override xmlns="`+contentNS+`" PartName="/`+name+`" ContentType="`+classifyPart(name).mime+`"/>`)
	if err == nil {
		n.types = raw
	}
	return err
}

func (p *xlsxPackage) writeReviewNotes(ctx context.Context, dst io.Writer, items []editdoc.ReviewNote) (opc.Receipt, error) {
	if len(items) == 0 || len(items) > formats.ReviewNotesPerEdit {
		return opc.Receipt{}, errors.New("所选标记数量超限或为空")
	}
	groups := map[string][]editdoc.ReviewNote{}
	for _, v := range items {
		col, row, err := workbook.ParseCellRef(v.Ref)
		if err != nil || row > formats.ExcelRows || v.Ref != workbook.ColumnName(col)+strconv.Itoa(row) || !p.layout.HasSheet(v.Sheet) || strings.TrimSpace(v.Text) == "" || utf8.RuneCountInString(v.Text) > formats.ReviewNoteChars {
			return opc.Receipt{}, errors.New("批注工作表、坐标或文字无效")
		}
		groups[v.Sheet] = append(groups[v.Sheet], v)
	}
	n := noteParts{p: p, updates: map[string][]byte{}}
	var err error
	n.types, err = p.readPart(ctx, "[Content_Types].xml")
	if err != nil {
		return opc.Receipt{}, err
	}
	for _, sheet := range p.layout.Sheets {
		if group := groups[sheet.Name]; len(group) > 0 {
			if err = n.add(ctx, sheet, group); err != nil {
				return opc.Receipt{}, err
			}
		}
	}
	original, err := p.readPart(ctx, "[Content_Types].xml")
	if err != nil {
		return opc.Receipt{}, err
	}
	if !bytes.Equal(original, n.types) {
		n.updates["[Content_Types].xml"] = n.types
	}
	add := map[string][]byte{}
	for name, value := range n.updates {
		if p.files[name] == nil {
			add[name] = value
		}
	}
	return opc.RewriteWithAdditions(ctx, dst, p.zip, add, func(f *zip.File) (opc.Change, error) {
		raw, ok := n.updates[f.Name]
		if !ok {
			return opc.Change{}, nil
		}
		return opc.Change{Write: func(_ io.Reader, w io.Writer) error { _, err := w.Write(raw); return err }}, nil
	})
}

func (n *noteParts) add(ctx context.Context, sheet XLSXSheet, items []editdoc.ReviewNote) error {
	p := n.p
	if sheet.ReadOnly != "" {
		return fmt.Errorf("%s：%s", sheet.Name, sheet.ReadOnly)
	}
	var comments, drawing string
	used := map[string]bool{}
	for id, name := range p.links[sheet.Part] {
		used[id] = true
		switch classifyPart(name).role {
		case "comments":
			comments = name
		case "notesDrawing":
			drawing = name
		}
	}
	rels := path.Join(path.Dir(sheet.Part), "_rels", path.Base(sheet.Part)+".rels")
	relXML := []byte(`<Relationships xmlns="` + packageNS + `"></Relationships>`)
	var err error
	if p.files[rels] != nil {
		relXML, err = p.readPart(ctx, rels)
		if err != nil {
			return err
		}
	}
	link := func(name string) (string, error) {
		id := ""
		for i := 1; ; i++ {
			id = "rId" + strconv.Itoa(i)
			if !used[id] {
				break
			}
		}
		used[id] = true
		relXML, err = appendXML(ctx, relXML, "Relationships", `<Relationship xmlns="`+packageNS+`" Id="`+id+`" Type="`+classifyPart(name).relation+`" Target="/`+name+`"/>`)
		if err == nil {
			n.updates[rels] = relXML
			err = n.addType(ctx, name)
		}
		return id, err
	}
	var raw, boxes []byte
	if comments == "" {
		comments = n.allocate("xl/comments%d.xml")
		if _, err = link(comments); err != nil {
			return err
		}
		raw = []byte(`<comments xmlns="` + mainNS + `"><authors></authors><commentList></commentList></comments>`)
	} else {
		raw, err = p.readPart(ctx, comments)
		if err != nil {
			return err
		}
	}
	authors, old, err := workbook.DecodeNotes(ctx, raw)
	if err != nil {
		return err
	}
	const author = "ExcelLens"
	authorID := slices.Index(authors, author)
	if authorID < 0 {
		authorID = len(authors)
		raw, err = appendXML(ctx, raw, "authors", `<author xmlns="`+mainNS+`">ExcelLens</author>`)
		if err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, note := range old {
		seen[note.Ref] = true
	}
	drawingID := ""
	if drawing == "" {
		if len(old) > 0 {
			return errors.New("已有批注缺少绘图，无法保留原批注框")
		}
		drawing = n.allocate("xl/drawings/notes%d.vml")
		drawingID, err = link(drawing)
		if err != nil {
			return err
		}
		boxes = []byte(`<xml xmlns:v="` + vmlNS + `" xmlns:o="` + vmlOfficeNS + `" xmlns:x="` + vmlExcelNS + `"><v:shapetype id="_x0000_t202" coordsize="21600,21600" o:spt="202" path="m,l,21600r21600,l21600,xe"><v:stroke joinstyle="miter"/><v:path gradientshapeok="t" o:connecttype="rect"/></v:shapetype></xml>`)
	} else {
		boxes, err = p.readPart(ctx, drawing)
		if err != nil {
			return err
		}
	}
	shapeType := ""
	ids := map[string]bool{}
	err = walkXML(ctx, boxes, func(e xmlEvent) error {
		v, ok := e.token.(xml.StartElement)
		if !ok {
			return nil
		}
		if v.Name.Space == vmlNS {
			if v.Name.Local == "shapetype" && attribute(v, vmlOfficeNS, "spt") == "202" {
				shapeType = attribute(v, "", "id")
			}
			if v.Name.Local == "shape" {
				ids[attribute(v, "", "id")] = true
				ids[attribute(v, vmlOfficeNS, "spid")] = true
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if shapeType == "" {
		return errors.New("传统绘图缺少可复用的批注框类型")
	}
	var texts, shapes strings.Builder
	seq := 1025
	for _, v := range items {
		if seen[v.Ref] {
			return fmt.Errorf("%s!%s 已有批注，整批未写入", sheet.Name, v.Ref)
		}
		seen[v.Ref] = true
		fmt.Fprintf(&texts, `<comment xmlns="%s" ref="%s" authorId="%d"><text><t xml:space="preserve">%s</t></text></comment>`, mainNS, v.Ref, authorID, escapedXML(v.Text))
		id := ""
		for {
			id = "_x0000_s" + strconv.Itoa(seq)
			seq++
			if !ids[id] {
				break
			}
		}
		col, row, _ := workbook.ParseCellRef(v.Ref)
		fmt.Fprintf(&shapes, `<v:shape xmlns:v="%s" xmlns:x="%s" id="%s" type="#%s" style="position:absolute;width:108pt;height:60pt;visibility:hidden" fillcolor="#ffffe1"><v:textbox/><x:ClientData ObjectType="Note"><x:MoveWithCells/><x:SizeWithCells/><x:Anchor>%d, 3, %d, 2, %d, 32, %d, 8</x:Anchor><x:AutoFill>False</x:AutoFill><x:Row>%d</x:Row><x:Column>%d</x:Column></x:ClientData></v:shape>`, vmlNS, vmlExcelNS, id, escapedXML(shapeType), min(col, formats.ExcelColumns-2), max(0, row-2), min(col+2, formats.ExcelColumns-1), min(row+3, formats.ExcelRows-1), row-1, col-1)
	}
	n.updates[comments], err = appendXML(ctx, raw, "commentList", texts.String())
	if err != nil {
		return err
	}
	n.updates[drawing], err = appendXML(ctx, boxes, "xml", shapes.String())
	if err != nil {
		return err
	}
	// Insert at the schema location before later drawing/controls/table parts.
	sheetXML, err := p.readPart(ctx, sheet.Part)
	if err != nil {
		return err
	}
	insert := -1
	err = walkXML(ctx, sheetXML, func(e xmlEvent) error {
		switch v := e.token.(type) {
		case xml.StartElement:
			if e.parent() == "worksheet" && e.local() == "sheetProtection" && isTrue(attribute(v, "", "sheet")) && isTrue(attribute(v, "", "objects")) {
				return errors.New("工作表保护禁止修改批注对象")
			}
			if e.parent() == "worksheet" && insert < 0 && slices.Contains([]string{"legacyDrawingHF", "picture", "oleObjects", "controls", "webPublishItems", "tableParts", "extLst"}, e.local()) {
				insert = e.from
			}
		case xml.EndElement:
			if e.local() == "worksheet" && insert < 0 {
				insert = e.from
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if drawingID != "" {
		if insert < 0 {
			return ErrXLSX
		}
		tag := `<legacyDrawing xmlns="` + mainNS + `" xmlns:r="` + relationNS + `" r:id="` + drawingID + `"/>`
		n.updates[sheet.Part], err = applyXML(ctx, sheetXML, []xmlPatch{{insert, insert, tag}})
	}
	return err
}
