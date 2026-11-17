package server

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

// This test is opt-in because the user's large workbook is not distributed
// with the source code. The whole XLSM remains read-only throughout.
func TestRealWorkbookExportAndExactCellRoundTrip(t *testing.T) {
	source := os.Getenv("EXCELLENS_REAL_XLSM")
	if source == "" {
		t.Skip("set EXCELLENS_REAL_XLSM to a local XLSM workbook")
	}
	wb, err := workbook.OpenWorkbook(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 79 {
		t.Logf("working with %d sheets", len(wb.Sheets))
	}
	meta := wb.Sheets[0]
	for _, m := range wb.Sheets {
		if strings.Contains(m.Name, "最低资本EAST交易") {
			meta = m
			break
		}
	}
	start := time.Now()
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real sheet=%s rows=%d cols=%d indexing=%s", meta.Name, len(idx.Rows), idx.MaxCol, time.Since(start))
	rows, err := wb.ReadRows(idx, 0, 6, 1, 18)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("source sheet unexpectedly empty")
	}
	s := excelExport.Selection{Sheet: meta, Index: idx, FirstRow: 1, LastRow: 6, FirstCol: 1, LastCol: 18, Compact: true}
	for _, typ := range []string{"xlsx", "csv", "tsv", "jsonl", "html"} {
		t.Run(typ, func(t *testing.T) {
			var buffer bytes.Buffer
			opts := excelExport.Options{Format: typ, Mode: "values", Styles: true, BOM: true, ProtectCSV: true}
			begin := time.Now()
			err := excelExport.Write(context.Background(), &buffer, wb, []excelExport.Selection{s}, opts, nil)
			if err != nil {
				t.Fatal(err)
			}
			if buffer.Len() == 0 {
				t.Fatal("export is empty")
			}
			t.Logf("format=%s bytes=%d duration=%s", typ, buffer.Len(), time.Since(begin))
			if typ != "xlsx" {
				return
			}
			path := filepath.Join(t.TempDir(), "test.xlsx")
			if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			z, err := zip.OpenReader(path)
			if err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, f := range z.File {
				names[f.Name] = true
			}
			z.Close()
			for _, name := range []string{"xl/styles.xml", "xl/workbook.xml", "xl/worksheets/sheet1.xml"} {
				if !names[name] {
					t.Fatalf("required part not present: %s", name)
				}
			}
			out, err := workbook.OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			m, ok := out.FindSheet(meta.Name)
			if !ok {
				t.Fatal("exported sheet not found")
			}
			ix, err := out.BuildIndex(m, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := out.ReadRows(ix, 0, 6, 1, 18)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(rows) {
				t.Fatalf("row count mismatch %d vs %d", len(got), len(rows))
			}
			for i, original := range rows {
				if len(original.Cells) != len(got[i].Cells) {
					t.Fatalf("row %d cells mismatch %d vs %d", i, len(original.Cells), len(got[i].Cells))
				}
				for j, cell := range original.Cells {
					output := got[i].Cells[j]
					if cell.Raw != output.Raw || cell.Style != output.Style {
						t.Fatalf("row %d col %d mismatch: source raw=%q style=%d; output raw=%q style=%d", i, j, cell.Raw, cell.Style, output.Raw, output.Style)
					}
				}
			}
		})
	}
}

// Optional large export benchmark verifies that no browser-side array is
// needed for the 10,000-row, 884-column real worksheet.
func TestRealLargeSheetFullExportStreaming(t *testing.T) {
	source := os.Getenv("EXCELLENS_REAL_XLSM")
	if source == "" {
		t.Skip("set EXCELLENS_REAL_XLSM to a local XLSM workbook")
	}
	wb, err := workbook.OpenWorkbook(source)
	if err != nil {
		t.Fatal(err)
	}
	var meta workbook.SheetMeta
	found := false
	for _, m := range wb.Sheets {
		if strings.Contains(m.Name, "最低资本EAST交易") {
			meta = m
			found = true
			break
		}
	}
	if !found {
		t.Skip("large EAST sheet not found")
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "streaming-full-sheet.xlsx")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	selection := excelExport.Selection{Sheet: meta, Index: idx, FirstRow: 1, LastRow: idx.MaxRow, FirstCol: 1, LastCol: idx.MaxCol}
	started := time.Now()
	var progress int64
	err = excelExport.Write(context.Background(), out, wb, []excelExport.Selection{selection}, excelExport.Options{Format: "xlsx", Mode: "values", Styles: true}, func(done, total int64, stage string) { progress = done })
	closeErr := out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	st, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("full real EAST export: rows=%d columns=%d done=%d bytes=%d elapsed=%s", idx.MaxRow, idx.MaxCol, progress, st.Size(), time.Since(started))
	if progress != int64(idx.MaxRow) || st.Size() < 1024 {
		t.Fatal("export incomplete")
	}
	opened, err := workbook.OpenWorkbook(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Sheets) != 1 {
		t.Fatalf("exported sheet count=%d", len(opened.Sheets))
	}
}
