package server

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestRichTextSheetCompactCellAndClipboardKeepTheSameSourceEvidence(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "富文本.xlsx")
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="2"><font><name val="宋体"/><sz val="11"/><b/><u/></font><font><name val="Arial"/><sz val="12"/><i/></font></fonts><cellXfs count="2"><xf numFmtId="0" fontId="0"/><xf numFmtId="0" fontId="1"/></cellXfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1" s="1"><c r="A1" s="0" t="inlineStr"><is><r><rPr><b val="0"/><u val="none"/></rPr><t>中😀</t></r><r><rPr><color rgb="FF1255AA"/></rPr><t>文</t></r><rPh><t>注音</t></rPh></is></c><c r="B1"><v>9007199254740993.31</v></c><c r="C1" t="inlineStr"><is><t>普通</t></is></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>001</t></is></c><c r="B2"><f>1+1</f></c></row></sheetData></worksheet>`,
	})
	before, _ := os.ReadFile(file)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	url := "/api/sheet?sheet=Data&offset=0&limit=10&startCol=1&colCount=3"
	full := decodeResponse[struct{ Rows []workbook.RowData }](t, analysisRequest(t, a, "GET", url, ""))
	var sourceJSON struct {
		Rows []struct{ Cells []map[string]json.RawMessage }
	}
	if err := json.Unmarshal(analysisRequest(t, a, "GET", url, "").Body.Bytes(), &sourceJSON); err != nil || string(sourceJSON.Rows[0].Cells[0]["style"]) != "0" {
		t.Fatal("explicit style zero was lost from the full transport", err)
	}
	if full.Rows[0].Style != 1 || full.Rows[0].Cells[0].Style != 0 || full.Rows[0].Cells[1].Style != 1 {
		t.Fatal("source row-style inheritance changed", full.Rows[0])
	}
	if len(full.Rows) != 2 || full.Rows[0].Cells[0].Raw != "中😀文" || full.Rows[0].Cells[0].Type != "inlineStr" || full.Rows[0].Cells[0].RichText == nil || len(full.Rows[0].Cells[0].RichText.Runs) != 2 {
		t.Fatal(full)
	}
	rich := full.Rows[0].Cells[0].RichText
	if rich.Runs[0].End != 3 || rich.Runs[0].Font.Bold == nil || *rich.Runs[0].Font.Bold || rich.Runs[0].Font.Underline != "none" || rich.Runs[1].Font.Color != "#1255AA" || full.Rows[0].Cells[1].Raw != "9007199254740993.31" || full.Rows[0].Cells[1].RichText != nil || !full.Rows[1].Cells[1].CacheMissing {
		t.Fatal(full.Rows)
	}
	compact := decodeResponse[struct {
		Rows    []CompactRow
		Compact int
	}](t, analysisRequest(t, a, "GET", url+"&compact=1", ""))
	if compact.Compact != 1 || !reflect.DeepEqual(compact.Rows, packViewportRows(full.Rows)) {
		t.Fatal("compact transport lost source or font state", compact)
	}
	if compact.Rows[0].Cells[0].Flags&8 == 0 {
		t.Fatal("compact transport failed to preserve a zero-style override")
	}
	cell := decodeResponse[struct{ Cell workbook.CellData }](t, analysisRequest(t, a, "GET", "/api/cell?sheet=Data&ref=A1", ""))
	if !reflect.DeepEqual(cell.Cell, full.Rows[0].Cells[0]) {
		t.Fatal("inspector disagreed with viewport", cell.Cell, full.Rows[0].Cells[0])
	}
	clipboard := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?sheet=Data&start=A1&end=C2&format=json", ""))
	if len(clipboard.Rows) != 2 || !reflect.DeepEqual(clipboard.Rows[0].Cells[0], cell.Cell) || clipboard.Rows[1].Cells[0].Raw != "001" {
		t.Fatal("clipboard disagreed with source presentation", clipboard)
	}
	withoutFonts := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?sheet=Data&start=A1&end=C2&format=json&richText=0", ""))
	if withoutFonts.Rows[0].Cells[0].RichText != nil || withoutFonts.Rows[0].Cells[0].Raw != cell.Cell.Raw || withoutFonts.Rows[0].Cells[1].Raw != "9007199254740993.31" {
		t.Fatal("plain clipboard decoded fonts or changed source values", withoutFonts)
	}
	plain := analysisRequest(t, a, "GET", "/api/range?sheet=Data&start=A1&end=C2", "")
	if plain.Code != 200 || plain.Body.String() != "中😀文\t9007199254740993.31\t普通\n001\t\t" {
		t.Fatal(plain.Code, plain.Body.String())
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("presentation mutated the workbook")
	}
}

func TestClipboardTextBudgetRejectsWholeOversizedSelectionBeforeWritingAPayload(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "bounded-copy.csv")
	value := strings.Repeat("x", formats.ClipboardTextBytes/4)
	source := []byte(strings.Repeat(value+"\n", 3))
	if err := os.WriteFile(file, source, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "&format=json", "&format=json&richText=0"} {
		response := analysisRequest(t, a, "GET", "/api/range?sheet=数据&start=A1&end=A3"+suffix, "")
		if response.Code != 413 || !strings.Contains(response.Body.String(), "使用导出") || strings.Contains(response.Body.String(), value[:100]) {
			t.Fatal("oversized copy published partial source content", response.Code, response.Body.Len())
		}
	}
	response := analysisRequest(t, a, "GET", "/api/range?sheet=数据&start=A1&end=A2&format=json&richText=0", "")
	payload := decodeResponse[clipboardPayload](t, response)
	if len(payload.Rows) != 2 || payload.Rows[0].Cells[0].Raw != value || payload.Rows[1].Cells[0].Raw != value {
		t.Fatal("copy at the byte boundary was clipped")
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(after) != sha256.Sum256(source) {
		t.Fatal("bounded copy changed source")
	}
}
