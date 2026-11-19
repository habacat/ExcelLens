package editfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// A legacy note has two independent representations: its text and author in
// SpreadsheetML, and its box and zero-based cell address in an Excel VML
// ClientData element. The sheet owns both relationships; their filenames
// need not have matching numbers.
func (p *xlsxPackage) checkSheetNotes(ctx context.Context, links map[string]string) error {
	var comments, drawing string
	var controls int
	for _, target := range links {
		switch classifyPart(target).role {
		case "control":
			controls++
		case "comments":
			if comments != "" {
				return fmt.Errorf("%w：工作表关联了多份批注", ErrXLSX)
			}
			comments = target
		case "notesDrawing":
			if drawing != "" {
				return fmt.Errorf("%w：工作表关联了多份传统绘图", ErrXLSX)
			}
			drawing = target
		}
	}
	var notes map[string]bool
	if comments != "" {
		data, err := p.readPart(ctx, comments)
		if err != nil {
			return err
		}
		notes, err = commentAddresses(ctx, data)
		if err != nil {
			return fmt.Errorf("%s：%w", comments, err)
		}
	}
	if drawing == "" {
		if controls != 0 {
			return fmt.Errorf("%w：控件缺少传统绘图", ErrXLSX)
		}
		return nil
	}
	data, err := p.readPart(ctx, drawing)
	if err != nil {
		return err
	}
	if controls != 0 {
		data, err = stripButtonShapes(ctx, data, controls, p.layout.MacroEnabled)
		if err != nil {
			return fmt.Errorf("%s：%w", drawing, err)
		}
	}
	boxes, err := noteBoxAddresses(ctx, data)
	if err != nil {
		return fmt.Errorf("%s：%w", drawing, err)
	}
	if comments == "" && controls == 0 || !maps.Equal(notes, boxes) {
		return fmt.Errorf("%w：批注绘图与当前工作表的批注坐标不一致", ErrXLSX)
	}
	return nil
}

var commentChildren = map[string][]string{
	"": {"comments"}, "comments": {"authors", "commentList"}, "authors": {"author"},
	"commentList": {"comment"}, "comment": {"text"}, "text": {"t", "r"}, "r": {"rPr", "t"},
	"rPr": {"rFont", "charset", "family", "b", "i", "strike", "outline", "shadow", "condense", "extend", "color", "sz", "u", "vertAlign", "scheme"},
}

func commentAddresses(ctx context.Context, data []byte) (map[string]bool, error) {
	result := map[string]bool{}
	var authors int
	var seenAuthors, seenList bool
	err := walkXML(ctx, data, func(e xmlEvent) error {
		switch t := e.token.(type) {
		case xml.StartElement:
			if t.Name.Space != mainNS || !slices.Contains(commentChildren[e.parent()], e.local()) || !mainAttributes(t) {
				return fmt.Errorf("%w：批注元素 %s", ErrXLSX, e.local())
			}
			switch e.local() {
			case "authors":
				if seenAuthors || seenList {
					return ErrXLSX
				}
				seenAuthors = true
			case "author":
				authors++
			case "commentList":
				if !seenAuthors || seenList {
					return ErrXLSX
				}
				seenList = true
			case "comment":
				ref := attribute(t, "", "ref")
				author, authorOK := nonnegativeInt(attribute(t, "", "authorId"))
				col, row, err := workbook.ParseCellRef(ref)
				if !authorOK || author >= authors || err != nil || row > formats.ExcelRows || col > formats.ExcelColumns ||
					ref != workbook.ColumnName(col)+strconv.Itoa(row) || result[ref] || len(result) >= formats.EditXLSXRangeLimit {
					return fmt.Errorf("%w：批注坐标或作者无效", ErrXLSX)
				}
				result[ref] = true
			}
		case xml.CharData:
			if e.local() != "author" && e.local() != "t" && len(bytes.TrimSpace(t)) != 0 {
				return ErrXLSX
			}
		}
		return nil
	})
	if err == nil && !seenList {
		err = fmt.Errorf("%w：缺少批注清单", ErrXLSX)
	}
	return result, err
}

const (
	vmlNS       = "urn:schemas-microsoft-com:vml"
	vmlOfficeNS = "urn:schemas-microsoft-com:office:office"
	vmlExcelNS  = "urn:schemas-microsoft-com:office:excel"
)

func vmlName(n xml.Name) string {
	switch n.Space {
	case "":
		return n.Local
	case vmlNS:
		return "v:" + n.Local
	case vmlOfficeNS:
		return "o:" + n.Local
	case vmlExcelNS:
		return "x:" + n.Local
	}
	return "?"
}

