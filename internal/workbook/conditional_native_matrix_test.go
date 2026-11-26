package workbook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConditionalNativeExcelMatrix(t *testing.T) {
	dir := os.Getenv("EXCELLENS_CONDITIONAL_NATIVE")
	if dir == "" {
		dir = filepath.Join("testdata", "conditional-native")
	}
	isolateTestStorage(t)
	var fixtures []struct {
		Epoch string
		Cases []struct {
			Ref, Group, Formula string
			Highlight           bool
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 2 || fixtures[0].Epoch != "1900" || fixtures[1].Epoch != "1904" {
		t.Fatal("both Excel date systems are required")
	}
	for _, fixture := range fixtures {
		if len(fixture.Cases) < 68 {
			t.Fatal("native color matrix incomplete", fixture.Epoch, len(fixture.Cases))
		}
		for _, ext := range []string{"xlsx", "xlsb"} {
			t.Run(fixture.Epoch+"-"+ext, func(t *testing.T) {
				w, err := OpenWorkbook(filepath.Join(dir, fixture.Epoch+"."+ext))
				if err != nil {
					t.Fatal(err)
				}
				if w.Date1904 != (fixture.Epoch == "1904") {
					t.Fatal("unexpected workbook date system")
				}
				sheet, ok := w.FindSheet("Checks")
				if !ok {
					t.Fatal("missing Checks")
				}
				idx, err := w.BuildIndex(sheet, nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				rows, err := w.ReadRowsWithOptionsContext(context.Background(), idx, 0, idx.RowCount(), 6, 7, RowReadOptions{ConditionalFormats: true, FormatValue: true})
				if err != nil {
					t.Fatal(err)
				}
				cells := map[string]CellData{}
				for _, r := range rows {
					for _, c := range r.Cells {
						cells[c.Ref] = c
					}
				}
				for _, test := range fixture.Cases {
					c, ok := cells[test.Ref]
					if !ok {
						t.Errorf("missing %s", test.Ref)
						continue
					}
					notice := ""
					hit := false
					if v := c.Conditional; v != nil {
						notice = v.Notice
						hit = v.Visual != nil && strings.EqualFold(v.Visual.Background, "#00FF00")
					}
					if hit != test.Highlight || notice != "" {
						t.Errorf("%s [%s] %s: want %t, got %t, notice %q", test.Ref, test.Group, test.Formula, test.Highlight, hit, notice)
					}
				}
				t.Logf("%d native Excel cell observations", len(fixture.Cases))
			})
		}
	}
}
