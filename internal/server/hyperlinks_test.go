package server

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func hyperlinkServerFixture(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "hyperlinks.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="r1"/><sheet name="Summary" sheetId="2" r:id="r2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="r2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Go Summary</t></is></c></row><row r="2"><c r="A2" t="str"><f>HYPERLINK(&quot;#Summary!C9&quot;,&quot;Jump&quot;)</f><v>Jump</v></c></row></sheetData><hyperlinks><hyperlink ref="A1" location="Summary!$B$18" tooltip="Jump detail"/></hyperlinks></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`,
	}
	for part, data := range parts {
		entry, err := writer.Create(part)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestHyperlinksAPIAndSourceReadOnly(t *testing.T) {
	isolateTestStorage(t)
	source := hyperlinkServerFixture(t)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(source); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	if _, err = wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	call := func(path string) map[string]any {
		res := featureCall(t, app, "GET", path, "")
		var obj map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &obj); err != nil {
			t.Fatal(err)
		}
		return obj
	}
	sheet := call("/api/sheet?sheet=First&row=1&limit=2&col=1&cols=2")
	if sheet["hyperlinkError"] != "" {
		t.Fatalf("hyperlink parse failure: %v", sheet["hyperlinkError"])
	}
	rows := sheet["rows"].([]any)
	want := []string{"B18", "C9"}
	for i, item := range rows {
		cells := item.(map[string]any)["cells"].([]any)
		if len(cells) < 1 {
			t.Fatalf("row %d missing cell", i+1)
		}
		link, ok := cells[0].(map[string]any)["hyperlink"].(map[string]any)
		if !ok || link["kind"] != "internal" || link["sheet"] != "Summary" || link["ref"] != want[i] {
			t.Fatalf("row %d link=%v", i+1, link)
		}
	}
	cell := call("/api/cell?sheet=First&ref=A1")["cell"].(map[string]any)
	if cell["hyperlink"].(map[string]any)["ref"] != "B18" {
		t.Fatal(cell)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("source workbook was modified")
	}
}
