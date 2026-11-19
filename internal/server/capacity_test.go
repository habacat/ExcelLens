package server

import (
	"encoding/json"
	"net/url"
	"testing"

	"excellens/internal/workbook"
)

func TestBillionthRowCellAndFormulaKeepOriginalTextAndCoordinates(t *testing.T) {
	a, key := calculatorTestWorkbook(t)
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	idx := wb.GetCachedIndex(meta.Path)
	// Move only the final index coordinate in this tiny sparse fixture. This
	// exercises HTTP addressing without claiming a billion-record load test.
	offsets := make([]workbook.RowOffset, idx.RowCount())
	for i, row := range idx.RowOffsets() {
		offsets[i] = row
	}
	offsets[len(offsets)-1].Number = 1_000_000_000
	idx.RowIndex = workbook.NewRowIndex(offsets)
	idx.MaxRow = 1_000_000_000
	idx.Dimension = "A1:B1000000000"
	query := "/api/cell?sheet=" + url.QueryEscape(meta.Name) + "&ref=A1000000000"
	var cell struct {
		Cell workbook.CellData `json:"cell"`
	}
	if err := json.Unmarshal(featureCall(t, a, "GET", query, "").Body.Bytes(), &cell); err != nil {
		t.Fatal(err)
	}
	if cell.Cell.Raw != "1/2" || cell.Cell.Ref != "A1000000000" || cell.Cell.Type != "inlineStr" {
		t.Fatal(cell.Cell)
	}
	body, _ := json.Marshal(map[string]string{"expression": "=A1000000000", "sheet": meta.Name, "workbookKey": key})
	var formula struct {
		Display string `json:"display"`
	}
	if err := json.Unmarshal(featureCall(t, a, "POST", "/api/calculator/formula", string(body)).Body.Bytes(), &formula); err != nil {
		t.Fatal(err)
	}
	if formula.Display != "1/2" {
		t.Fatal(formula)
	}
	request, _ := json.Marshal(map[string]string{"sheet": meta.Name, "start": "A1", "end": "A1000000000"})
	if response := rawArchiveCall(a, "POST", "/api/profile", string(request)); response.Code != 400 {
		t.Fatal("oversized statistics were not rejected", response.Code)
	}
	if !validLayoutSize("row", 1_000_000_000, 24) || validLayoutSize("row", 1_000_000_001, 24) {
		t.Fatal("layout ceiling drift")
	}
}
