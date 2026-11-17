package export

import (
	"excellens/internal/workbook"
	"strings"
	"testing"
)

func TestExternalTextExportMayExceedExcelRows(t *testing.T) {
	s := Selection{Index: &workbook.SheetIndex{XMLPath: "ledger.rows"}, FirstRow: 1, LastRow: 1048580, FirstCol: 1, LastCol: 2}
	for _, format := range []string{"csv", "tsv", "jsonl", "zipcsv"} {
		if err := Validate(Options{Format: format, Mode: "values"}, []Selection{s}); err != nil {
			t.Fatalf("%s should be permitted: %v", format, err)
		}
	}
	if err := Validate(Options{Format: "xlsx", Mode: "values"}, []Selection{s}); err == nil || !strings.Contains(err.Error(), "1,048,576") {
		t.Fatalf("XLSX row limit must be explicit: %v", err)
	}
	s.Index.XMLPath = "worksheet.xml"
	if err := Validate(Options{Format: "csv", Mode: "values"}, []Selection{s}); err == nil {
		t.Fatal("XLSX source beyond 1,048,576 must remain invalid")
	}
}

func TestBillionRowStreamingTextExportKeepsTargetCeilings(t *testing.T) {
	s := Selection{Index: &workbook.SheetIndex{XMLPath: "ledger.rows"}, FirstRow: 1, LastRow: 1_000_000_000, FirstCol: 1, LastCol: 3}
	for _, format := range []string{"csv", "tsv", "jsonl", "zipcsv"} {
		if err := Validate(Options{Format: format, Mode: "values"}, []Selection{s}); err != nil {
			t.Fatal(format, err)
		}
	}
	for _, format := range []string{"xlsx", "html"} {
		if err := Validate(Options{Format: format, Mode: "values"}, []Selection{s}); err == nil {
			t.Fatal("target ceiling ignored", format)
		}
	}
	s.LastRow++
	if err := Validate(Options{Format: "csv", Mode: "values"}, []Selection{s}); err == nil {
		t.Fatal("source ceiling ignored")
	}
}
