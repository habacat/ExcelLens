package workbook

import (
	"strings"
	"testing"
)

func TestSearchInfersMissingCellAddressesIncludingEmptyCells(t *testing.T) {
	idx := syntheticIndex(t, `<row r="7"><c r="A7" t="str"><v>first</v></c><c r="D7" s="1"/><c t="inlineStr"><is><t>account 00123</t></is></c><c><f>LEN(A7)</f><v>5</v></c></row>`)
	idx.ResidentRows[0].Number = 7
	wb := &Workbook{NumFmts: map[int]string{}}
	cases := []struct{ query, mode, want string }{
		{"account 00123", "value", "E7"},
		{"LEN", "formula", "F7"},
		{"A7", "reference", "F7"},
	}
	for _, tc := range cases {
		got, _, err := wb.SearchSheet(idx, "Sheet1", tc.query, tc.mode, 25, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Ref != tc.want {
			t.Fatalf("query %s %s: got %+v want %s", tc.query, tc.mode, got, tc.want)
		}
	}
}

func TestCDATAIsIndexedAndSearchableWithoutMarkup(t *testing.T) {
	x := `<worksheet><sheetData><row r="7"><c r="J7" t="inlineStr"><is><t><![CDATA[<客户>&需要核查]]></t></is></c></row></sheetData></worksheet>`
	offsets, _, _, maxRow, maxCol, err := scanWorksheetWithShared(strings.NewReader(x), int64(len(x)), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 || maxRow != 7 || maxCol != 10 {
		t.Fatalf("CDATA-only cell disappeared: rows=%v extent=%d/%d", offsets, maxRow, maxCol)
	}
	idx := syntheticIndex(t, `<row r="7"><c r="J7" t="inlineStr"><is><t><![CDATA[<客户>&需要核查]]></t></is></c></row>`)
	idx.ResidentRows[0].Number = 7
	wb := &Workbook{NumFmts: map[int]string{}}
	got, _, err := wb.SearchSheet(idx, "Sheet1", "<客户>&", "value", 25, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Ref != "J7" || got[0].Value != "<客户>&需要核查" {
		t.Fatalf("CDATA search result %+v", got)
	}
}

func TestSearchExcludesPhoneticAnnotations(t *testing.T) {
	idx := syntheticIndex(t, `<row r="1"><c r="A1" t="inlineStr"><is><t>中文</t><rPh sb="0" eb="2"><t>zhong wen</t></rPh></is></c></row>`)
	wb := &Workbook{}
	for _, query := range []string{"zhong", "wen"} {
		got, _, err := wb.SearchSheet(idx, "Sheet1", query, "value", 10, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("phonetic %q leaked into search: %+v", query, got)
		}
	}
	got, _, err := wb.SearchSheet(idx, "Sheet1", "中文", "value", 10, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Value != "中文" {
		t.Fatalf("main text missing: %+v", got)
	}
}
