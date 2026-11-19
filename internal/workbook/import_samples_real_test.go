package workbook

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// Opt-in regression against the exact user-provided samples. Canonical hashes
// were independently computed with Python's GB18030 decoder / HTMLParser,
// including the complete dataset and all empty/merged column positions.
func TestProvidedOracleJournal(t *testing.T) {
	verifyProvidedImport(t, "EXCELLENS_ORACLE_TXT", 2907951, 39,
		"796fc065e39609bbbd6574cc6c1860bc15dc677547f27827e4fdb09aea7e58f6",
		"cea9f86b70c029cb087af4ef3a4fd423804820810724e4bc437376e6b2115846")
}

func TestProvidedOfficeHTMLXLS(t *testing.T) {
	verifyProvidedImport(t, "EXCELLENS_HTML_XLS", 63397, 14,
		"f0e13cd032dc7d0250071db91dbac21f0de4f23d783ed0311a4bd132f1a86e14",
		"881fcdd9f771bc456770e3910048b3daa019abc3a14bbbdbe5e09933ea381f87")
}

func providedImportSHA256(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func verifyProvidedImport(t *testing.T, variable string, rows, cols int, sourceSHA, canonicalSHA string) {
	t.Helper()
	path := os.Getenv(variable)
	if path == "" {
		t.Skip("requires " + variable + " pointing to the supplied fixture")
	}
	isolateTestStorage(t)
	if got := providedImportSHA256(t, path); got != sourceSHA {
		t.Fatalf("different source fixture: %s", got)
	}
	started := time.Now()
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(wb.Sheets) != 1 {
		t.Fatalf("sheet count %d", len(wb.Sheets))
	}
	t.Logf("metadata open: %s", time.Since(started))
	previewStarted := time.Now()
	preview, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 80, 40)
	if err != nil || len(preview) != 80 {
		t.Fatalf("initial preview: %d rows %v", len(preview), err)
	}
	t.Logf("initial 80-row preview after metadata open: %s; no full index required", time.Since(previewStarted))
	if variable == "EXCELLENS_ORACLE_TXT" {
		header := wb.SourceInfo.TableHeader
		if header == nil || header.Row != 1 || !header.Detected || len(header.Columns) != 39 || header.Columns[0] != "凭证来源" || header.Columns[38] != "seg12_desc" {
			t.Fatalf("Oracle automatic table header: %+v", header)
		}
		for _, cell := range preview[0].Cells {
			if cell.Raw != header.Columns[cell.Col-1] {
				t.Fatalf("automatic header and parsed R1C%d differ", cell.Col)
			}
		}
		t.Log("automatic TXT table header: row 1, 39 fields, identical to parsed source header")
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != rows || idx.RowCount() != rows || idx.MaxCol != cols {
		t.Fatalf("shape %d x %d, %d indexed rows; want %d x %d", idx.MaxRow, idx.MaxCol, idx.RowCount(), rows, cols)
	}
	t.Logf("cold open + index: %s; %d x %d; native cache %d bytes", time.Since(started), rows, cols, idx.XMLSize)
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	position := idx.RowAt(0).Start
	if _, err := f.Seek(position, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReaderSize(f, 1<<20)
	buffer, record := make([]byte, 0, 4096), make([]byte, 0, 4096)
	fields := make([]string, cols)
	h := sha256.New()
	for i, offset := range idx.RowOffsets() {
		if offset.Number != i+1 || offset.Start != position || offset.End <= offset.Start {
			t.Fatalf("noncontiguous index at row %d: %+v", i+1, offset)
		}
		size := int(offset.End - offset.Start)
		if cap(buffer) < size {
			buffer = make([]byte, size)
		}
		if _, err := io.ReadFull(br, buffer[:size]); err != nil {
			t.Fatal(err)
		}
		position = offset.End
		row, err := wb.decodeExternalRow(buffer[:size], offset.Number, 1, cols, nil, false, false)
		if err != nil {
			t.Fatal(err)
		}
		clear(fields)
		for _, cell := range row.Cells {
			fields[cell.Col-1] = cell.Raw
		}
		record = record[:0]
		for col, field := range fields {
			if col != 0 {
				record = append(record, '@')
			}
			record = append(record, field...)
		}
		record = append(record, '\n')
		_, _ = h.Write(record)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != canonicalSHA {
		t.Fatalf("complete cell/coordinate digest mismatch: got %s want %s", got, canonicalSHA)
	}
	warmStarted := time.Now()
	reopened, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	cached := reopened.GetCachedIndex(reopened.Sheets[0].Path)
	if cached == nil || cached.MaxRow != rows || cached.MaxCol != cols {
		t.Fatal("native cache could not be reopened")
	}
	if _, err := reopened.ReadRow(cached, rows-1, 1, cols); err != nil {
		t.Fatal(err)
	}
	t.Logf("warm open + persisted index load + last row: %s", time.Since(warmStarted))
	if got := providedImportSHA256(t, path); got != sourceSHA {
		t.Fatalf("source changed during read-only import: %s", got)
	}
	t.Logf("all %d rows matched independent canonical SHA-256 %s; cache reopen/last row/source hash verified; total %s", rows, canonicalSHA, time.Since(started))
}
