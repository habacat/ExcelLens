package workbook

import (
	"context"
	"testing"

	"excellens/internal/formats"
)

func FuzzPrintGeometryXML(f *testing.F) {
	for _, source := range []string{`<worksheet><sheetData/></worksheet>`, `<worksheet><sheetFormatPr defaultRowHeight="15" baseColWidth="8"/><sheetData><row r="1" ht="0.1"/><row r="3" hidden="1"/></sheetData></worksheet>`} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > formats.PrintTextBytes {
			return
		}
		index, err := scanPrintGeometryFixture(source)
		if err != nil {
			if index != nil {
				t.Fatal("partial geometry")
			}
			return
		}
		if index.BudgetBytes() > formats.PrintGeometryBytes {
			t.Fatal("unbounded geometry")
		}
		for _, axis := range []string{"row", "col"} {
			for _, rangePair := range [][2]int{{1, 1}, {1, 100}, {2, 50}} {
				value, err := index.Extent(context.Background(), axis, rangePair[0], rangePair[1])
				if err == nil && value.Units < 0 {
					t.Fatal("negative physical extent")
				}
			}
		}
	})
}

func FuzzPrintGeometryMeasure(f *testing.F) {
	for _, source := range []string{"0", "1e-1000000000", "9000000000", "0.0000000001", "1.005", "1e19", "NaN"} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > formats.PrintGeometryMeasureBytes+1 {
			return
		}
		for _, unit := range []string{"point", "twip", "base-char", "stored-char", "biff-col256"} {
			units, status, reason := printGeometryMeasure(source, unit, 7)
			if units < 0 {
				t.Fatal("negative geometry")
			}
			if status != "stored" && reason == "" {
				t.Fatal("missing uncertainty reason")
			}
		}
	})
}
