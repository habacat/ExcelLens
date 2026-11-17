package export

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func TestSeparatedExportPreservesEveryCellLineEnding(t *testing.T) {
	root := exportTestStorage(t)
	var source bytes.Buffer
	z := zip.NewWriter(&source)
	for name, content := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="r1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet.xml"/></Relationships>`,
		"xl/worksheets/sheet.xml":    `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>a&#13;b</t></is></c><c r="B1" t="inlineStr"><is><t>a&#10;b</t></is></c><c r="C1" t="inlineStr"><is><t>a&#13;&#10;b</t></is></c><c r="D1" t="inlineStr"><is><t>&quot;引号&quot;&#9;0001</t></is></c></row></sheetData></worksheet>`,
	} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "line-endings.xlsx")
	if err := os.WriteFile(path, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := workbook.OpenWorkbook(path)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := Selection{Sheet: wb.Sheets[0], Index: idx, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 4}
	for _, format := range []string{"csv", "tsv"} {
		var out bytes.Buffer
		if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: format, Mode: "values"}, nil); err != nil {
			t.Fatal(err)
		}
		comma := ","
		if format == "tsv" {
			comma = "\t"
		}
		want := strings.Join([]string{"\"a\rb\"", "\"a\nb\"", "\"a\r\nb\"", "\"\"\"引号\"\"\t0001\""}, comma) + "\r\n"
		if out.String() != want {
			t.Fatalf("%s changed literal line breaks: got %q want %q", format, out.String(), want)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(source.Bytes(), after) {
		t.Fatal("source changed", err)
	}
}

func TestDisplayedNumericCellsCannotBypassTextFormulaProtection(t *testing.T) {
	cell := workbook.CellData{Type: "n", Raw: "-1234.50", Display: "=1+1"}
	if got := safeTabularValue(cell, Options{Mode: "display", ProtectCSV: true}); got != "'=1+1" {
		t.Fatal("display text bypassed protection", got)
	}
	if got := safeTabularValue(cell, Options{Mode: "values", ProtectCSV: true}); got != "-1234.50" {
		t.Fatal("numeric source changed", got)
	}
	cell.Raw = "-1e-400"
	if got := safeTabularValue(cell, Options{Mode: "values", ProtectCSV: true}); got != cell.Raw {
		t.Fatal("small native number was changed", got)
	}
	cell.HasFormula = true
	cell.Formula = "=1+1"
	if got := safeTabularValue(cell, Options{Mode: "formula", ProtectCSV: true}); got != "=1+1" {
		t.Fatal("explicit formula request changed", got)
	}
	cell.Type = "str"
	cell.Raw = "-1234.50"
	if got := safeTabularValue(cell, Options{Mode: "values", ProtectCSV: true}); got != "'-1234.50" {
		t.Fatal("native text became a number", got)
	}
}
