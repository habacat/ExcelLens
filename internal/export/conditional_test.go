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

func conditionalExportFixture(t *testing.T) (*workbook.Workbook, Selection) {
	t.Helper()
	root := exportTestStorage(t)
	var source bytes.Buffer
	z := zip.NewWriter(&source)
	for name, content := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/styles.xml":              `<styleSheet><fonts count="1"><font><b/><color rgb="FF112233"/></font></fonts><cellXfs count="1"><xf fontId="0" numFmtId="0"/></cellXfs><dxfs count="1"><dxf><font><b val="0"/><color rgb="FFFF0033"/><u val="none"/></font><fill><patternFill patternType="solid"><fgColor rgb="FFFFFF00"/></patternFill></fill><border><bottom style="thin"><color rgb="FF0000FF"/></bottom></border></dxf></dxfs></styleSheet>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><r><rPr><b/><color rgb="FF008800"/></rPr><t>中😀</t></r><r><t>&lt;&amp;尾</t></r></is></c><c r="C1"><v>9007199254740993.31</v></c></row><row r="3"><c r="C3"><v>1</v></c></row></sheetData><conditionalFormatting sqref="A1:C3"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting></worksheet>`,
	} {
		if err := zipString(z, name, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "conditional.xlsx")
	if err := os.WriteFile(file, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := workbook.OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(after, source.Bytes()) {
			t.Error("export changed source", err)
		}
	})
	return w, Selection{Sheet: w.Sheets[0], Index: idx, FirstRow: 1, LastRow: 3, FirstCol: 1, LastCol: 3}
}

func TestConditionalHTMLStreamsSparseRowsAndOverlaysFontsWithoutChangingValues(t *testing.T) {
	w, s := conditionalExportFixture(t)
	var output bytes.Buffer
	if err := Write(context.Background(), &output, w, []Selection{s}, Options{Format: "html", Mode: "values", Styles: true}, nil); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"font-weight:400", "color:#FF0033", "background:#FFFF00", "border-bottom:0.55px solid #0000FF", "中😀", "&lt;&amp;尾", "9007199254740993.31", "<tr><th>2</th>"} {
		if !strings.Contains(text, want) {
			t.Fatal("HTML lost conditional source presentation", want, text)
		}
	}
	if strings.Contains(text, "#008800") {
		t.Fatal("source rich font overrode conditional font", text)
	}
	if strings.Count(text, "background:#FFFF00") != 9 {
		t.Fatal("blank rows/cells lost conditional effects", text)
	}
	output.Reset()
	if err := Write(context.Background(), &output, w, []Selection{s}, Options{Format: "html", Mode: "values"}, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "#FF0033") || strings.Contains(output.String(), "#FFFF00") {
		t.Fatal("plain export evaluated styles")
	}
	output.Reset()
	if err := Write(context.Background(), &output, w, []Selection{s}, Options{Format: "xlsx", Mode: "values", Styles: true}, nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "frozen.xlsx")
	if err := os.WriteFile(file, output.Bytes(), 0600); err != nil {
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
	rows, err := reopened.ReadRowsWithOptionsContext(context.Background(), idx, 0, 3, 1, 3, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Styles) != 2 || len(rows) != 3 || idx.MaxRow != 3 || idx.MaxCol != 3 {
		t.Fatal("repeated effects were not deduplicated or extent drifted", len(reopened.Styles), rows, idx.MaxRow, idx.MaxCol)
	}
	for _, row := range rows {
		for _, cell := range row.Cells {
			v := reopened.VisualStyles[cell.Style]
			if cell.Style != 1 || v.Color != "#FF0033" || v.Background != "#FFFF00" || v.Bold || v.Underline || v.BorderBottom != "0.55px solid #0000FF" || cell.Conditional != nil {
				t.Fatal("frozen style did not reopen as an ordinary source style", cell, v)
			}
		}
	}
	if rows[0].Cells[0].Raw != "中😀<&尾" || rows[0].Cells[0].Type != "inlineStr" || rows[0].Cells[2].Raw != "9007199254740993.31" || rows[0].Cells[2].Type != "" {
		t.Fatal("source values/types changed", rows[0])
	}
	for _, run := range rows[0].Cells[0].RichText.Runs {
		if run.Font.Bold == nil || *run.Font.Bold || run.Font.Color != "#FF0033" || run.Font.Underline != "none" {
			t.Fatal("source rich text overrode the frozen font", run)
		}
	}
	output.Reset()
	if err := Write(context.Background(), &output, w, []Selection{s}, Options{Format: "xlsx", Mode: "values"}, nil); err != nil {
		t.Fatal("plain snapshot was blocked", err)
	}
}
