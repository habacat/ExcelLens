package workbook

import (
	"archive/zip"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hyperlinkTestFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "links.xlsx")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	parts := map[string]string{
		"xl/workbook.xml":                     `<?xml version="1.0"?><workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Home" sheetId="1" r:id="rId1"/><sheet name="Detail Sheet" sheetId="2" r:id="rId2"/><sheet name="O'Brien" sheetId="3" r:id="rId3"/></sheets><definedNames><definedName name="MyPos">'Detail Sheet'!$B$4</definedName><definedName name="MyPos" localSheetId="0">'Detail Sheet'!$D$44</definedName><definedName name="Loop">Loop</definedName></definedNames></workbook>`,
		"xl/_rels/workbook.xml.rels":          `<?xml version="1.0"?><Relationships><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet2.xml"/><Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet3.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":            `<?xml version="1.0"?><worksheet xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>go detail</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>website</t></is></c></row><row r="3"><c r="A3" t="inlineStr"><is><t>relationship internal</t></is></c></row><row r="4"><c r="A4" t="inlineStr"><is><t>named link</t></is></c></row><row r="5"><c r="A5" t="inlineStr"><is><t>unsafe</t></is></c></row><row r="6"><c r="B6" t="inlineStr"><is><t>span left</t></is></c><c r="C6" t="inlineStr"><is><t>span right</t></is></c></row><row r="7"><c r="A7" t="str"><f>HYPERLINK(&quot;#'Detail Sheet'!G23&quot;,&quot;Go&quot;)</f><v>Go</v></c></row><row r="8"><c r="A8" t="str"><f>HYPERLINK(&quot;https://example.org/report&quot;,&quot;Site&quot;)</f><v>Site</v></c></row><row r="9"><c r="A9" t="str"><f>HYPERLINK(A1,&quot;No&quot;)</f><v>No</v></c></row><row r="10"><c r="A10" t="inlineStr"><is><t>missing</t></is></c></row><row r="11"><c r="A11" t="str"><f>HYPERLINK(&quot;file:///c:/evil&quot;,&quot;No&quot;)</f><v>No</v></c></row><row r="12"><c r="Z12"><v>1</v></c></row><row r="13"><c r="A13" t="inlineStr"><is><t>apostrophe</t></is></c></row></sheetData><hyperlinks><hyperlink ref="A1" location="'Detail Sheet'!$C$25" tooltip="Go detail"/><hyperlink ref="A2" r:id="h2"/><hyperlink ref="A3" r:id="h3"/><hyperlink ref="A4" location="MyPos"/><hyperlink ref="A5" r:id="h5"/><hyperlink ref="B6:C6" location="'Detail Sheet'!B2"/><hyperlink ref="A10" location="Unknown!A1"/><hyperlink ref="E12" location="'Detail Sheet'!C7" display="See detail"/><hyperlink ref="A13" location="'O''Brien'!$B$2"/></hyperlinks></worksheet>`,
		"xl/worksheets/_rels/sheet1.xml.rels": `<?xml version="1.0"?><Relationships><Relationship Id="h2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.org/help?q=1" TargetMode="External"/><Relationship Id="h3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="#'Detail Sheet'!B6" TargetMode="External"/><Relationship Id="h5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="javascript:alert(1)" TargetMode="External"/></Relationships>`,
		"xl/worksheets/sheet2.xml":            `<?xml version="1.0"?><worksheet><sheetData><row r="1"><c r="A1"><v>2</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet3.xml":            `<?xml version="1.0"?><worksheet><sheetData><row r="1"><c r="A1"><v>3</v></c></row></sheetData></worksheet>`,
	}
	for name, data := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExcelHyperlinksResolveAndPreserveSource(t *testing.T) {
	isolateTestStorage(t)
	file := hyperlinkTestFixture(t)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	home, ok := wb.FindSheet("Home")
	if !ok {
		t.Fatal("no home")
	}
	idx, err := wb.BuildIndex(home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.HyperlinkStart <= 0 {
		t.Fatalf("index did not capture hyperlink XML offset: %d", idx.HyperlinkStart)
	}
	reloaded, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	cached := reloaded.GetCachedIndex(home.Path)
	if cached == nil || cached.HyperlinkStart != idx.HyperlinkStart {
		t.Fatalf("cached hyperlink marker mismatch: %+v", cached)
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.ReadRows(idx, 0, 13, 1, 26)
	if err != nil {
		t.Fatal(err)
	}
	if err := wb.DecorateHyperlinks(home, rows, nil, 1, 26); err != nil {
		t.Fatal(err)
	}
	byRef := make(map[string]CellData)
	for _, r := range rows {
		for _, c := range r.Cells {
			byRef[c.Ref] = c
		}
	}
	cases := []struct{ ref, kind, sheet, target, url string }{
		{"A1", "internal", "Detail Sheet", "C25", ""},
		{"A2", "external", "", "", "https://example.org/help?q=1"},
		{"A3", "internal", "Detail Sheet", "B6", ""},
		{"A4", "internal", "Detail Sheet", "D44", ""},
		{"B6", "internal", "Detail Sheet", "B2", ""},
		{"C6", "internal", "Detail Sheet", "B2", ""},
		{"A7", "internal", "Detail Sheet", "G23", ""},
		{"A8", "external", "", "", "https://example.org/report"},
		{"E12", "internal", "Detail Sheet", "C7", ""},
		{"A13", "internal", "O'Brien", "B2", ""},
	}
	for _, test := range cases {
		data, found := byRef[test.ref]
		if !found {
			t.Errorf("missing %s", test.ref)
			continue
		}
		link := data.Hyperlink
		if link == nil {
			t.Errorf("missing hyperlink at %s: %+v", test.ref, data)
			continue
		}
		if link.Kind != test.kind || link.Sheet != test.sheet || link.Ref != test.target || link.URL != test.url {
			t.Errorf("%s = %+v, want %+v", test.ref, *link, test)
		}
	}
	for _, ref := range []string{"A5", "A9", "A10", "A11"} {
		if data := byRef[ref]; data.Hyperlink != nil {
			t.Errorf("unsafe or unresolved hyperlink at %s: %+v", ref, data.Hyperlink)
		}
	}
	if data := byRef["A1"]; data.Display != "go detail" {
		t.Error("original display modified")
	}
	if data := byRef["E12"]; data.Display != "See detail" {
		t.Error("link-only display missing")
	}
	again, err := wb.SheetHyperlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.single) == 0 {
		t.Error("cache is unexpectedly empty")
	}
	after, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("hyperlink reading modified original workbook")
	}
}

func TestHyperlinkDestinationValidation(t *testing.T) {
	isolateTestStorage(t)
	wb, err := OpenWorkbook(hyperlinkTestFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := []string{"#Home!A1", "#'Detail Sheet'!$B$3:$C$4", "'O''Brien'!$B$2", "MyPos"}
	for _, target := range valid {
		if _, ok := wb.resolveHyperlinkLocation(target, "Home", 0); !ok {
			t.Errorf("valid %q", target)
		}
	}
	invalid := []string{"#Ghost!A1", "#Home!XFE1", "#Home!A1048577", "Loop", "#Home!A0", "#Home!#REF!"}
	for _, target := range invalid {
		if _, ok := wb.resolveHyperlinkLocation(target, "Home", 0); ok {
			t.Errorf("invalid %q", target)
		}
	}
	for _, target := range []string{"javascript:alert(1)", "file:///C:/private", "data:text/html,bad", "https://user:pass@example.org"} {
		if _, ok := safeExternalHyperlink(target); ok {
			t.Errorf("unsafe accepted %q", target)
		}
	}
	for _, target := range []string{"https://example.org/test", "http://example.org", "mailto:help@example.org"} {
		if _, ok := safeExternalHyperlink(target); !ok {
			t.Errorf("safe rejected %q", target)
		}
	}
	_, _, _, _, ok := parseAddressRange("A1:XFD1048576")
	if !ok {
		t.Error("valid large area")
	}
	if _, _, _, _, ok := parseAddressRange("A1:B0"); ok {
		t.Error("invalid range accepted")
	}
	if !strings.Contains(wb.NamedRanges["mypos"], "B$4") {
		t.Fatalf("global named range missing: %+v", wb.NamedRanges)
	}
}

// Native caches made before the source-text scope fix must be rebuilt. The
// refreshed index retains hyperlink offsets without an extra metadata scan.
func TestHyperlinkLegacyNativeIndexRebuildsWithCurrentExtentRules(t *testing.T) {
	isolateTestStorage(t)
	file := hyperlinkTestFixture(t)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := wb.FindSheet("Home")
	idx, err := wb.BuildIndex(home, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.HyperlinkStart <= 0 {
		t.Fatalf("new index missing hyperlink marker %d", idx.HyperlinkStart)
	}
	raw, err := os.ReadFile(idx.IndexPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 20 || string(raw[:8]) != "XLIDX009" {
		t.Fatal("bad new index fixture")
	}
	// Old version has no 8-byte hyperlink offset preceding its checksum.
	older := append([]byte(nil), raw[:len(raw)-12]...)
	copy(older[:8], []byte("XLIDX007"))
	var footer [4]byte
	binary.LittleEndian.PutUint32(footer[:], crc32.ChecksumIEEE(older))
	older = append(older, footer[:]...)
	if err := os.WriteFile(idx.IndexPath, older, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	oldIndex := reopened.GetCachedIndex(home.Path)
	if oldIndex != nil {
		t.Fatalf("legacy native extent was reused: %+v", oldIndex)
	}
	oldIndex, err = reopened.BuildIndex(home, nil, nil)
	if err != nil || oldIndex.HyperlinkStart <= 0 {
		t.Fatalf("legacy native cache did not rebuild: %+v / %v", oldIndex, err)
	}
	links, err := reopened.SheetHyperlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := links.lookup(1, 1); !ok || l.Ref != "C25" {
		t.Fatalf("fallback hyperlink missing %+v", l)
	}
	noLinks, _ := wb.FindSheet("Detail Sheet")
	blankIndex, err := wb.BuildIndex(noLinks, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if blankIndex.HyperlinkStart != -1 {
		t.Fatalf("link-free sheet hint %d", blankIndex.HyperlinkStart)
	}
}
