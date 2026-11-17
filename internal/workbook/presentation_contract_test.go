package workbook

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// Every adapter supplies the same text/value/coordinate contract to the UI.
func TestCrossFormatPresentationContract(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	writeBIFF8Fixture(t, filepath.Join(dir, "binary.xls"))
	text := "单号@金额\n000000123456789@1234.5\n测试客户@0\n"
	for name, data := range map[string]string{
		"ledger.txt": text,
		"ledger.csv": "单号,金额\n000000123456789,1234.5\n测试客户,0\n",
		"ledger.tsv": "单号\t金额\n000000123456789\t1234.5\n测试客户\t0\n",
		"html.xls":   "<html><table><tr><td>单号</td><td>金额</td></tr><tr><td>000000123456789</td><td>1234.5</td></tr><tr><td>测试客户</td><td>0</td></tr></table></html>",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Create(filepath.Join(dir, "ledger.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(file)
	for name, data := range map[string]string{
		"xl/workbook.xml":            "<workbook xmlns='http://schemas.openxmlformats.org/spreadsheetml/2006/main' xmlns:r='http://schemas.openxmlformats.org/officeDocument/2006/relationships'><sheets><sheet name='Ledger' sheetId='1' r:id='rId1'/></sheets></workbook>",
		"xl/_rels/workbook.xml.rels": "<Relationships xmlns='http://schemas.openxmlformats.org/package/2006/relationships'><Relationship Id='rId1' Type='http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet' Target='worksheets/sheet1.xml'/></Relationships>",
		"xl/worksheets/sheet1.xml":   "<worksheet xmlns='http://schemas.openxmlformats.org/spreadsheetml/2006/main'><sheetData><row r='1'><c r='A1' t='inlineStr'><is><t>单号</t></is></c></row><row r='2'><c r='A2' t='inlineStr'><is><t>000000123456789</t></is></c><c r='B2'><v>1234.5</v></c></row></sheetData></worksheet>",
	} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	xlsx, err := os.ReadFile(filepath.Join(dir, "ledger.xlsx"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.xlsm"), xlsx, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ledger.xlsx", "ledger.xlsm", "binary.xls", "html.xls", "ledger.txt", "ledger.csv", "ledger.tsv"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			wb, err := OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := wb.ReadRows(idx, idx.FindRowOffset(2), 1, 1, 4)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Number != 2 || len(rows[0].Cells) == 0 {
				t.Fatal(rows)
			}
			c := rows[0].Cells[0]
			if c.Ref != "A2" || c.Raw != "000000123456789" || c.Display != c.Raw || (c.Type != "s" && c.Type != "str" && c.Type != "inlineStr") {
				t.Fatalf("presentation contract differs: %+v", c)
			}
			if len(rows[0].Cells) < 2 || rows[0].Cells[1].Raw != "1234.5" {
				t.Fatalf("source amount changed: %+v", rows[0])
			}
			matches, truncated, err := wb.SearchSheet(idx, wb.Sheets[0].Name, "000000123456789", "value", 10, nil, nil)
			if err != nil || truncated || len(matches) != 1 || matches[0].Ref != "A2" {
				t.Fatal("search changed source coordinates", matches, truncated, err)
			}
			filtered, err := wb.FilterSheetWithOptions(idx, []FilterRule{{Column: 1, Operator: "contains", Value: "000000123456789"}}, FilterOptions{HeaderRow: 1}, nil, nil)
			if err != nil || len(filtered) != 2 || idx.Rows[filtered[1]].Number != 2 {
				t.Fatal("filter changed source coordinates", filtered, err)
			}
			reopened, err := OpenWorkbook(path)
			if err != nil {
				t.Fatal(err)
			}
			cached := reopened.GetCachedIndex(reopened.Sheets[0].Path)
			if cached == nil {
				t.Fatal("cache was not reusable on reopen")
			}
			cachedRow, err := reopened.ReadRow(cached, cached.FindRowOffset(2), 1, 2)
			if err != nil || len(cachedRow.Cells) != 2 || cachedRow.Cells[0].Raw != c.Raw {
				t.Fatal("cached row changed", cachedRow, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("source changed", err)
			}
		})
	}
}
