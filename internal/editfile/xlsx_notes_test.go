package editfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestWorksheetLegacyNotes(t *testing.T) {
	isolateEditFileStorage(t)
	const drawing = "xl/drawings/vmlDrawing7.vml"
	const comments = "xl/comments1.xml"
	for _, tc := range []struct{ label, part, from, to string }{
		{label: "note"},
		{label: "openpyxl paths", part: "paths"},
		{"control", drawing, `ObjectType="Note"`, `ObjectType="Button"`},
		{"cell link", drawing, "<x:Row>", "<x:FmlaLink>A1</x:FmlaLink><x:Row>"},
		{"different sheet coordinate", drawing, "<x:Row>3", "<x:Row>2"},
		{"invalid author", comments, `authorId="0"`, `authorId="2"`},
		{"missing sheet pointer", "xl/worksheets/sheet1.xml", `r:id="legacy"`, `r:id="notes"`},
	} {
		t.Run(tc.label, func(t *testing.T) {
			path := writeEditBook(t, func(parts map[string]string) {
				d, c := drawing, comments
				if tc.part == "paths" {
					d, c = "xl/drawings/commentsDrawing1.vml", "xl/comments/comment1.xml"
				}
				parts[d] = `<xml xmlns:v="` + vmlNS + `" xmlns:o="` + vmlOfficeNS + `" xmlns:x="` + vmlExcelNS + `"><v:shapetype id="_x0000_t202" o:spt="202"/><v:shape id="box1" type="#_x0000_t202"><x:ClientData ObjectType="Note"><x:Row>3</x:Row><x:Column>3</x:Column></x:ClientData></v:shape></xml>`
				parts[c] = `<comments xmlns="` + mainNS + `"><authors><author>A</author></authors><commentList><comment ref="D4" authorId="0"><text><r><rPr><b/></rPr><t>提示</t></r></text></comment></commentList></comments>`
				parts["xl/worksheets/_rels/sheet1.xml.rels"] = `<Relationships xmlns="` + packageNS + `"><Relationship Id="legacy" Type="` + relationNS + `/vmlDrawing" Target="/` + d + `"/><Relationship Id="notes" Type="` + relationNS + `/comments" Target="/` + c + `"/></Relationships>`
				parts["xl/worksheets/sheet1.xml"] = strings.Replace(testSheet1, "</p:worksheet>", `<p:legacyDrawing xmlns:r="`+relationNS+`" r:id="legacy"/></p:worksheet>`, 1)
				parts["[Content_Types].xml"] = strings.Replace(parts["[Content_Types].xml"], "</Types>", `<Default Extension="vml" ContentType="`+vmlMIME+`"/><Override PartName="/`+c+`" ContentType="`+spreadMIME+`comments+xml"/></Types>`, 1)
				if tc.from != "" {
					parts[tc.part] = strings.Replace(parts[tc.part], tc.from, tc.to, 1)
				}
			})
			wb, err := workbook.OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(wb.ClearIndexes)
			f, err := workbook.OpenEditSource(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			layout, proof, err := InspectXLSX(t.Context(), f, wb)
			if tc.from != "" {
				if !errors.Is(err, ErrXLSX) {
					t.Fatal("unverified note accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := os.Create(filepath.Join(t.TempDir(), "copy.xlsx"))
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			a := editdoc.Address{Sheet: "数据", Row: 4, Col: 4}
			saved, err := WriteXLSX(t.Context(), out, f, layout, proof, map[editdoc.Address]editdoc.Value{a: {Kind: "number", Raw: "47"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyXLSX(t.Context(), out, saved); err != nil {
				t.Fatal(err)
			}
			for part := range saved.Changed {
				if part != "xl/worksheets/sheet1.xml" {
					t.Fatalf("unrelated part rewritten: %s", part)
				}
			}
		})
	}
}
