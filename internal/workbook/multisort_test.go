package workbook

import (
	"archive/zip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMultiSortThreeKeysWithStableTies(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sort.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
 <row r="1"><c r="A1" t="inlineStr"><is><t>Group</t></is></c></row>
 <row r="2"><c r="A2" t="inlineStr"><is><t>X</t></is></c><c r="B2"><v>3</v></c><c r="C2"><v>12</v></c></row>
 <row r="3"><c r="A3" t="inlineStr"><is><t>A</t></is></c><c r="B3"><v>5</v></c><c r="C3"><v>8</v></c></row>
 <row r="4"><c r="A4" t="inlineStr"><is><t>X</t></is></c><c r="B4"><v>5</v></c><c r="C4"><v>9</v></c></row>
 <row r="5"><c r="A5" t="inlineStr"><is><t>X</t></is></c><c r="B5"><v>5</v></c><c r="C5"><v>9</v></c></row></sheetData></worksheet>`,
	}
	for name, content := range entries {
		w, e := zw.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte(content)); e != nil {
			t.Fatal(e)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	offsets, err := wb.FilterSheetWithOptions(idx, nil, FilterOptions{HeaderRow: 1, SortKeys: []SortKey{{Column: 1}, {Column: 2, Descending: true}, {Column: 3}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	numbers := []int{}
	for _, pos := range offsets {
		numbers = append(numbers, idx.ResidentRows[pos].Number)
	}
	want := []int{1, 3, 4, 5, 2}
	if !reflect.DeepEqual(numbers, want) {
		t.Fatalf("multisort got %v want %v", numbers, want)
	}
	if _, err := wb.FilterSheetWithOptions(idx, nil, FilterOptions{SortKeys: []SortKey{{Column: 1}, {Column: 1}}}, nil, nil); err == nil {
		t.Fatal("duplicate key accepted")
	}
}
