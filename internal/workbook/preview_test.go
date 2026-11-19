package workbook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInitialPreviewPreservesNativeValuesWithoutCreatingIndex(t *testing.T) {
	isolateTestStorage(t)
	header, records := oracleJournalFixture()
	for _, tc := range []struct{ name, body string }{{"oracle.txt", header + strings.Join(records, "\n") + "\n"}, {"wps.xls", officeHTMLFixture}, {"ledger.csv", "ID,amount,credit\n00001234567890123456,1234567890.123456,\n"}} {
		path := filepath.Join(t.TempDir(), tc.name)
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		wb, err := OpenWorkbook(path)
		if err != nil {
			t.Fatal(err)
		}
		preview, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 3, 40)
		if err != nil {
			t.Fatal(err)
		}
		if wb.GetCachedIndex(wb.Sheets[0].Path) != nil {
			t.Fatal("preview built a full index")
		}
		idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		full, err := wb.ReadRows(idx, 0, min(3, len(idx.ResidentRows)), 1, 40)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(preview, full) {
			t.Fatalf("%s preview changed fields/coordinates: %+v / %+v", tc.name, preview, full)
		}
	}
}

func TestXLSXPreviewValuesStylesAndFormulasMatchIndexedRead(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "mini.xlsx")
	writeTinyWorkbook(t, path)
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 80, 40)
	if err != nil {
		t.Fatal(err)
	}
	if wb.GetCachedIndex(wb.Sheets[0].Path) != nil {
		t.Fatal("preview wrote an index")
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range preview {
		at := idx.FindRowOffset(row.Number)
		if at >= len(idx.ResidentRows) || idx.ResidentRows[at].Number != row.Number {
			continue
		}
		full, err := wb.ReadRows(idx, at, 1, 1, 40)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(row, full[0]) {
			t.Fatalf("row %d: preview %+v full %+v", row.Number, row, full[0])
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := wb.PreviewRows(ctx, wb.Sheets[0], 80, 40); !errors.Is(err, context.Canceled) {
		t.Fatal("preview ignored cancellation", err)
	}
}

func TestPreviewDoesNotHideLaterSourceErrors(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "broken.csv")
	if err := os.WriteFile(path, []byte("ID,amount\n"+strings.Repeat("0001,1.23\n", 100)+"\"unterminated,2"), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.PreviewRows(context.Background(), wb.Sheets[0], 80, 40)
	if err != nil || len(rows) != 80 {
		t.Fatal(len(rows), err)
	}
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err == nil {
		t.Fatal("preview was mistaken for complete validated data")
	}
}
