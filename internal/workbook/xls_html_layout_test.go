package workbook

import (
	"excellens/internal/fileidentity"

	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHTMLSourceLayoutKeepsBlankAreasBordersAndVerticalAlignment(t *testing.T) {
	isolateTestStorage(t)
	const source = `<html><head><style>
.report{width:300pt}.short{height:13.25pt}.title{height:25pt}.left{width:25%}.right{width:75%}
.value{border:0.5pt solid black;font-size:9pt}
</style></head><body><table class="report">
<tr class="title"><td colspan="2" valign="middle">标题</td></tr>
<tr class="short"><td class="left" valign="top"></td><td class="right value" valign="middle">000123</td></tr>
<tr class="short"><td valign="top">原文</td><td class="value" valign="top">1,234.50</td></tr>
</table></body></html>`
	path := filepath.Join(t.TempDir(), "report.xls")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	meta := wb.Sheets[0]
	heights, widths, gridLines := wb.HTMLLayout(meta)
	if gridLines == nil || *gridLines || len(heights) != 2 || heights[1].Min != 2 || heights[1].Max != 3 || math.Abs(heights[1].Size-13.25*4/3) > 1e-9 || widths[1] != 100 || widths[2] != 300 {
		t.Fatalf("unexpected source layout: %+v %+v %v", heights, widths, gridLines)
	}
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := wb.ReadRows(idx, 0, 3, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		row, col      int
		raw, vertical string
	}{{1, 1, "000123", "center"}, {2, 1, "1,234.50", "top"}} {
		cell := rows[tc.row].Cells[tc.col]
		visual := wb.VisualStyles[cell.Style]
		if cell.Raw != tc.raw || visual.Vertical != tc.vertical || visual.BorderBottom == "" {
			t.Fatalf("changed value/alignment/border: %+v %+v", cell, visual)
		}
	}
	reopened, err := OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	h2, w2, g2 := reopened.HTMLLayout(reopened.Sheets[0])
	if !reflect.DeepEqual(heights, h2) || !reflect.DeepEqual(widths, w2) || g2 == nil || *g2 {
		t.Fatal("warm metadata lost layout")
	}
	identity, _, err := fileidentity.Fingerprint(path, "xls-html-v3-document")
	if err != nil || identity != wb.Fingerprint || reopened.Fingerprint != wb.Fingerprint {
		t.Fatal("display metadata changed the identity used for user workspaces")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != source {
		t.Fatal("source file changed")
	}
}

func TestHTMLFixedDimensionsAndLargeRowRunsRemainBounded(t *testing.T) {
	m := newHTMLStyleMetadata()
	m.rules["size"] = "width:75pt;height:13.25pt"
	if n := m.dimension(htmlTableToken{data: `width="999" class="size" style="width:20px"`}, "width", 0); n != 20 {
		t.Fatal(n)
	}
	for _, raw := range []string{`width="50%"`, `width="NaN"`, `width="Inf"`, `width="-2"`} {
		if m.dimension(htmlTableToken{data: raw}, "width", 0) != 0 {
			t.Fatal("guessed unsupported dimension", raw)
		}
	}
	s := htmlDocumentSheet{}
	for i := 1; i <= 1000000; i++ {
		s.addRowSize(i, 17.5)
	}
	if len(s.Rows) != 1 || s.Rows[0].Max != 1000000 {
		t.Fatal("repeated sizes expanded per row")
	}
	s.addColumns(1, 2, 80)
	s.addColumns(1, 2, 200)
	if s.Cols[1] != 80 || s.Cols[2] != 80 {
		t.Fatal("later sections shifted established coordinates")
	}
	s.addColumns(1, 2, 200)
	if s.Cols[1] != 200 {
		t.Fatal("report rows did not outweigh a title grid")
	}
	s.addColumns(1, 1, 100, true)
	for i := 0; i < 5; i++ {
		s.addColumns(1, 1, 200)
	}
	if s.Cols[1] != 100 {
		t.Fatal("explicit columns were replaced by cell hints")
	}
}
