package export

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func TestFormulaExportPreservesCachedTypes(t *testing.T) {
	root := exportTestStorage(t)
	data := `<row r="1"><c r="A1" t="b"><f>1=1</f><v>1</v></c><c r="B1" t="b"><f>1=2</f><v>0</v></c><c r="C1" t="str"><f>"00012"</f><v>00012</v></c><c r="D1" t="e"><f>1/0</f><v>#DIV/0!</v></c><c r="E1"><f>1+1</f><v>2</v></c><c r="F1"><f>1+2</f></c></row>`
	path := filepath.Join(root, "row.xml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	idx := &workbook.SheetIndex{XMLPath: path, XMLSize: int64(len(data)), MaxRow: 1, MaxCol: 6, RowIndex: workbook.NewRowIndex([]workbook.RowOffset{{Number: 1, End: int64(len(data))}})}
	s := Selection{Sheet: workbook.SheetMeta{Name: "Data"}, Index: idx, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 6}
	wb := &workbook.Workbook{Format: "xls"}
	var out bytes.Buffer
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "formula"}, nil); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	f, err := z.Open("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	xml, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, cached := range []string{`t="b"><f>1=1</f><v>1</v>`, `t="b"><f>1=2</f><v>0</v>`} {
		if !strings.Contains(string(xml), cached) {
			t.Fatalf("invalid boolean formula cache: %s", xml)
		}
	}
	file := filepath.Join(root, "formula.xlsx")
	if err := os.WriteFile(file, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	index, err := reopened.BuildIndex(reopened.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	row, err := reopened.ReadRowAll(index, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ typ, raw string }{{"b", "TRUE"}, {"b", "FALSE"}, {"str", "00012"}, {"e", "#DIV/0!"}, {"", "2"}, {"", ""}}
	for i, expected := range want {
		cell := row.Cells[i]
		if cell.Type != expected.typ || cell.Raw != expected.raw || !cell.HasFormula || cell.CacheMissing != (i == 5) {
			t.Fatalf("cached type changed at %s: %+v", cell.Ref, cell)
		}
	}
}

func TestFormulaExportRejectsUnresolvedOriginal(t *testing.T) {
	root := exportTestStorage(t)
	data := `<row r="1"><c r="A1"><f t="shared" si="8"/><v>12</v></c></row>`
	path := filepath.Join(root, "row.xml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	idx := &workbook.SheetIndex{XMLPath: path, XMLSize: int64(len(data)), MaxRow: 1, MaxCol: 1, RowIndex: workbook.NewRowIndex([]workbook.RowOffset{{Number: 1, End: int64(len(data))}})}
	s := Selection{Sheet: workbook.SheetMeta{Name: "Data"}, Index: idx, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 1}
	wb := &workbook.Workbook{Format: "xls"}
	var out bytes.Buffer
	err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "formula"}, nil)
	if err == nil || !strings.Contains(err.Error(), "缓存值导出") || !strings.Contains(err.Error(), "Data!A1") {
		t.Fatal("unresolved formula must not become an XLSX expression", err)
	}
	out.Reset()
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "values"}, nil); err != nil {
		t.Fatal("cached value export should remain available", err)
	}
}
