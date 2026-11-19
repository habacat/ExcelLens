package workbook

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFastViewportDecoderParity(t *testing.T) {
	cases := []string{
		`<row r="1" s="1"><c r="A1" s="1"><v>123.4</v></c><c r="B1" t="s"><v>0</v></c><c r="C1"><f>SUM(A1,B1)</f><v>123</v></c><c r="D1" s="1"/><c r="F1" t="inlineStr"><is><r><t>Good</t></r><r><t>&amp;More</t></r></is></c><c r="G1" t="b"><v>1</v></c></row>`,
		`<row r="2"><c r="A2"><f t="shared" si="1" ref="A2:A3">A1*2</f><v>6</v></c><c r="B2" t="e"><v>#VALUE!</v></c><c r="H2" t="inlineStr"><is><t xml:space="preserve">  left </t></is></c></row>`,
		`<row r="3"><c r="A3"><f t="shared" si="1"/><v>8</v></c><c r="B3" t="str"><v>hello &amp; goodbye</v></c><c r="I3" s="1"/></row>`,
		`<row r="4"/>`,
		"<row r=\"5\"><c r=\"A5\" t=\"str\"><v>line1\r\nline2</v></c><c r=\"B5\" t=\"str\"><v>line1&#13;line2</v></c></row>",
		"<row r=\"6\"><c r=\"F6\" t=\"inlineStr\"><is><t>first\r\nsecond</t></is></c><c r=\"G6\"><f>SUM(A1,\r\nB2)</f><v>25</v></c></row>",
	}
	path := filepath.Join(t.TempDir(), "sheet.xml")
	var text strings.Builder
	idx := &SheetIndex{XMLPath: path, SharedAnchors: map[string]SharedFormulaAnchor{"1": {Ref: "A2", Formula: "A1*2"}}}
	for i, line := range cases {
		begin := int64(text.Len())
		text.WriteString(line)
		idx.resident = append(idx.resident, RowOffset{Number: i + 1, Start: begin, End: int64(text.Len())})
	}
	if err := os.WriteFile(path, []byte(text.String()), 0600); err != nil {
		t.Fatal(err)
	}
	idx.XMLSize = int64(text.Len())
	wb := &Workbook{Styles: make([]StyleInfo, 3), Shared: []string{"SharedString"}, NumFmts: map[int]string{}}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, cols := range [][2]int{{1, 15}, {1, 1}, {2, 4}, {6, 10}} {
		var buffer []byte
		for i := range cases {
			want, err := wb.readRowFromFile(f, idx, i, cols[0], cols[1])
			if err != nil {
				t.Fatal(err)
			}
			got, err := wb.fastReadRowAt(f, idx, i, cols[0], cols[1], &buffer)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("row %d col %d-%d differ: fast=%+v legacy=%+v", i+1, cols[0], cols[1], got, want)
			}
		}
	}
}

