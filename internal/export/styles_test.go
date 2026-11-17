package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func exportTestStorage(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, root)
	}
	return root
}

func exportReopen(t *testing.T, wb *workbook.Workbook, s Selection, styled bool, file string) (*workbook.Workbook, []workbook.RowData) {
	t.Helper()
	var out bytes.Buffer
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "values", Styles: styled}, nil); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	// Read every part to EOF (CRC) and parse every XML token independently of
	// workbook's optimized cell reader.
	for _, entry := range zr.File {
		r, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		d := xml.NewDecoder(bytes.NewReader(raw))
		for {
			_, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", entry.Name, err)
			}
		}
	}
	if err := os.WriteFile(file, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := reopened.BuildIndex(reopened.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reopened.ReadRows(idx, 0, 10, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	return reopened, rows
}

func TestHTMLXLSExportReopensWithValidStylesAndExactText(t *testing.T) {
	root := exportTestStorage(t)
	path := filepath.Join(root, "source.xls")
	text := `<html><style>.head{font-family:宋体;font-size:9pt;color:#123456;background:#e1f5ff;font-weight:bold;text-align:center;border:1px solid blue}.money{text-align:right;font-style:italic;border-bottom:3px double #ff0000}</style><table><tr><td class="head">金额</td><td class="head">编号</td></tr><tr><td class="money">1,234.500001</td><td style="text-decoration:underline">00012345678901234567</td></tr></table></html>`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
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
	s := Selection{Sheet: wb.Sheets[0], Index: idx, FirstRow: 1, LastRow: 2, FirstCol: 1, LastCol: 2}
	for _, styled := range []bool{true, false} {
		got, rows := exportReopen(t, wb, s, styled, filepath.Join(root, "result-"+map[bool]string{true: "styled", false: "plain"}[styled]+".xlsx"))
		if len(rows) != 2 || rows[1].Cells[0].Raw != "1,234.500001" || rows[1].Cells[1].Raw != "00012345678901234567" {
			t.Fatal("export changed financial text", rows)
		}
		for _, row := range rows {
			for _, c := range row.Cells {
				if c.Style < 0 || c.Style >= len(got.Styles) {
					t.Fatalf("invalid style reference %d/%d", c.Style, len(got.Styles))
				}
			}
		}
		if !styled {
			if len(got.Styles) != 1 {
				t.Fatal("unrequested palette exported")
			}
			continue
		}
		header := got.VisualStyles[rows[0].Cells[0].Style]
		if !header.Bold || header.FontName != "宋体" || header.FontSize != 9 || header.Color != "#123456" || header.Background != "#E1F5FF" || header.Horizontal != "center" || !strings.Contains(header.BorderLeft, "#0000FF") {
			t.Fatalf("header style lost: %+v", header)
		}
		amount := got.VisualStyles[rows[1].Cells[0].Style]
		if !amount.Italic || amount.Horizontal != "right" || !strings.Contains(amount.BorderBottom, "double #FF0000") {
			t.Fatalf("amount style lost: %+v", amount)
		}
		if !got.VisualStyles[rows[1].Cells[1].Style].Underline {
			t.Fatal("text underline lost")
		}
	}
}

func TestNativeExportPreservesNumberFormatsAndDate1904(t *testing.T) {
	root := exportTestStorage(t)
	path := filepath.Join(root, "source.xml")
	row := `<row r="1"><c r="A1" s="1"><v>1.25</v></c><c r="B1" s="2"><v>0.25</v></c><c r="C1" s="3"><v>1</v></c></row>`
	if err := os.WriteFile(path, []byte(row), 0600); err != nil {
		t.Fatal(err)
	}
	wb := &workbook.Workbook{Format: "xls", Date1904: true, Styles: []workbook.StyleInfo{{}, {NumFmtID: 164}, {NumFmtID: 10}, {NumFmtID: 14}}, NumFmts: map[int]string{164: `0.0000 "CNY"`}, VisualStyles: []workbook.VisualStyle{{}, {Bold: true, Background: "#aabbcc"}}}
	idx := &workbook.SheetIndex{XMLPath: path, XMLSize: int64(len(row)), MaxRow: 1, MaxCol: 3, Rows: []workbook.RowOffset{{Number: 1, Start: 0, End: int64(len(row))}}}
	s := Selection{Sheet: workbook.SheetMeta{Name: "Data"}, Index: idx, FirstRow: 1, LastRow: 1, FirstCol: 1, LastCol: 3}
	got, rows := exportReopen(t, wb, s, true, filepath.Join(root, "numeric.xlsx"))
	if !got.Date1904 || len(got.Styles) != 4 || got.NumFmts[164] != wb.NumFmts[164] || rows[0].Cells[0].Raw != "1.25" || rows[0].Cells[0].Display != "1.2500 CNY" || rows[0].Cells[1].Display != "25.00%" || rows[0].Cells[2].Display != "1904-01-02" {
		t.Fatalf("numeric/date format changed: %+v %+v", rows, got.Styles)
	}
}

func TestXLSXExportRejectsUnrepresentableSourceCoordinates(t *testing.T) {
	s := Selection{Index: &workbook.SheetIndex{XMLPath: "ledger.rows"}, FirstRow: 1048580, LastRow: 1048585, FirstCol: 1, LastCol: 1}
	if err := Validate(Options{Format: "xlsx", Mode: "values"}, []Selection{s}); err == nil {
		t.Fatal("invalid Excel row numbers accepted")
	}
	s.Compact = true
	if err := Validate(Options{Format: "xlsx", Mode: "values"}, []Selection{s}); err != nil {
		t.Fatal(err)
	}
}
