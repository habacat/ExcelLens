package workbook

import (
	"encoding/csv"
	"encoding/xml"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestPipeTextRecordsMatchIndependentCSVRead(t *testing.T) {
	source := "  编号  |\u3000说明\u3000|金额\r\n 0007 | \"a|b\" | (1,234.50) \n\"a\nb\"|\u00a0quoted\u00a0|\r\n|尾行"
	w := &Workbook{Format: "txt", source: &externalSource{delimiter: '|'}}
	reader := csv.NewReader(strings.NewReader(source))
	reader.Comma, reader.LazyQuotes, reader.FieldsPerRecord = '|', true, -1
	err := w.streamTextRecords(strings.NewReader(source), func(got []string) error {
		want, err := reader.Read()
		if err != nil {
			t.Fatal(err)
		}
		for i := range want {
			want[i] = strings.TrimSpace(want[i])
		}
		if !slices.Equal(got, want) {
			t.Fatalf("got %q want %q", got, want)
		}
		return nil
	})
	if _, end := reader.Read(); err != nil || end != io.EOF {
		t.Fatal(err, end)
	}
}

func TestIndexTagNamesMatchStandardXMLRowSpans(t *testing.T) {
	// Prefixes, similarly named extension elements and a quoted '>' exercise
	// the exact name comparisons on both opening and closing tags.
	src := `<x:worksheet xmlns:x="urn:sheet"><x:sheetData><x:row r="3" label="x>y"><x:c r="A3"><x:v>0</x:v></x:c><x:c r="D3" t="inlineStr"><x:is><x:t>原文</x:t></x:is></x:c></x:row><x:row r="8"><x:c r="C8"><x:v>7</x:v></x:c><x:c r="F8" s="1"/></x:row></x:sheetData><x:extLst><x:rowExtra/></x:extLst></x:worksheet>`
	decoder := xml.NewDecoder(strings.NewReader(src))
	var want []RowOffset
	var start int64
	for {
		offset := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch item := token.(type) {
		case xml.StartElement:
			if item.Name.Local == "row" {
				start = offset
			}
		case xml.EndElement:
			if item.Name.Local == "row" {
				number := []int{3, 8}[len(want)]
				want = append(want, RowOffset{Number: number, Start: start, End: decoder.InputOffset()})
			}
		}
	}
	got, _, _, lastRow, lastCol, err := scanWorksheet(strings.NewReader(src), int64(len(src)), nil, nil)
	if err != nil || !slices.Equal(got, want) || lastRow != 8 || lastCol != 4 {
		t.Fatalf("rows %v want %v extent %d,%d err %v", got, want, lastRow, lastCol, err)
	}
}
