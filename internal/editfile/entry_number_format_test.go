package editfile

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"excellens/internal/calculator"
	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestEntryNumberFormatUsesOrderedAreaAnchorsAndDateCancellation(t *testing.T) {
	table := entryFormatTable{xfs: []entryXF{{number: 0}, {number: 164}, {number: 165}, {number: 14}, {number: 21}, {number: 9}},
		calendar: map[int]bool{164: false, 165: false}}
	for _, tc := range []struct {
		formula string
		want    int
	}{
		{"SUM(A1:A9,B1:B9,F1:F9)", 164}, {"AVERAGE(F1:F9,B1:B9)", 9},
		{"SUM(A1:F1)", 0}, {"SUM(Other!B1:B9,F1:F9)", 9}, {"SUM(Other!F1:F9,B1:B9)", 9},
		{"D1-Other!D1", 0}, {"SUM(D1:D9)", 14}, {"SUM(D1,D1)", 0}, {"D1-1", 14},
		{"B1+F1", 164}, {"F1+B1", 9}, {"B1*F1", 0}, {"SUM((B1:B3,F1:F3))", 0},
		{"SUM(E1:E3)", 46}, {"AVERAGE(E1:E3)", 21}, {"DATE(2026,7,1)-D1", 0},
		{"SUM(B1,ABS(C1*2),F1)", 9},
	} {
		f, err := calculator.ParseEditableFormula(t.Context(), "="+tc.formula, func(string) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		got, ok := table.formulaNumberFormat(f.EntryFormatOperands("Data"), func(h calculator.EntryFormatOperand) editdoc.Value {
			if h.Row != 1 {
				t.Fatal("read outside area anchor", h)
			}
			return editdoc.Value{Style: strconv.Itoa(h.Col - 1)}
		})
		id := 0
		if ok {
			id = table.effectiveNumber(got)
		}
		if id != tc.want {
			t.Errorf("%s format %d, want %d", tc.formula, id, tc.want)
		}
	}
}

// Locally generated through desktop Excel. Unsupported worksheet functions
// remain outside the ordinary editor and are counted separately.
func TestNativeFormulaEntryFormats(t *testing.T) {
	path := os.Getenv("EXCELLENS_ENTRY_FORMAT_MATRIX")
	if path == "" {
		t.Skip("native Excel format matrix not supplied")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var groups []struct {
		Sheet, Styles string
		Cells         map[string]string
		Cases         []struct {
			Formula       string
			Expected, Row int
		}
	}
	if err = json.Unmarshal(raw, &groups); err != nil {
		t.Fatal(err)
	}
	checked, unsupported, mismatches := 0, 0, 0
	for _, g := range groups {
		styles, err := parseEntryFormats(t.Context(), []byte(g.Styles))
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range g.Cases {
			f, err := calculator.ParseLocalFormula(t.Context(), strings.TrimPrefix(tc.Formula, "="), func(string) bool { return true }, func(string) (bool, bool) { return false, true })
			if err != nil {
				unsupported++
				continue
			}
			winner, ok := styles.formulaNumberFormat(f.EntryFormatOperands(g.Sheet), func(h calculator.EntryFormatOperand) editdoc.Value {
				prefix := ""
				if h.Sheet != "" {
					prefix = h.Sheet + "!"
				}
				return editdoc.Value{Style: g.Cells[prefix+workbook.ColumnName(h.Col)+strconv.Itoa(h.Row)]}
			})
			n := 0
			if ok {
				n = styles.effectiveNumber(winner)
			}
			if n != tc.Expected {
				mismatches++
				if mismatches <= 75 {
					t.Errorf("row %d %s got %d want %d", tc.Row, tc.Formula, n, tc.Expected)
				}
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("empty native format comparison")
	}
	t.Logf("compared %d formulas, %d outside the editable grammar, %d differences", checked, unsupported, mismatches)
}