// Only passive formatting and Excel note geometry belong to this profile.
// Control bindings, macros, links and embedded images are different objects.
var noteVML = map[string]struct{ parents, attrs string }{
	"xml":           {"root", ""},
	"o:shapelayout": {"xml", "v:ext"}, "o:idmap": {"o:shapelayout", "v:ext data"},
	"v:shapetype": {"xml", "id coordsize o:spt path"}, "v:stroke": {"v:shapetype v:shape", "joinstyle color weight"},
	"v:path":  {"v:shapetype v:shape", "gradientshapeok o:connecttype"},
	"v:shape": {"xml", "id type style fillcolor strokecolor strokeweight filled stroked o:insetmode o:spid o:allowincell"},
	"v:fill":  {"v:shape", "color2 color angle type"}, "v:shadow": {"v:shape", "on color obscured offset opacity"},
	"v:textbox": {"v:shape", "style inset"}, "div": {"v:textbox", "style"},
	"x:ClientData": {"v:shape", "ObjectType"}, "x:MoveWithCells": {"x:ClientData", ""}, "x:SizeWithCells": {"x:ClientData", ""},
	"x:Anchor": {"x:ClientData", ""}, "x:AutoFill": {"x:ClientData", ""}, "x:AutoLine": {"x:ClientData", ""},
	"x:Row": {"x:ClientData", ""}, "x:Column": {"x:ClientData", ""}, "x:Visible": {"x:ClientData", ""},
	"x:TextHAlign": {"x:ClientData", ""}, "x:TextVAlign": {"x:ClientData", ""},
}

func noteBoxAddresses(ctx context.Context, data []byte) (map[string]bool, error) {
	boxes, ids := map[string]bool{}, map[string]bool{}
	var shapeType string
	var row, col int
	var client bool
	var value strings.Builder
	err := walkXML(ctx, data, func(e xmlEvent) error {
		key := vmlName(e.stack[len(e.stack)-1])
		switch t := e.token.(type) {
		case xml.StartElement:
			parent := "root"
			if len(e.stack) > 1 {
				parent = vmlName(e.stack[len(e.stack)-2])
			}
			spec, ok := noteVML[key]
			if !ok || !slices.Contains(strings.Fields(spec.parents), parent) {
				return fmt.Errorf("%w：传统绘图元素 %s", ErrXLSX, key)
			}
			for _, a := range t.Attr {
				if !nsDeclaration(a) && !slices.Contains(strings.Fields(spec.attrs), vmlName(a.Name)) {
					return fmt.Errorf("%w：传统绘图属性 %s", ErrXLSX, a.Name.Local)
				}
				if a.Name == (xml.Name{Local: "style"}) && strings.ContainsAny(a.Value, "(){}\\@") {
					return fmt.Errorf("%w：批注绘图样式", ErrXLSX)
				}
			}
			switch key {
			case "v:shapetype":
				id := attribute(t, "", "id")
				if shapeType != "" || id == "" || attribute(t, vmlOfficeNS, "spt") != "202" {
					return fmt.Errorf("%w：传统绘图含非批注形状类型", ErrXLSX)
				}
				shapeType = id
			case "v:shape":
				id := attribute(t, "", "id")
				if shapeType == "" || attribute(t, "", "type") != "#"+shapeType || id == "" || ids[id] || len(ids) >= formats.EditXLSXRangeLimit {
					return fmt.Errorf("%w：批注形状标识或引用无效", ErrXLSX)
				}
				ids[id] = true
				row, col, client = -1, -1, false
			case "x:ClientData":
				object := attribute(t, "", "ObjectType")
				if client || object != "Note" {
					return fmt.Errorf("%w：传统绘图对象 %s", ErrXLSX, object)
				}
				client = true
			case "x:Row", "x:Column":
				value.Reset()
			}
		case xml.CharData:
			if key == "x:Row" || key == "x:Column" {
				if value.Len()+len(t) > 64 {
					return ErrXLSX
				}
				value.Write(t)
			} else if !strings.HasPrefix(key, "x:") && len(bytes.TrimSpace(t)) != 0 {
				return ErrXLSX
			}
		case xml.EndElement:
			switch key {
			case "x:Row", "x:Column":
				n, ok := nonnegativeInt(strings.TrimSpace(value.String()))
				if !ok || key == "x:Row" && (row != -1 || n >= formats.ExcelRows) || key == "x:Column" && (col != -1 || n >= formats.ExcelColumns) {
					return fmt.Errorf("%w：批注框坐标无效", ErrXLSX)
				}
				if key == "x:Row" {
					row = n
				} else {
					col = n
				}
			case "v:shape":
				if !client || row < 0 || col < 0 {
					return fmt.Errorf("%w：批注框缺少工作表坐标", ErrXLSX)
				}
				ref := workbook.ColumnName(col+1) + strconv.Itoa(row+1)
				if boxes[ref] {
					return fmt.Errorf("%w：批注框坐标重复", ErrXLSX)
				}
				boxes[ref] = true
			}
		}
		return nil
	})
	return boxes, err
}

// Excel stores a DEVMODE structure followed by the printer driver's extra
// bytes. The editor does not load the driver or interpret its private data.
func checkPrinterSettings(_ context.Context, _ string, data []byte) error {
	for _, offset := range []int{68, 36} { // DEVMODEW and DEVMODEA
		if len(data) < offset+4 {
			continue
		}
		size := int(binary.LittleEndian.Uint16(data[offset:]))
		extra := int(binary.LittleEndian.Uint16(data[offset+2:]))
		if size >= offset+4 && size+extra == len(data) {
			return nil
		}
	}
	return fmt.Errorf("%w：打印设置数据长度无效", ErrXLSX)
}
