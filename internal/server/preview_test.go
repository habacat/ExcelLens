package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/workbook"
)

func TestTextViewportCanReachRowsBeyondExcelLimit(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "long.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("ID@amount\n")
	_, _ = f.Write(bytes.Repeat([]byte("x@1\n"), 1048577))
	_, _ = f.WriteString("FINAL_ROW@2.345678\n")
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err := app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	preview := decodeResponse[struct {
		Partial, Available bool
		Rows               []workbook.RowData
	}](t, featureCall(t, app, "GET", "/api/preview?sheet=数据", ""))
	if !preview.Partial || !preview.Available || len(preview.Rows) != 80 {
		t.Fatal("initial preview missing")
	}
	wb := app.currentWorkbook()
	if wb.GetCachedIndex(wb.Sheets[0].Path) != nil {
		t.Fatal("preview waited for full indexing")
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 1048579 {
		t.Fatal(idx.MaxRow)
	}
	result := decodeResponse[struct{ Rows []workbook.RowData }](t, featureCall(t, app, "GET", "/api/sheet?sheet=数据&row=1048579&limit=1&cols=2", ""))
	if len(result.Rows) != 1 || result.Rows[0].Number != 1048579 || result.Rows[0].Cells[0].Raw != "FINAL_ROW" {
		t.Fatalf("large row was clamped: %+v", result.Rows)
	}
}
