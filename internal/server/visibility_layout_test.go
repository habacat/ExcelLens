package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/workbook"
)

func TestLayoutExposesSourceVisibilityOutlineDefaultsWithoutPersistingOrChangingSource(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "axis.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetPr><outlinePr summaryBelow="0" summaryRight="1"/></sheetPr><sheetFormatPr defaultRowHeight="0" defaultColWidth="0" zeroHeight="1"/><cols><col min="2" max="4" width="0" hidden="1" outlineLevel="2" collapsed="1"/></cols><sheetData><row r="1" hidden="0" ht="20"><c r="A1"><v>9007199254740993.31</v></c></row><row r="50" hidden="1" outlineLevel="1"/><row r="51" hidden="1" outlineLevel="1"/><row r="52" collapsed="1"/></sheetData></worksheet>`,
	})
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	w := a.currentWorkbook()
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx == nil || idx.MaxRow != 1 || idx.MaxCol != 1 {
		t.Fatal("metadata changed source used range", idx)
	}
	layout := decodeResponse[sheetLayout](t, featureCall(t, a, "GET", "/api/layout?sheet=Data", ""))
	if !layout.SourceZeroHeight || !layout.SourceDefaultRowZeroSize || !layout.SourceDefaultColZeroSize || layout.SourceSummaryBelow == nil || *layout.SourceSummaryBelow || layout.SourceSummaryRight == nil || !*layout.SourceSummaryRight || layout.SourceStateNotice != "" {
		t.Fatal("layout endpoint dropped default/summary semantics", layout)
	}
	if len(layout.SourceRowStates) != 3 || layout.SourceRowStates[1] != (workbook.SourceAxisRange{Min: 50, Max: 51, Hidden: true, HiddenSet: true, Level: 1}) || len(layout.SourceColStates) != 1 || !layout.SourceColStates[0].ZeroSize || layout.SourceColStates[0].Level != 2 {
		t.Fatal("layout endpoint dropped compressed source intervals", layout.SourceRowStates, layout.SourceColStates)
	}
	featureCall(t, a, "POST", "/api/layout", `{"sheet":"Data","kind":"row","index":1,"size":30}`)
	stored, err := os.ReadFile(layoutFile(w))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sourceRowStates", "sourceColStates", "sourceZeroHeight", "sourceSummaryBelow", "sourceDefaultRowZeroSize"} {
		if bytes.Contains(stored, []byte(key)) {
			t.Fatal("immutable source metadata leaked into view preferences", key)
		}
	}
	again := decodeResponse[sheetLayout](t, featureCall(t, a, "GET", "/api/layout?sheet=Data", ""))
	if again.Rows["1"] != 30 || again.SourceRowStates[1] != layout.SourceRowStates[1] || !again.SourceDefaultRowZeroSize {
		t.Fatal("local view resize overwrote source flags", again)
	}
	cell := decodeResponse[struct{ Cell workbook.CellData }](t, featureCall(t, a, "GET", "/api/cell?sheet=Data&ref=A1", ""))
	if cell.Cell.Raw != "9007199254740993.31" || cell.Cell.Type != "" {
		t.Fatal("metadata transport changed source value/type", cell)
	}
	if after, _ := os.ReadFile(file); !bytes.Equal(before, after) {
		t.Fatal("layout metadata endpoint changed source file")
	}
}
