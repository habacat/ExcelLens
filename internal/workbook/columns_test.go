package workbook

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadHiddenColumnsSourceMetadata(t *testing.T) {
	cases := []struct {
		name, xml string
		want      []HiddenColumnRange
		fail      bool
	}{
		{"none", `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData></worksheet>`, nil, false},
		{"single", `<worksheet><cols><col min="3" max="3" hidden="1"/></cols><sheetData/></worksheet>`, []HiddenColumnRange{{3, 3}}, false},
		{"wide and merged", `<worksheet><cols><col min="2" max="80" hidden="true"/><col min="81" max="120" hidden="1"/></cols><sheetData/></worksheet>`, []HiddenColumnRange{{2, 120}}, false},
		{"overlap reveals", `<worksheet><cols><col min="1" max="4" hidden="1"/><col min="2" max="3" hidden="0"/></cols><sheetData/></worksheet>`, []HiddenColumnRange{{1, 1}, {4, 4}}, false},
		{"overlap hides", `<worksheet><cols><col min="1" max="5" hidden="0"/><col min="3" max="4" hidden="1"/></cols><sheetData/></worksheet>`, []HiddenColumnRange{{3, 4}}, false},
		{"highest boundary", `<worksheet><cols><col min="16384" max="16384" hidden="1"/></cols><sheetData/></worksheet>`, []HiddenColumnRange{{16384, 16384}}, false},
		{"not a col outside cols", `<worksheet><col min="1" max="2" hidden="1"/><sheetData/></worksheet>`, nil, false},
		{"invalid missing max", `<worksheet><cols><col min="1" hidden="1"/></cols><sheetData/></worksheet>`, nil, true},
		{"invalid zero", `<worksheet><cols><col min="0" max="5" hidden="1"/></cols><sheetData/></worksheet>`, nil, true},
		{"invalid reversed", `<worksheet><cols><col min="4" max="2" hidden="1"/></cols><sheetData/></worksheet>`, nil, true},
		{"invalid high", `<worksheet><cols><col min="1" max="16385" hidden="1"/></cols><sheetData/></worksheet>`, nil, true},
		{"noninteger", `<worksheet><cols><col min="X" max="2" hidden="1"/></cols><sheetData/></worksheet>`, nil, true},
		{"malformed xml", `<worksheet><cols><col min="1"`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sheet.xml")
			if err := os.WriteFile(path, []byte(tc.xml), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadHiddenColumns(path)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v fail=%v", err, tc.fail)
			}
			if !tc.fail && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestReadHiddenColumnsIgnoresHugeSheetBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sheet.xml")
	content := `<worksheet><cols><col min="4" max="7" hidden="1"/></cols><sheetData>` + strings.Repeat("<row/>", 30000) + `</sheetData></worksheet>`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadHiddenColumns(path)
	if err != nil || !reflect.DeepEqual(got, []HiddenColumnRange{{4, 7}}) {
		t.Fatalf("ranges=%v error=%v", got, err)
	}
}
