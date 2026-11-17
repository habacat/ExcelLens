package workbook

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRealInspectLargeSheet(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("set EXCELLENS_REAL_XLSM to test original workbook")
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	var sheet SheetMeta
	for _, sh := range wb.Sheets {
		if strings.Contains(sh.Name, "最低资本EAST交易") {
			sheet = sh
			break
		}
	}
	if sheet.Name == "" {
		t.Skip("large sheet not found")
	}
	started := time.Now()
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("index %s, actual rows=%d, used=%d x %d, XML %.0f MB", time.Since(started), len(idx.Rows), idx.MaxRow, idx.MaxCol, float64(idx.XMLSize)/1e6)
	for _, opt := range []InspectOptions{
		{Mode: "duplicates", FirstRow: 2, LastRow: idx.MaxRow, FirstCol: 2, LastCol: 2, Limit: 800},
		{Mode: "formulas", FirstRow: 1, LastRow: idx.MaxRow, FirstCol: 1, LastCol: idx.MaxCol, Limit: 800},
		{Mode: "errors", FirstRow: 1, LastRow: idx.MaxRow, FirstCol: 1, LastCol: idx.MaxCol, Limit: 800},
	} {
		started = time.Now()
		res, err := wb.InspectSelection(context.Background(), idx, opt)
		if err != nil {
			t.Fatalf("%s: %v", opt.Mode, err)
		}
		t.Logf("mode=%s matched=%d returned=%d truncated=%t duration=%s", opt.Mode, res.Matched, len(res.Matches), res.Truncated, time.Since(started))
	}
}
