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

func TestHTMLDocumentXLSXExportKeepsOneSheetMergesAndLiteralValues(t *testing.T) {
	root := exportTestStorage(t)
	path := filepath.Join(root, "ledger.xls")
	original := []byte(`<html><table><tr><td colspan="2"><b>标题</b></td></tr></table><p><br></p><table><tr><td>编号</td><td>金额</td></tr><tr><td>=&quot;000000000000000001&quot;</td><td>1,234.500001</td></tr></table></html>`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := Selection{Sheet: wb.Sheets[0], Index: idx, FirstRow: 1, LastRow: 4, FirstCol: 1, LastCol: 2}
	order, err := workbook.NewRowOrder(context.Background(), t.TempDir(), idx.RowCount(), []int{3})
	if err != nil {
		t.Fatal(err)
	}
	defer order.Close()
	for _, tc := range []struct {
		name          string
		s             Selection
		styles, merge bool
	}{
		{"full", base, true, true},
		{"plain", base, false, false},
		{"range", Selection{Sheet: base.Sheet, Index: idx, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 2, Compact: true}, true, true},
		{"cut", Selection{Sheet: base.Sheet, Index: idx, FirstRow: 1, LastRow: 4, FirstCol: 2, LastCol: 2, Compact: true}, true, false},
		{"filtered", Selection{Sheet: base.Sheet, Index: idx, FirstRow: 1, LastRow: 4, FirstCol: 1, LastCol: 2, Compact: true, Filtered: true, Order: order}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := Write(context.Background(), &out, wb, []Selection{tc.s}, Options{Format: "xlsx", Mode: "values", Styles: tc.styles}, nil); err != nil {
				t.Fatal(err)
			}
			zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range zr.File {
				if entry.Name == "xl/worksheets/sheet1.xml" {
					r, err := entry.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(r)
					r.Close()
					if err != nil {
						t.Fatal(err)
					}
					has := strings.Contains(string(data), `<mergeCell ref="A1:B1"/>`)
					if has != tc.merge {
						t.Fatalf("merge mismatch: %s", data)
					}
				}
			}
			if tc.name == "full" {
				target := filepath.Join(root, "reopened.xlsx")
				if err := os.WriteFile(target, out.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
				opened, err := workbook.OpenWorkbook(target)
				if err != nil || len(opened.Sheets) != 1 {
					t.Fatal("worksheet count changed", err)
				}
				indexed, err := opened.BuildIndex(opened.Sheets[0], nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				row, err := opened.ReadRow(indexed, indexed.FindRowOffset(4), 1, 2)
				if err != nil || row.Number != 4 || len(row.Cells) != 2 || row.Cells[0].Raw != "000000000000000001" || row.Cells[1].Raw != "1,234.500001" {
					t.Fatal("export values changed", row, err)
				}
			}
		})
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, original) {
		t.Fatal("source XLS changed")
	}
}