func TestRealViewportDecoderParity(t *testing.T) {
	path := os.Getenv("EXCELLENS_REAL_XLSM")
	if path == "" {
		t.Skip("set EXCELLENS_REAL_XLSM for large real-world parity test")
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	var target SheetMeta
	for _, s := range wb.Sheets {
		if strings.TrimSpace(s.Name) == "最低资本EAST交易和资金流水表" {
			target = s
			break
		}
	}
	if target.Name == "" {
		t.Skip("target sheet missing")
	}
	idx, err := wb.BuildIndex(target, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sample := []int{0, 1, 9, 30, 100, 235, 500, 1000, idx.RowCount() - 1}
	// Exercise numerous independent physical rows without depending on the
	// workbook's ordering, including distant cached-formula and style areas.
	for k := 0; k < 128 && idx.RowCount() > 0; k++ {
		sample = append(sample, (17+k*7849)%idx.RowCount())
	}
	var buffer []byte
	for _, i := range sample {
		if i < 0 || i >= idx.RowCount() {
			continue
		}
		for _, cols := range [][2]int{{1, 35}, {200, 235}, {500, 535}, {800, 835}} {
			legacy, err := wb.readRowFromFile(f, idx, i, cols[0], cols[1])
			if err != nil {
				t.Fatal(err)
			}
			fast, err := wb.fastReadRowAt(f, idx, i, cols[0], cols[1], &buffer)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fast, legacy) {
				if fast.Number != legacy.Number || fast.Style != legacy.Style || len(fast.Cells) != len(legacy.Cells) {
					t.Fatalf("mismatch at %s row %d columns %d-%d fast-header=(%d,%d,%d), legacy-header=(%d,%d,%d)", target.Name, i, cols[0], cols[1], fast.Number, fast.Style, len(fast.Cells), legacy.Number, legacy.Style, len(legacy.Cells))
				}
				for j := range fast.Cells {
					if !reflect.DeepEqual(fast.Cells[j], legacy.Cells[j]) {
						t.Fatalf("mismatch at %s row %d columns %d-%d cell %d: fast=%+v legacy=%+v", target.Name, i, cols[0], cols[1], j, fast.Cells[j], legacy.Cells[j])
					}
				}
			}
		}
	}
}

// The filtering/sorting fast path must agree with the strict XML decoder for
// sparse, widely separated columns and the different formula/cache modes.
func TestFastSparseColumnsParity(t *testing.T) {
	content := []string{
		`<row r="1"><c r="A1" t="inlineStr"><is><t>资产名称</t></is></c><c r="H1"><v>12</v></c><c r="N1"><f>SUM(A1:H1)</f><v>18</v></c></row>`,
		`<row r="3" s="1"><c r="A3" t="s"><v>0</v></c><c r="B3" t="inlineStr"><is><t>跳过</t></is></c><c r="H3"><v>0</v></c><c r="N3" s="2"><f t="shared" si="6"/><v>19.5</v></c></row>`,
		`<row r="20"><c r="D20" t="inlineStr"><is><t>不需要</t></is></c><c r="N20" t="inlineStr"><is><t>含&amp;实体</t></is></c></row>`,
	}
	file := filepath.Join(t.TempDir(), "selected.xml")
	var buf strings.Builder
	idx := &SheetIndex{XMLPath: file, SharedAnchors: map[string]SharedFormulaAnchor{"6": {Ref: "N2", Formula: "A2+H2"}}}
	for _, line := range content {
		start := int64(buf.Len())
		buf.WriteString(line)
		n := idx.RowCount() + 1
		if n == 3 {
			n = 20
		}
		if n == 2 {
			n = 3
		}
		idx.resident = append(idx.resident, RowOffset{Number: n, Start: start, End: int64(buf.Len())})
	}
	idx.XMLSize = int64(buf.Len())
	if err := os.WriteFile(file, []byte(buf.String()), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	wb := &Workbook{Styles: make([]StyleInfo, 3), Shared: []string{"原始字符串"}, NumFmts: map[int]string{}}
	selected := map[int]struct{}{1: {}, 8: {}, 14: {}}
	for _, includeFormula := range []bool{false, true} {
		for _, formatting := range []bool{false, true} {
			var reused []byte
			for i := range idx.RowCount() {
				want, err := wb.readRowFromFileSelected(f, idx, i, 1, 14, selected, formatting, includeFormula)
				if err != nil {
					t.Fatal(err)
				}
				got, err := wb.fastReadRowWithOptions(f, idx, i, 1, 14, selected, formatting, includeFormula, &reused)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("row%d fmt%v formula%v got=%+v want=%+v", i, formatting, includeFormula, got, want)
				}
			}
		}
	}
}

func TestRealFastSparseColumnParity(t *testing.T) {
	file := os.Getenv("EXCELLENS_REAL_XLSM")
	if file == "" {
		t.Skip("set EXCELLENS_REAL_XLSM")
	}
	wb, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	var sheet SheetMeta
	for _, candidate := range wb.Sheets {
		if strings.Contains(candidate.Name, "最低资本EAST交易") {
			sheet = candidate
			break
		}
	}
	if sheet.Name == "" {
		t.Skip("large sheet not found")
	}
	idx, err := wb.BuildIndex(sheet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	selected := map[int]struct{}{2: {}, 19: {}, 132: {}, 500: {}, 884: {}}
	var reused []byte
	for i := 0; i < idx.RowCount(); i += max(1, idx.RowCount()/72) {
		want, err := wb.readRowFromFileSelected(f, idx, i, 2, 884, selected, true, false)
		if err != nil {
			t.Fatal(err)
		}
		got, err := wb.fastReadRowWithOptions(f, idx, i, 2, 884, selected, true, false, &reused)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("real row%d mismatch got=%+v want=%+v", i, got, want)
		}
	}
}
