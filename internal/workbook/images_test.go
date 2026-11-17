package workbook

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO9ZbuYAAAAASUVORK5CYII="

func imageFixture(t *testing.T, path string) {
	t.Helper()
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	put := func(name string, b []byte) {
		wr, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = wr.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	book := `<?xml version="1.0"?><workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Drawing Sheet" sheetId="1" r:id="rId1"/></sheets></workbook>`
	bookRels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`
	sheet := `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><dimension ref="A1:J20"/><sheetData><row r="1"><c r="A1"><v>8</v></c></row></sheetData><drawing r:id="rDrawing"/></worksheet>`
	sheetRels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rDrawing" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/drawing1.xml"/><Relationship Id="unused" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/drawing" Target="../drawings/unused.xml"/></Relationships>`
	marker := func(col, row, offX, offY int) string {
		return "<xdr:col>" + imageItoa(col) + "</xdr:col><xdr:colOff>" + imageItoa(offX) + "</xdr:colOff><xdr:row>" + imageItoa(row) + "</xdr:row><xdr:rowOff>" + imageItoa(offY) + "</xdr:rowOff>"
	}
	pic := func(id, name string) string {
		return `<xdr:pic><xdr:nvPicPr><xdr:cNvPr id="7" name="` + name + `" descr="` + name + ` description"/></xdr:nvPicPr><xdr:blipFill><a:blip r:embed="` + id + `"/><a:stretch><a:fillRect/></a:stretch></xdr:blipFill><xdr:spPr><a:xfrm rot="5400000" flipH="1"/></xdr:spPr></xdr:pic>`
	}
	drawing := `<?xml version="1.0"?><xdr:wsDr xmlns:xdr="http://schemas.openxmlformats.org/drawingml/2006/spreadsheetDrawing" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<xdr:oneCellAnchor><xdr:from>` + marker(1, 2, 95250, 47625) + `</xdr:from><xdr:ext cx="1143000" cy="762000"/>` + pic("img1", "OneCell") + `<xdr:clientData/></xdr:oneCellAnchor>` +
		`<xdr:twoCellAnchor editAs="twoCell"><xdr:from>` + marker(2, 3, 0, 0) + `</xdr:from><xdr:to>` + marker(4, 7, 0, 0) + `</xdr:to>` + pic("img2", "TwoCell") + `<xdr:clientData/></xdr:twoCellAnchor>` +
		`<xdr:absoluteAnchor><xdr:pos x="762000" y="285750"/><xdr:ext cx="476250" cy="381000"/>` + pic("img3", "Absolute") + `<xdr:clientData/></xdr:absoluteAnchor>` +
		`<xdr:twoCellAnchor editAs="oneCell"><xdr:from>` + marker(5, 2, 0, 0) + `</xdr:from><xdr:to>` + marker(8, 5, 0, 0) + `</xdr:to>` + strings.Replace(pic("img1", "MoveOnly"), `<a:xfrm rot="5400000" flipH="1"/>`, `<a:xfrm rot="5400000" flipH="1"><a:ext cx="1905000" cy="952500"/></a:xfrm>`, 1) + `<xdr:clientData/></xdr:twoCellAnchor>` +
		`<xdr:twoCellAnchor editAs="absolute"><xdr:from>` + marker(5, 10, 0, 0) + `</xdr:from><xdr:to>` + marker(8, 13, 0, 0) + `</xdr:to>` + strings.Replace(pic("img2", "Fixed"), `<a:xfrm rot="5400000" flipH="1"/>`, `<a:xfrm rot="5400000" flipH="1"><a:off x="0" y="285750"/><a:ext cx="1428750" cy="666750"/></a:xfrm>`, 1) + `<xdr:clientData/></xdr:twoCellAnchor>` +
		`<xdr:oneCellAnchor><xdr:from>` + marker(4, 9, 0, 0) + `</xdr:from><xdr:ext cx="1143000" cy="762000"/><xdr:pic><xdr:blipFill><a:blip r:link="external"/></xdr:blipFill></xdr:pic><xdr:clientData/></xdr:oneCellAnchor></xdr:wsDr>`
	drawingRels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="img1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/><Relationship Id="img2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="/xl/media/image2.png"/><Relationship Id="img3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="/media/image3.svg"/><Relationship Id="external" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="https://example.org/image.png" TargetMode="External"/></Relationships>`
	png, e := base64.StdEncoding.DecodeString(testPNGBase64)
	if e != nil {
		t.Fatal(e)
	}
	for name, text := range map[string]string{"xl/workbook.xml": book, "xl/_rels/workbook.xml.rels": bookRels, "xl/worksheets/sheet1.xml": sheet, "xl/worksheets/_rels/sheet1.xml.rels": sheetRels, "xl/drawings/drawing1.xml": drawing, "xl/drawings/_rels/drawing1.xml.rels": drawingRels, "media/image3.svg": `<svg xmlns="http://www.w3.org/2000/svg" width="50" height="50"><rect width="50" height="50" fill="#1267ab"/></svg>`, "xl/drawings/unused.xml": drawing} {
		put(name, []byte(text))
	}
	put("xl/media/image1.png", png)
	put("xl/media/image2.png", png)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
func imageItoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	return digits
}

func TestEmbeddedImagesRealOOXML(t *testing.T) {
	file := filepath.Join(t.TempDir(), "pictures.xlsx")
	imageFixture(t, file)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	if _, err = wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	images, err := wb.SheetImages(meta)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 5 {
		t.Fatalf("expected five embedded pictures, got %d: %+v", len(images), images)
	}
	one := images[0]
	if one.Kind != "oneCell" || one.From.Col != 1 || one.From.Row != 2 || one.From.ColOff != 10 || one.From.RowOff != 5 || one.Width != 120 || one.Height != 80 {
		t.Fatalf("one cell anchor wrong: %+v", one)
	}
	if one.Rotation != 90 || !one.FlipH || one.Name != "OneCell" || one.Description != "OneCell description" {
		t.Errorf("picture properties wrong: %+v", one)
	}
	two := images[1]
	if two.Kind != "twoCell" || two.To == nil || two.To.Col != 4 || two.To.Row != 7 {
		t.Fatalf("two-cell anchor wrong: %+v", two)
	}
	abs := images[2]
	if abs.Kind != "absolute" || abs.X != 80 || abs.Y != 30 || abs.Width != 50 || abs.Height != 40 {
		t.Fatalf("absolute anchor wrong: %+v", abs)
	}
	move := images[3]
	if move.Kind != "twoCell" || move.EditAs != "oneCell" || !move.HasTransform || move.Width != 200 || move.Height != 100 {
		t.Errorf("move-only transform incorrect: %+v", move)
	}
	fixed := images[4]
	if fixed.Kind != "twoCell" || fixed.EditAs != "absolute" || !fixed.HasTransform || fixed.X != 0 || fixed.Y != 30 || fixed.Width != 150 || fixed.Height != 70 {
		t.Errorf("absolute editAs transform incorrect: %+v", fixed)
	}
	for i := 0; i < 5; i++ {
		b, mime, err := wb.PictureBytes(meta, i)
		if err != nil {
			t.Fatalf("picture %d: %v", i, err)
		}
		if len(b) == 0 {
			t.Fatal("missing picture content")
		}
		if i == 2 && mime != "image/svg+xml" || i != 2 && mime != "image/png" {
			t.Errorf("picture %d mime=%s", i, mime)
		}
	}
	if _, _, err := wb.PictureBytes(meta, 5); !os.IsNotExist(err) {
		t.Errorf("invalid ID should be rejected: %v", err)
	}
	again, err := wb.SheetImages(meta)
	if err != nil || len(again) != 5 {
		t.Fatal("image metadata cache failed", err)
	}
	again[0].Name = "CHANGED"
	after, _ := wb.SheetImages(meta)
	if after[0].Name != "OneCell" {
		t.Fatal("cache should not expose mutable slice")
	}
}
func TestPictureContentSafety(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
		want    string
	}{
		{"valid PNG", []byte("\x89PNG\r\n\x1a\nhello"), "image/png"},
		{"valid WebP", []byte("RIFF0000WEBPVP8 "), "image/webp"},
		{"HTML renamed PNG", []byte("<html><script>alert(1)</script></html>"), ""},
		{"SVG script", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ""},
		{"SVG external", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://evil.invalid/a"/></svg>`), ""},
		{"SVG DOM event", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"/>`), ""},
		{"SVG basic", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><path d="M0 0"/></svg>`), "image/svg+xml"},
		{"Windows EMF", []byte("\x01\x00\x00\x00"), ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := detectSafeImageType(test.content); got != test.want {
				t.Errorf("mime=%q, want %q", got, test.want)
			}
		})
	}
	for _, v := range []string{"../../outside.png", "https://evil.invalid/x", "foo?x=1", ""} {
		if _, valid := resolvePackagePart("xl/drawings/drawing1.xml", v); valid && strings.HasPrefix(v, "http") {
			t.Fatalf("unexpected accepted external scheme: %s", v)
		}
	}
	if _, valid := resolvePackagePart("xl/drawings/drawing1.xml", "../../../../escape.png"); valid {
		t.Fatal("package path escaped ZIP root")
	}
	idMap := map[string]bool{}
	drawingIDs([]byte(`<drawing r:id="first"/><x:drawing r:id='second'/>`), idMap)
	if !idMap["first"] || !idMap["second"] {
		t.Fatalf("drawing references not found: %v", idMap)
	}
}
func TestReferencedDrawingIDsAcrossBuffers(t *testing.T) {
	file := filepath.Join(t.TempDir(), "big.xml")
	var b bytes.Buffer
	b.WriteString("<worksheet><sheetData>")
	b.Write(bytes.Repeat([]byte("x"), 2<<20))
	b.WriteString(`<drawing r:id="rId77"/>`)
	b.Write(bytes.Repeat([]byte("z"), 2<<20))
	b.WriteString("</sheetData></worksheet>")
	if err := os.WriteFile(file, b.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := referencedDrawingIDs(file)
	if err != nil || !got["rId77"] {
		t.Fatalf("fallback missed drawing: %v %v", got, err)
	}
}
