package workbook

import (
	"strings"
	"testing"
)

func TestUsedExtentIgnoresStyledBlankTail(t *testing.T) {
	source := `<worksheet><dimension ref="A1:XFD1048576"/><sheetData>` +
		`<row r="1"><c r="A1" t="inlineStr"><is><t>Title</t></is></c><c r="D1" s="3" /></row>` +
		`<row r="5"><c r="B5"><f>SUM(A1:A4)</f></c></row>` +
		`<row r="1000000"><c r="XFD1000000" s="23"/><c r="XFC1000000" s="23"><v></v></c></row>` +
		`</sheetData></worksheet>`
	rows, _, dimension, maxRow, maxCol, err := scanWorksheet(strings.NewReader(source), int64(len(source)), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dimension != "A1:XFD1048576" || maxRow != 5 || maxCol != 2 || len(rows) != 2 {
		t.Fatalf("trailing styled blanks not ignored: %s %d %d rows=%d", dimension, maxRow, maxCol, len(rows))
	}
}
func TestUsedExtentKeepsZeroValuesAndSharedFormulas(t *testing.T) {
	source := `<worksheet><sheetData><row r="1"><c r="B1"><v>0</v></c></row><row r="100"><c r="G100"><f t="shared" si="1"/></c></row></sheetData></worksheet>`
	_, _, _, maxRow, maxCol, err := scanWorksheet(strings.NewReader(source), int64(len(source)), nil, nil)
	if err != nil || maxRow != 100 || maxCol != 7 {
		t.Fatalf("lost zero/formula: row=%d col=%d error=%v", maxRow, maxCol, err)
	}
}
