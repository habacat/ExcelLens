package workbook

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanWorksheetOffsetsAndSharedFormula(t *testing.T) {
	xmlData := `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<!-- <row r="99"/> -->
<dimension ref="A1:C3"/><sheetData>
<row r="1"><c r="A1"><v>1</v></c><c r="C1"><f t="shared" si="7" ref="C1:C3">A1&amp;B1</f><v>1</v></c></row>
<row r="2"><c r="A2"><v>2</v></c><c r="C2"><f t="shared" si="7"/><v>2</v></c></row>
<row r="3"/>
</sheetData></worksheet>`
	rows, anchors, dimension, maxRow, maxCol, err := scanWorksheet(strings.NewReader(xmlData), int64(len(xmlData)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 content rows, got %d", len(rows))
	}
	if dimension != "A1:C3" || maxRow != 2 || maxCol != 3 {
		t.Fatalf("unexpected dimension: %q maxRow=%d maxCol=%d", dimension, maxRow, maxCol)
	}
	if got := string(anchors["7"].Formula); got != "A1&B1" {
		t.Fatalf("unexpected shared formula: %q", got)
	}
	for _, row := range rows {
		if row.Start < 0 || row.End <= row.Start || row.End > int64(len(xmlData)) {
			t.Fatalf("invalid row offset: %+v", row)
		}
		if !bytes.Contains([]byte(xmlData[row.Start:row.End]), []byte("<row")) {
			t.Fatalf("row offset does not point at row: %+v", row)
		}
	}
}

func TestOpenBuildIndexReadRowsAndSearch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "mini.xlsx")
	writeTinyWorkbook(t, file)
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	meta, ok := wb.FindSheet("Data")
	if !ok {
		t.Fatal("Data sheet not found")
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.ResidentRows) != 3 {
		t.Fatalf("want 3 indexed rows, got %d", len(idx.ResidentRows))
	}
	rows, err := wb.ReadRows(idx, 0, 3, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := rows[0].Cells[0].Display; got != "Hello" {
		t.Fatalf("A1 display = %q", got)
	}
	if got := rows[0].Cells[1].Display; got != "1,234.50" {
		t.Fatalf("B1 display = %q", got)
	}
	if got := rows[1].Cells[1].Display; got != "(1,234.50)" {
		t.Fatalf("B2 display = %q", got)
	}
	if got := rows[2].Cells[1].Display; got != "2025-09-30" {
		t.Fatalf("B3 date display = %q", got)
	}
	if got := rows[1].Cells[2].Formula; got != "=A2+B2" {
		t.Fatalf("C2 shared formula = %q", got)
	}
	if got := rows[2].Cells[2].Formula; got != "=A3+B3" {
		t.Fatalf("C3 shared formula = %q", got)
	}
	matches, truncated, err := wb.SearchSheet(idx, "Data", "A2+B2", "formula", 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(matches) != 1 || matches[0].Ref != "C2" {
		t.Fatalf("unexpected search result: %+v truncated=%v", matches, truncated)
	}
	refs, refTruncated, err := wb.SearchSheet(idx, "Data", "A2", "reference", 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refTruncated || len(refs) != 1 || refs[0].Ref != "C2" {
		t.Fatalf("unexpected reference search result: %+v truncated=%v", refs, refTruncated)
	}

	qualifiedRefs, qualifiedTruncated, err := wb.SearchSheet(idx, "Data", "'Data'!A2", "reference", 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if qualifiedTruncated || len(qualifiedRefs) != 1 || qualifiedRefs[0].Ref != "C2" {
		t.Fatalf("qualified reference search failed: %+v", qualifiedRefs)
	}

	wb2, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	cached := wb2.GetCachedIndex(meta.Path)
	if cached == nil || len(cached.ResidentRows) != 3 {
		t.Fatal("expected reusable local index cache")
	}
}

func TestStripFormulaStringLiterals(t *testing.T) {
	got := stripFormulaStringLiterals(`=IF(A1="A1",$B$2,0)`)
	if strings.Contains(got, `"A1"`) {
		t.Fatalf("string literal was not removed: %q", got)
	}
	if !strings.Contains(got, "A1") || !strings.Contains(got, "$B$2") {
		t.Fatalf("cell references were removed: %q", got)
	}
}

func TestFormatCustomNumber(t *testing.T) {
	cases := []struct {
		value        float64
		format, want string
	}{
		{1234.5, `#,##0.00"元"`, "1,234.50元"},
		{-1234.5, `#,##0.00;[Red](#,##0.00);-`, "(1,234.50)"},
		{0, `#,##0.00;[Red](#,##0.00);-`, "-"},
		{0.125, `0.00%`, "12.50%"},
		{1234.5, `[$¥-804]#,##0.00`, "¥1,234.50"},
	}
	for _, tc := range cases {
		if got := formatCustomNumber(tc.value, tc.format, false); got != tc.want {
			t.Errorf("format %q value %v: got %q want %q", tc.format, tc.value, got, tc.want)
		}
	}
}

func TestParseCellRef(t *testing.T) {
	col, row, err := ParseCellRef("$AGZ$10000")
	if err != nil {
		t.Fatal(err)
	}
	if col != 884 || row != 10000 {
		t.Fatalf("got col=%d row=%d", col, row)
	}
	if got := ColumnName(884); got != "AGZ" {
		t.Fatalf("column name = %q", got)
	}
}

func writeTinyWorkbook(t *testing.T, file string) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entries := map[string]string{
		"xl/workbook.xml":            `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><bookViews><workbookView activeTab="0"/></bookViews><sheets><sheet name="Data" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="1" uniqueCount="1"><si><t>Hello</t></si></sst>`,
		"xl/styles.xml":              `<?xml version="1.0"?><styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0.00;[Red](#,##0.00);-"/></numFmts><cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="164"/><xf numFmtId="14"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:C3"/><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" s="1"><v>1234.5</v></c><c r="C1"><f t="shared" si="0" ref="C1:C3">A1+B1</f><v>1234.5</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>Text</t></is></c><c r="B2" s="1"><v>-1234.5</v></c><c r="C2"><f t="shared" si="0"/><v>1</v></c></row><row r="3"><c r="A3"><v>0</v></c><c r="B3" s="2"><v>45930</v></c><c r="C3"><f t="shared" si="0"/><v>2</v></c></row></sheetData></worksheet>`,
	}
	for name, content := range entries {
		writer, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTranslateSharedFormulaRelativeAndAbsoluteReferences(t *testing.T) {
	formula := `SUM(A1,$B1,C$1,$D$1,'Sheet 1'!A1,"A1")`
	got := translateSharedFormula(formula, "C2", "D3")
	want := `SUM(B2,$B2,D$1,$D$1,'Sheet 1'!B2,"A1")`
	if got != want {
		t.Fatalf("translated formula:\n got %s\nwant %s", got, want)
	}
}

func TestTranslateSharedFormulaOutOfBounds(t *testing.T) {
	got := translateSharedFormula("=A1", "B1", "A1")
	if got != "=#REF!" {
		t.Fatalf("got %q", got)
	}
}

func TestTranslateSharedFormulaWholeAxes(t *testing.T) {
	formula := `SUM('Q1'!$A:B,$1:2,Sheet1!b:b,"A:B",Table1[Q1:Q2],Jan:Mar!B3)+费率A1`
	want := `SUM('Q1'!$A:E,$1:4,Sheet1!E:E,"A:B",Table1[Q1:Q2],Jan:Mar!E5)+费率A1`
	if got := translateSharedFormula(formula, "B2", "E4"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestCustomDateAndElapsedTimeFormats(t *testing.T) {
	date := excelSerialTime(45930.5, false)
	if got := formatCustomDate(45930.5, `m/d/yy h:mm:ss AM/PM`, false); got != "9/30/25 12:00:00 PM" {
		t.Fatalf("date/time format = %q (date %s)", got, date.Format("2006-01-02 15:04:05"))
	}
	if got := formatCustomDate(1.5, `[h]:mm:ss`, false); got != "36:00:00" {
		t.Fatalf("elapsed time format = %q", got)
	}
}
