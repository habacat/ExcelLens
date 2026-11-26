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

func TestReadRowsSparseContextParity(t *testing.T) {
	cases := []struct {
		name, xml string
		cols      []int
	}{
		{"wide gap", `<row r="1"><c r="A1" t="inlineStr"><is><t>资产编码001</t></is></c><c r="B1"><v>99999999999999</v></c><c r="IV1" s="1"><f>SUMIF(A1:A100,"样本",B1:B100)</f><v>-0</v></c><c r="XFD1" t="s"><v>0</v></c></row>`, []int{1, 256, 16384}},
		{"implicit addresses", `<row r="2"><c t="inlineStr"><is><t>隐式A2</t></is></c><c><v>22</v></c><c r="AA2"><v>35</v></c></row>`, []int{1, 2, 27}},
		{"cdata and string", `<row r="3"><c r="A3" t="inlineStr"><is><t><![CDATA[A & B]]></t></is></c><c r="K3" t="str"><v>X&amp;Y</v></c></row>`, []int{1, 11}},
		{"empty with style", `<row r="4" s="2"><c r="B4" s="1"/><c r="Q4" t="b"><v>1</v></c></row>`, []int{2, 17}},
		{"formula no cache", `<row r="5"><c r="A5"><f>A4+B4</f></c><c r="R5" t="inlineStr"><is><t>末尾</t></is></c></row>`, []int{1, 18}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.xml")
			if err := os.WriteFile(path, []byte(tc.xml), 0600); err != nil {
				t.Fatal(err)
			}
			idx := &SheetIndex{XMLPath: path, RowIndex: NewRowIndex([]RowOffset{{Number: 1, Start: 0, End: int64(len(tc.xml))}}), XMLSize: int64(len(tc.xml)), SharedAnchors: map[string]SharedFormulaAnchor{}}
			wb := &Workbook{Shared: []string{"shared content"}, Styles: []StyleInfo{{NumFmtID: 0}, {NumFmtID: 164}, {NumFmtID: 0}}, NumFmts: map[int]string{164: `#,##0.00;[Red](#,##0.00);"-"`}}
			wide, err := wb.ReadRowsContext(context.Background(), idx, 0, 1, tc.cols[0], tc.cols[len(tc.cols)-1])
			if err != nil {
				t.Fatal(err)
			}
			got, err := wb.ReadRowsSparseContext(context.Background(), idx, []int{0}, tc.cols)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || len(wide) != 1 {
				t.Fatalf("got %d wide %d", len(got), len(wide))
			}
			keep := map[int]bool{}
			for _, col := range tc.cols {
				keep[col] = true
			}
			selected := wide[0]
			selected.Cells = nil
			for _, c := range wide[0].Cells {
				if keep[c.Col] {
					selected.Cells = append(selected.Cells, c)
				}
			}
			if !reflect.DeepEqual(got[0], selected) {
				t.Errorf("sparse mismatch:\n got %#v\nwant %#v", got[0], selected)
			}
		})
	}
}

func TestReadRowsSparseContextGuards(t *testing.T) {
	w := &Workbook{}
	idx := &SheetIndex{XMLPath: filepath.Join(t.TempDir(), "not-read.xml")}
	for _, cols := range [][]int{nil, {}, {2, 1}, {1, 1}, {-1}, {0}, {16385}, make([]int, 81)} {
		if _, err := w.ReadRowsSparseContext(context.Background(), idx, nil, cols); err == nil {
			t.Errorf("accepted invalid cols %v", cols)
		}
	}
	path := filepath.Join(t.TempDir(), "s.xml")
	if err := os.WriteFile(path, []byte(`<row r="1"><c r="A1"><v>3</v></c></row>`), 0600); err != nil {
		t.Fatal(err)
	}
	idx.XMLPath = path
	idx.RowIndex = NewRowIndex([]RowOffset{{Number: 1, Start: 0, End: int64(len(`<row r="1"><c r="A1"><v>3</v></c></row>`))}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := w.ReadRowsSparseContext(ctx, idx, []int{0}, []int{1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	_, err = w.ReadRowsSparseContext(context.Background(), idx, []int{1}, []int{1})
	if err == nil || !strings.Contains(err.Error(), "row index") {
		t.Fatalf("index validation=%v", err)
	}
}
