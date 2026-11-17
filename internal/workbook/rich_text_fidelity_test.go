package workbook

import (
	"bytes"
	"encoding/xml"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRichTextPhoneticsAreNotCellContent(t *testing.T) {
	const plain = `<si><t>中文</t><rPh sb="0" eb="2"><t>zhong wen</t></rPh><phoneticPr fontId="1"/></si>`
	const rich = `<si><r><t>上</t></r><r><rPr><b/></rPr><t>&amp;海</t></r><rPh sb="0" eb="2"><t>shang hai</t></rPh></si>`
	for _, tc := range []struct{ input, want string }{{plain, "中文"}, {rich, "上&海"}} {
		var str sharedStringItem
		if err := xml.Unmarshal([]byte(tc.input), &str); err != nil {
			t.Fatal(err)
		}
		if str.Text != tc.want {
			t.Fatalf("rich string %q returned %q want %q", tc.input, str.Text, tc.want)
		}
	}
	body := []byte(`<is><r><t>上</t></r><r><t>&amp;海</t></r><rPh sb="0" eb="2"><t>shang hai</t></rPh></is>`)
	fast, ok := inlineStringValue(body)
	if !ok || fast != "上&海" {
		t.Fatalf("inline fast text included phonetics: value=%q ok=%t", fast, ok)
	}
	idx := syntheticIndex(t, `<row r="1"><c r="A1" t="inlineStr">`+string(body)+`</c></row>`)
	w := &Workbook{}
	f, err := os.Open(idx.XMLPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	strict, err := w.readRowFromFile(f, idx, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var buffer []byte
	optimized, err := w.fastReadRowAt(f, idx, 0, 1, 1, &buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(strict, optimized) || optimized.Cells[0].Display != "上&海" {
		t.Fatalf("optimized reader differed on rich text: %v / %v", optimized, strict)
	}
}

func TestPhoneticOnlyCellDoesNotExtendUsedExtent(t *testing.T) {
	x := `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c><c r="Z1" t="inlineStr"><is><rPh sb="0" eb="1"><t>annotation</t></rPh></is></c></row></sheetData></worksheet>`
	_, _, _, maxRow, maxCol, err := scanWorksheetWithShared(strings.NewReader(x), int64(len(x)), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if maxRow != 1 || maxCol != 1 {
		t.Fatalf("phonetic extension changed data extent: %d/%d", maxRow, maxCol)
	}
}

func TestTruncatedRichStringIsRejected(t *testing.T) {
	var item sharedStringItem
	err := xml.Unmarshal([]byte(`<si><r><t>partial</t></r>`), &item)
	if err == nil {
		t.Fatal("broken shared-string XML incorrectly accepted")
	}
	dec := xml.NewDecoder(bytes.NewReader([]byte(`<is><t>partial</t>`)))
	tok, err := dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	_, err = readInlineText(dec, tok.(xml.StartElement))
	if err == nil {
		t.Fatal("truncated inline text was not rejected")
	}
}
