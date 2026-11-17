package export

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/workbook"
)

func TestVisualSelectionExportPreservesBlankHeadersAndSourceOrder(t *testing.T) {
	root := exportTestStorage(t)
	row4 := `<row r="4"><c r="A4" t="inlineStr"><is><t>00042</t></is></c></row>`
	row8 := `<row r="8"><c r="A8"><v>8</v></c></row>`
	path := filepath.Join(root, "rows.xml")
	if err := os.WriteFile(path, []byte(row4+row8), 0600); err != nil {
		t.Fatal(err)
	}
	idx := &workbook.SheetIndex{XMLPath: path, XMLSize: int64(len(row4 + row8)), MaxRow: 8, MaxCol: 1, Rows: []workbook.RowOffset{{Number: 4, End: int64(len(row4))}, {Number: 8, Start: int64(len(row4)), End: int64(len(row4 + row8))}}}
	s := Selection{Sheet: workbook.SheetMeta{Name: "Data"}, Index: idx, FirstRow: 2, LastRow: 8, FirstCol: 1, LastCol: 1, Filtered: true, Compact: true, RowNumbers: []int{2, 3, 8, 4}}
	wb := &workbook.Workbook{Format: "xlsx"}
	var out bytes.Buffer
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "csv", Mode: "values"}, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "\r\n\r\n8\r\n00042\r\n" {
		t.Fatalf("blank rows/order changed: %q", got)
	}
	out.Reset()
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "values"}, nil); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "selection.xlsx")
	if err := os.WriteFile(output, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := workbook.OpenWorkbook(output)
	if err != nil {
		t.Fatal(err)
	}
	index, err := reopened.BuildIndex(reopened.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.ReadRows(index, 0, 2, 1, 1)
	if err != nil || len(rows) != 2 || rows[0].Number != 3 || rows[0].Cells[0].Raw != "8" || rows[1].Number != 4 || rows[1].Cells[0].Type != "inlineStr" || rows[1].Cells[0].Raw != "00042" {
		t.Fatalf("XLSX round trip: %+v %v", rows, err)
	}
}
