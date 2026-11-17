package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func richExportFixture(t *testing.T, unsupported bool) (*workbook.Workbook, Selection) {
	t.Helper()
	root := exportTestStorage(t)
	var source bytes.Buffer
	z := zip.NewWriter(&source)
	extra := ""
	if unsupported {
		extra = `<outline/>`
	}
	for name, content := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><name val="宋体"/><sz val="11"/><b/><i/><strike/><u/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs></styleSheet>`,
		"xl/sharedStrings.xml":       `<sst><si><r><rPr><b val="0"/><i val="0"/><u val="none"/><strike val="0"/>` + extra + `</rPr><t>中😀</t></r><r><rPr><rFont val="Arial"/><sz val="14"/><color rgb="FF1255AA"/><vertAlign val="superscript"/><u val="doubleAccounting"/></rPr><t>&lt;&amp;&#13;&#10;尾</t></r><rPh><t>注音不应出现</t></rPh></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1"><v>9007199254740993.31</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><r><rPr><b/><color auto="1"/><vertAlign val="subscript"/></rPr><t>001</t></r><r><rPr><vertAlign val="baseline"/><color rgb="FF885522"/></rPr><t>保留</t></r></is></c><c r="B2" t="inlineStr"><is><t>000123</t></is></c></row></sheetData></worksheet>`,
	} {
		if err := zipString(z, name, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "rich-source.xlsx")
	if err := os.WriteFile(file, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	wb, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(file)
		if err != nil || sha256.Sum256(source.Bytes()) != sha256.Sum256(after) {
			t.Error("rich export mutated the source", err)
		}
	})
	return wb, Selection{Sheet: wb.Sheets[0], Index: idx, FirstRow: 1, LastRow: 2, FirstCol: 1, LastCol: 2}
}

func TestRichXLSXSnapshotReopensWithExactTextFontsStylesTypesAndCompactedSourceOrder(t *testing.T) {
	wb, selection := richExportFixture(t, false)
	ctx := context.Background()
	source, err := wb.ReadRowsWithOptionsContext(ctx, selection.Index, 0, 2, 1, 2, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true})
	if err != nil || len(source) != 2 {
		t.Fatal(source, err)
	}
	order, err := workbook.NewRowOrder(ctx, t.TempDir(), selection.Index.RowCount(), []int{1, 0})
	if err != nil {
		t.Fatal(err)
	}
	defer order.Close()
	for _, compact := range []bool{false, true} {
		s := selection
		if compact {
			s.Compact, s.Filtered, s.Order = true, true, order
		}
		for _, mode := range []string{"values", "display", "formula"} {
			if compact && mode == "formula" {
				continue
			}
			var out bytes.Buffer
			if err := Write(ctx, &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: mode, Styles: true}, nil); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "snapshot.xlsx")
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
			rows, err := reopened.ReadRowsWithOptionsContext(ctx, idx, 0, 2, 1, 2, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true})
			if err != nil || len(rows) != 2 {
				t.Fatal(rows, err)
			}
			for i, row := range rows {
				original := i
				if compact {
					original = 1 - i
				}
				for c, cell := range row.Cells {
					want := source[original].Cells[c]
					if cell.Raw != want.Raw || cell.Style != want.Style || !reflect.DeepEqual(cell.RichText, want.RichText) || c == 0 && cell.Type != "inlineStr" {
						t.Fatalf("mode=%s compact=%v: lost text/font/type: %#v / %#v", mode, compact, cell, want)
					}
					if c == 1 && original == 0 && mode != "display" && cell.Type != want.Type {
						t.Fatal("exact numeric type changed", cell)
					}
				}
			}
		}
	}
}

func TestRichHTMLAndPlainExportsPreserveTextAndExplicitStyleReset(t *testing.T) {
	wb, s := richExportFixture(t, false)
	var out bytes.Buffer
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "html", Mode: "values", Styles: true}, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"中😀", "&lt;&amp;\r\n尾", "font-weight:400", "font-style:normal", "text-decoration-line:none", "vertical-align:super", "vertical-align:sub", "color:#1255AA", "9007199254740993.31", "000123", "会计下划线"} {
		if !strings.Contains(text, want) {
			t.Errorf("HTML lost %q: %s", want, text)
		}
	}
	if strings.Contains(text, "注音不应出现") || strings.Contains(text, "<img") {
		t.Fatal("metadata became executable content")
	}
	for _, format := range []string{"csv", "tsv", "html", "xlsx"} {
		out.Reset()
		if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: format, Mode: "values"}, nil); err != nil {
			t.Fatal(err)
		}
		if format == "html" && (strings.Contains(out.String(), "<span") || strings.Contains(out.String(), "font-weight:400")) {
			t.Fatal("plain snapshot decoded/rendered rich fonts")
		}
	}
}

func TestUnrestoredRichMetadataCannotMasqueradeAsCompleteXLSXSnapshot(t *testing.T) {
	wb, s := richExportFixture(t, true)
	var out bytes.Buffer
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "values", Styles: true}, nil); err == nil || !strings.Contains(err.Error(), "A1") || !strings.Contains(err.Error(), "关闭原始格式") {
		t.Fatal("unrestored fonts silently exported as complete", err)
	}
	out.Reset()
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "html", Mode: "values", Styles: true}, nil); err != nil || !strings.Contains(out.String(), "呈现不完整") {
		t.Fatal("HTML incomplete state was lost", err)
	}
	out.Reset()
	if err := Write(context.Background(), &out, wb, []Selection{s}, Options{Format: "xlsx", Mode: "values"}, nil); err != nil {
		t.Fatal("plain-value fallback was blocked", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Write(ctx, &out, wb, []Selection{s}, Options{Format: "html", Mode: "values", Styles: true}, nil); err == nil {
		t.Fatal("cancelled presentation export succeeded")
	}
}
