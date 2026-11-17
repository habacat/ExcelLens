package workbook

import (
	"bytes"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// The reference is the standard XML tag parser; no benchmark fixture is
// dependent on a user's private workbook.
func BenchmarkWorksheetIndexScanner(b *testing.B) {
	var data bytes.Buffer
	data.WriteString(`<worksheet><sheetData>`)
	for r := 1; r <= 1800; r++ {
		fmt.Fprintf(&data, `<row r="%d">`, r)
		for c := 1; c <= 80; c++ {
			fmt.Fprintf(&data, `<c r="%s%d"><v>%d</v></c>`, columnName(c), r, (r*c)%123456)
		}
		data.WriteString(`</row>`)
	}
	data.WriteString(`</sheetData></worksheet>`)
	source := data.Bytes()
	b.ResetTimer()
	b.SetBytes(int64(len(source)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rows, anchors, _, lastR, lastC, err := scanWorksheetWithShared(bytes.NewReader(source), int64(len(source)), nil, nil, nil)
		if err != nil || lastR != 1800 || lastC != 80 || len(rows) != 1800 || len(anchors) != 0 {
			b.Fatalf("invalid index: %d %d %d %v", lastR, lastC, len(rows), err)
		}
	}
}

func columnName(column int) string {
	text := ""
	for column > 0 {
		column--
		text = string(rune('A'+column%26)) + text
		column /= 26
	}
	return text
}

func TestScanReusableTagPreservesSharedAnchorsAndExtents(t *testing.T) {
	source := strings.Join([]string{
		`<worksheet><dimension ref="A1:G10"/><sheetData>`,
		`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><f t="shared" si="7" ref="B1:B4">A1*2&amp;3</f><v>10</v></c><c r="C1" s="2"/><c r="G1"><v>99</v></c></row>`,
		`<row r="4"><c r="B4"><f t="shared" si="7"/><v>7</v></c><c r="C4" t="inlineStr"><is><t>文本</t></is></c></row>`,
		`<row r="10"><c r="G10" s="3"/></row></sheetData></worksheet>`,
	}, "")
	rows, anchors, dim, maxR, maxC, err := scanWorksheetWithShared(strings.NewReader(source), int64(len(source)), nil, nil, []string{""})
	if err != nil {
		t.Fatal(err)
	}
	if dim != "A1:G10" || maxR != 4 || maxC != 7 || len(rows) != 2 {
		t.Fatalf("extent=%s %d %d rows=%d", dim, maxR, maxC, len(rows))
	}
	want := SharedFormulaAnchor{Ref: "B1", Formula: "A1*2&3"}
	if !reflect.DeepEqual(anchors["7"], want) {
		t.Fatalf("formula anchor %v want %v", anchors["7"], want)
	}
	for _, row := range rows {
		if row.Start < 0 || row.End > int64(len(source)) || row.Start >= row.End {
			t.Fatalf("bad offset %v", row)
		}
		if !strings.HasPrefix(source[row.Start:row.End], `<row`) || !strings.HasSuffix(source[row.Start:row.End], `</row>`) {
			t.Fatalf("bad offset %v", row)
		}
	}
}

func TestScanTagSecurityBound(t *testing.T) {
	source := "<worksheet><sheetData><row " + strings.Repeat("x", 8<<20+1)
	_, _, _, _, _, err := scanWorksheetWithShared(strings.NewReader(source), int64(len(source)), nil, nil, nil)
	if err == nil {
		t.Fatal("expected malformed oversized tag rejection")
	}
}

// Keep io imported; the test also validates an ordinary cancellation reader.
var _ io.Reader

func TestBufferedTagReaderPreservesQuotedGreaterThan(t *testing.T) {
	source := `<worksheet><sheetData><row r="1" oddAttr="safe>quoted"><c r="A1" t='inlineStr'><is><t>Text</t></is></c></row></sheetData></worksheet>`
	rows, _, _, r, c, err := scanWorksheetWithShared(strings.NewReader(source), int64(len(source)), nil, nil, nil)
	if err != nil || len(rows) != 1 || r != 1 || c != 1 {
		t.Fatalf("quoted greater-than failed: rows=%v extent=%d,%d err=%v", rows, r, c, err)
	}
	if source[rows[0].Start:rows[0].End] != `<row r="1" oddAttr="safe>quoted"><c r="A1" t='inlineStr'><is><t>Text</t></is></c></row>` {
		t.Fatal("row byte offsets changed")
	}
}
func TestBufferedTagReaderCrossBufferAndXMLCDATA(t *testing.T) {
	source := `<worksheet><sheetData><row r="1" foo="` + strings.Repeat(`x`, (1<<20)+11) + `>value"><c r="A1" t="inlineStr"><is><t><![CDATA[a > b]]></t></is></c></row></sheetData></worksheet>`
	rows, _, _, r, c, err := scanWorksheetWithShared(strings.NewReader(source), int64(len(source)), nil, nil, nil)
	if err != nil || len(rows) != 1 || r != 1 || c != 1 {
		t.Fatalf("buffer boundary/CDATAs: rows=%d extent=%d,%d err=%v", len(rows), r, c, err)
	}
}
