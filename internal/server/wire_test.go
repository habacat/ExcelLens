package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/workbook"
)

func viewportWireFixture() []workbook.RowData {
	rows := make([]workbook.RowData, 80)
	for i := range rows {
		rows[i] = workbook.RowData{Number: i + 1, Cells: make([]workbook.CellData, 39)}
		for j := range rows[i].Cells {
			rows[i].Cells[j] = workbook.CellData{Ref: fmt.Sprintf("%s%d", workbook.ColumnName(j+1), i+1), Col: j + 1, Raw: "0001234567890.123456", Display: "0001234567890.123456", Type: "inlineStr"}
		}
	}
	return rows
}

func BenchmarkViewportJSONFull(b *testing.B) {
	rows := viewportWireFixture()
	var raw []byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		raw, err = json.Marshal(rows)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(raw)), "wire-B/op")
}
func BenchmarkViewportJSONCompact(b *testing.B) {
	rows := viewportWireFixture()
	var raw []byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		raw, err = json.Marshal(packViewportRows(rows))
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(len(raw)), "wire-B/op")
}

func TestCompactViewportRetainsEmptyDisplayFormulaFlagsAndNonstandardRef(t *testing.T) {
	rows := []workbook.RowData{{Number: 7, Style: 2, Cells: []workbook.CellData{
		{Col: 1, Ref: "A7", Raw: "00012345678901234567", Display: "00012345678901234567", Type: "inlineStr"},
		{Col: 2, Ref: "B7", Raw: "1.2300001", Display: "", Style: 8, ValueColor: "#ff0000"},
		{Col: 3, Ref: "C7", Raw: "", Display: "", Formula: "=A1+1", HasFormula: true, CacheMissing: true, SharedFormula: true},
		{Col: 4, Ref: "D9", Raw: "A&B", Display: "链接", Hyperlink: &workbook.CellHyperlink{Kind: "external", URL: "https://example.com/a?b=1&c=2"}},
	}}}
	packed := packViewportRows(rows)
	if packed[0].Cells[0].Ref != nil || packed[0].Cells[0].Display != nil || packed[0].Cells[0].Raw != rows[0].Cells[0].Raw {
		t.Fatal("redundant data not omitted exactly")
	}
	if packed[0].Cells[1].Display == nil || *packed[0].Cells[1].Display != "" || packed[0].Cells[1].Color != "#ff0000" {
		t.Fatal("empty display/color was lost")
	}
	if packed[0].Cells[2].Flags&7 != 7 || packed[0].Cells[2].Formula != "=A1+1" {
		t.Fatal("formula provenance lost")
	}
	if packed[0].Cells[2].Flags&8 == 0 {
		t.Fatal("explicit zero cell style lost under nonzero row style")
	}
	if packed[0].Cells[3].Ref == nil || *packed[0].Cells[3].Ref != "D9" || packed[0].Cells[3].Hyperlink.URL != rows[0].Cells[3].Hyperlink.URL {
		t.Fatal("source ref/hyperlink changed")
	}
	if _, err := json.Marshal(packed); err != nil {
		t.Fatal(err)
	}
}

func TestCompactViewportHTTPKeepsFinancialTextAndStyle(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	path := filepath.Join(t.TempDir(), "ledger.txt")
	_ = os.WriteFile(path, []byte("ID@amount\n00012345678901234567@1234567890.000001\n"), 0600)
	if err := app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := app.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	full := featureCall(t, app, "GET", "/api/sheet?sheet=数据&row=2&limit=1&cols=2", "")
	packed := featureCall(t, app, "GET", "/api/sheet?sheet=数据&row=2&limit=1&cols=2&compact=1", "")
	var plain struct{ Rows []workbook.RowData }
	if err := json.Unmarshal(full.Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	var small struct {
		Compact int
		Rows    []wireRow
	}
	if err := json.Unmarshal(packed.Body.Bytes(), &small); err != nil {
		t.Fatal(err)
	}
	if small.Compact != 1 || len(small.Rows) != 1 || small.Rows[0].Cells[0].Raw != plain.Rows[0].Cells[0].Raw || small.Rows[0].Cells[1].Raw != "1234567890.000001" {
		t.Fatal("compact HTTP changed values")
	}
}
