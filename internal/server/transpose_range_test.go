package server

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTransposeRangeRetainsTypedSourceIdentityAndFormulaState(t *testing.T) {
	a, file := recordFixture(t, 10, "长文本😀\r\n原文")
	before := printScaleHash(t, file)
	wb := a.currentWorkbook()
	params := url.Values{"sheet": {"First"}, "start": {"A2"}, "end": {"J2"}, "format": {"json"}, "book": {sourceBookID(wb)}, "workbookKey": {wb.Fingerprint}}
	p := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?"+params.Encode(), ""))
	if p.Book != sourceBookID(wb) || p.WorkbookKey != wb.Fingerprint || p.Sheet != "First" || len(p.Rows) != 1 || p.Rows[0].Row != 2 {
		t.Fatal("range lost source binding", p)
	}
	cells := p.Rows[0].Cells
	byRef := make(map[string]int)
	for i, cell := range cells {
		byRef[cell.Ref] = i
	}
	if cells[byRef["A2"]].Raw != "001" || cells[byRef["A2"]].Type != "inlineStr" || cells[byRef["B2"]].Raw != "9007199254740993.31" || !cells[byRef["F2"]].CacheMissing || cells[byRef["G2"]].RichText == nil {
		t.Fatal("range changed source values/types/formula cache/rich text", cells)
	}
	if printScaleHash(t, file) != before {
		t.Fatal("source changed")
	}
}

func TestTransposeRangeRetainsInheritedRowStyleWithoutInventingCells(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "inherit.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Target="worksheets/sheet1.xml" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts><font/><font><b/></font></fonts><cellXfs><xf fontId="0"/><xf fontId="1"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="2" s="1" customFormat="1"><c r="A2" t="inlineStr"><is><t>001</t></is></c></row></sheetData></worksheet>`,
	})
	a, _ := recordOpen(t, file)
	p := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?sheet=Data&start=A2&end=B2&format=json", ""))
	if len(p.Rows) != 1 || p.Rows[0].Style != 1 || p.Rows[0].Cells[0].Raw != "001" {
		t.Fatal("row style or value lost", p)
	}
	for _, cell := range p.Rows[0].Cells {
		if cell.Col == 2 && cell.Raw != "" {
			t.Fatal("invented blank value", cell)
		}
	}
}

func TestTransposeRangeAllExistingTextAndLegacyAdapters(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"data.csv", "编号,金额\n001,9007199254740993.31\n"},
		{"data.tsv", "编号\t金额\n001\t9007199254740993.31\n"},
		{"data.txt", "编号\t金额\n001\t9007199254740993.31\n"},
		{"html.xls", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>9007199254740993.31</td></tr></table></html>"},
		{"native.xls", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.name)
			if tc.body == "" {
				printBIFFSource(t, file, 4)
			} else if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			wb := a.currentWorkbook()
			p := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?"+url.Values{"sheet": {meta.Name}, "start": {"A2"}, "end": {"B2"}, "format": {"json"}}.Encode(), ""))
			idx := wb.GetCachedIndex(meta.Path)
			row, err := wb.ReadRow(idx, idx.FindRowOffset(2), 1, 2)
			if err != nil {
				t.Fatal(err)
			}
			if p.Book != sourceBookID(wb) || p.Sheet != meta.Name || len(p.Rows) != 1 || !reflect.DeepEqual(p.Rows[0].Cells, row.Cells) {
				t.Fatal("adapter values changed", p, row)
			}
			if printScaleHash(t, file) != before {
				t.Fatal("source changed")
			}
		})
	}
}
