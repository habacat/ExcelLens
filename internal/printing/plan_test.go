package printing

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

const testNS = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
const testRel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

func fixture(t *testing.T, body, names string) (*workbook.PrintSource, *workbook.Workbook, *workbook.SheetIndex, string) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"LOCALAPPDATA", "APPDATA", "XDG_CACHE_HOME", "XDG_CONFIG_HOME"} {
		t.Setenv(key, root)
	}
	file := filepath.Join(root, "source.xlsx")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for path, text := range map[string]string{"xl/workbook.xml": `<workbook xmlns="` + testNS + `" xmlns:r="` + testRel + `"><sheets><sheet name="Data" sheetId="1" r:id="s1"/></sheets><definedNames>` + names + `</definedNames></workbook>`, "xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="` + testRel + `/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`, "xl/worksheets/sheet1.xml": `<worksheet xmlns="` + testNS + `">` + body + `</worksheet>`} {
		writer, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
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
	source, err := w.ReadPrintSource(context.Background(), w.Sheets[0], idx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return source, w, idx, file
}

func table(rows, columns int, extra string) string {
	var text strings.Builder
	text.WriteString(`<sheetFormatPr defaultRowHeight="10" baseColWidth="8"/><cols><col min="1" max="` + fmt.Sprint(columns) + `" width="1"/></cols><sheetData>`)
	for row := 1; row <= rows; row++ {
		fmt.Fprintf(&text, `<row r="%d">`, row)
		for col := 1; col <= columns; col++ {
			ref := workbook.ColumnName(col) + fmt.Sprint(row)
			if row == 1 && col == 1 {
				fmt.Fprintf(&text, `<c r="%s" t="inlineStr"><is><t>001</t></is></c>`, ref)
			} else {
				fmt.Fprintf(&text, `<c r="%s" t="n"><v>9007199254740993.31</v></c>`, ref)
			}
		}
		text.WriteString(`</row>`)
	}
	text.WriteString(`</sheetData>`)
	text.WriteString(extra)
	return text.String()
}

func options(width, height string) Options {
	return Options{Paper: &Paper{width, height}, Orientation: "portrait", Margins: &Margins{"0", "0", "0", "0", "0", "0"}, ScalePercent: "100"}
}
func sourceArea() string {
	return `<definedName name="_xlnm.Print_Area" localSheetId="0">'Data'!$A$1:$E$5</definedName>`
}

func TestPlanExactGridPageOrderSourceValuesAndImmutableResults(t *testing.T) {
	source, w, idx, file := fixture(t, table(5, 5, ""), sourceArea())
	before, _ := os.ReadFile(file)
	rowBefore, _ := w.ReadRow(idx, 0, 1, 5)
	plan, err := New(context.Background(), source, options("10.5pt", "20pt"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary().Pages != 9 || plan.Summary().Origins["areas"] != "source" || plan.Summary().Scale.Percent != "100.000000" {
		t.Fatal(plan.Summary())
	}
	first, _ := plan.Page(context.Background(), 0)
	second, _ := plan.Page(context.Background(), 1)
	fourth, _ := plan.Page(context.Background(), 3)
	if first.Rows[0].First != 1 || first.Rows[0].Last != 2 || first.Columns[0].Last != 2 || second.Rows[0].First != 3 || fourth.Columns[0].First != 3 {
		t.Fatal(first, second, fourth)
	}
	o := options("10.5pt", "20pt")
	o.PageOrder = "overThenDown"
	other, err := New(context.Background(), source, o)
	if err != nil {
		t.Fatal(err)
	}
	next, _ := other.Page(context.Background(), 1)
	if next.Columns[0].First != 3 || next.Rows[0].First != 1 {
		t.Fatal(next)
	}
	first.Rows[0].First = 999
	summary := plan.Summary()
	summary.Origins["areas"] = "changed"
	again, _ := plan.Page(context.Background(), 0)
	if again.Rows[0].First != 1 || plan.Summary().Origins["areas"] != "source" {
		t.Fatal("public result changed shared plan")
	}
	rowAfter, _ := w.ReadRow(idx, 0, 1, 5)
	after, _ := os.ReadFile(file)
	if !reflect.DeepEqual(rowBefore, rowAfter) || !bytes.Equal(before, after) || rowAfter.Cells[0].Raw != "001" {
		t.Fatal("pagination changed source")
	}
	w.ClearIndexes()
	if _, err := plan.Page(context.Background(), 0); err == nil {
		t.Fatal("page read accepted replaced exact index")
	}
}

func TestPlanRepeatedTitlesManualBreakCompatibilityAndFit(t *testing.T) {
	names := sourceArea() + `<definedName name="_xlnm.Print_Titles" localSheetId="0">'Data'!$1:$1,'Data'!$A:$A</definedName>`
	source, _, _, _ := fixture(t, table(5, 5, `<rowBreaks count="1" manualBreakCount="1"><brk id="3" min="0" max="0" man="1"/></rowBreaks>`), names)
	plan, err := New(context.Background(), source, options("15.75pt", "30pt"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary().Pages != 4 {
		t.Fatal(plan.Summary())
	}
	page, _ := plan.Page(context.Background(), 0)
	if len(page.Rows) != 2 || !page.Rows[0].Repeated || page.Rows[1].First != 2 || page.Rows[1].Last != 3 || len(page.Columns) != 2 || page.Columns[1].First != 2 {
		t.Fatal(page)
	}
	if !strings.Contains(strings.Join(plan.Summary().Notices, " "), "Office") {
		t.Fatal("partial source break behavior not explained")
	}
	o := options("15.75pt", "30pt")
	o.ScalePercent = ""
	o.Fit = &Fit{1, 1}
	fit, err := New(context.Background(), source, o)
	if err != nil {
		t.Fatal(err)
	}
	if fit.Summary().Pages != 1 {
		t.Fatal(fit.Summary())
	}
	if !strings.Contains(strings.Join(fit.Summary().Notices, " "), "忽略") {
		t.Fatal("fit mode silently retained/discarded manual breaks")
	}
	page, _ = fit.Page(context.Background(), 0)
	if page.Content.Width > fit.Summary().Paper.Width || page.Content.Height > fit.Summary().Paper.Height {
		t.Fatal("fit overflow", page, fit.Summary())
	}
}

func TestPlanOversizedRowAndColumnPhysicalSlicesNeverLoseContent(t *testing.T) {
	source, _, _, _ := fixture(t, `<sheetFormatPr defaultRowHeight="200" baseColWidth="8"/><cols><col min="1" max="1" width="20"/></cols><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>`, "")
	o := options("30pt", "50pt")
	plan, err := New(context.Background(), source, o)
	if err != nil {
		t.Fatal(err)
	}
	var area int64
	for at := 0; at < plan.Summary().Pages; at++ {
		page, err := plan.Page(context.Background(), at)
		if err != nil {
			t.Fatal(err)
		}
		if !page.Oversize || page.Content.Width > 30_000_000_000 || page.Content.Height > 50_000_000_000 {
			t.Fatal(page)
		}
		if len(page.Rows) != 1 || len(page.Columns) != 1 || page.Rows[0].First != 1 || page.Columns[0].First != 1 {
			t.Fatal(page)
		}
		area += (page.Rows[0].Length / 1_000_000) * (page.Columns[0].Length / 1_000_000)
	}
	width, _ := source.Geometry().Extent(context.Background(), "col", 1, 1)
	if area != (200_000_000_000/1_000_000)*(width.Units/1_000_000) {
		t.Fatal("physical content area missing or duplicated", area, width)
	}
}

func TestPlanUnknownSourceRequiresExplicitPreviewDefaultsWithoutChangingMetadata(t *testing.T) {
	source, _, _, _ := fixture(t, `<sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>`, "")
	before := source.Geometry().Summary()
	if _, err := New(context.Background(), source, Options{}); err == nil {
		t.Fatal("unknown paper/device became source defaults")
	}
	plan, err := New(context.Background(), source, Options{UsePreviewDefaults: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary().Pages != 1 || plan.Summary().Origins["paper"] != "preview-default" || !strings.Contains(strings.Join(plan.Summary().Notices, " "), "缺省") {
		t.Fatal(plan.Summary())
	}
	if source.Geometry().Summary().DefaultRow.RawSize != before.DefaultRow.RawSize || source.Geometry().Summary().DefaultRow.Status != before.DefaultRow.Status {
		t.Fatal("preview default became source metadata")
	}
	summary := plan.Summary()
	summary.Defaults.Row.Value = "999"
	summary.Areas[0].EndRow = 999
	value, err := plan.Extent(context.Background(), "row", 1, 1)
	if err != nil || value.Units != 15_000_000_000 || !value.Defaulted || plan.Summary().Defaults.Row.Value != "15" || plan.Summary().Areas[0].EndRow != 1 {
		t.Fatal("resolved preview data not immutable", value, err)
	}
	if _, err := source.Geometry().Extent(context.Background(), "row", 1, 1); err == nil {
		t.Fatal("preview mutated source geometry")
	}
}

func TestPlanEmptyHiddenTitlesFullPaperInvalidOptionsAndSourceMutation(t *testing.T) {
	source, _, _, file := fixture(t, table(5, 5, ""), sourceArea())
	for name, o := range map[string]Options{"zero paper": options("0", "20"), "margins": {UsePreviewDefaults: true, Paper: &Paper{"10pt", "10pt"}, Margins: &Margins{"6pt", "6pt", "0", "0", "0", "0"}}, "scale conflict": {UsePreviewDefaults: true, ScalePercent: "100", Fit: &Fit{1, 1}}, "bad scale": {UsePreviewDefaults: true, ScalePercent: "0x64p0"}, "title fills": {Paper: &Paper{"10.5pt", "10pt"}, Orientation: "portrait", Margins: &Margins{"0", "0", "0", "0", "0", "0"}, RepeatRows: []Interval{{1, 2}}}} {
		t.Run(name, func(t *testing.T) {
			if plan, err := New(context.Background(), source, o); err == nil || plan != nil {
				t.Fatal("bad settings became partial plan", plan, err)
			}
		})
	}
	o := options("10.5pt", "20pt")
	o.Areas = []workbook.CellMerge{}
	empty, err := New(context.Background(), source, o)
	if err != nil || empty.Summary().Pages != 0 {
		t.Fatal(empty, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, source, options("10.5pt", "20pt")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	plan, err := New(context.Background(), source, options("10.5pt", "20pt"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write([]byte("changed")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := plan.Page(context.Background(), 0); err == nil {
		t.Fatal("page accepted changed source")
	}
}

func TestPlanMeasureTransportAndRationalUnitBoundaries(t *testing.T) {
	for _, text := range []string{"25.4mm", "2.54cm", "1in", "72pt", "6pc"} {
		value, err := measure(text, false)
		if err != nil || value != 72_000_000_000 {
			t.Fatal(text, value, err)
		}
	}
	for _, text := range []string{"NaN", "Inf", "-1mm", "1e-999999999pt", "0x10p0", "1/2pt"} {
		if _, err := measure(text, true); err == nil {
			t.Fatal("invalid measure", text)
		}
	}
	data, err := json.Marshal(Part{First: 1, Last: 1, Length: 9_000_000_000_000_000_000})
	if err != nil || !bytes.Contains(data, []byte(`"length":"9000000000000000000"`)) {
		t.Fatal(string(data), err)
	}
}

func TestPlanStoredPaperMarginsCenteringPageZeroAndRangeOverride(t *testing.T) {
	settings := `<printOptions horizontalCentered="1" verticalCentered="1"/><pageMargins left="0.5" right="0.5" top="0.5" bottom="0.5" header="0.2" footer="0.2"/><pageSetup paperWidth="200pt" paperHeight="300pt" orientation="portrait" scale="100" useFirstPageNumber="1" firstPageNumber="0"/>`
	source, _, _, _ := fixture(t, table(5, 5, settings), sourceArea())
	plan, err := New(context.Background(), source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := plan.Page(context.Background(), 0)
	if err != nil || page.Number != 0 || page.OffsetX != 86_875_000_000 || page.OffsetY != 125_000_000_000 || plan.Summary().Pages != 1 || plan.Summary().Origins["paper"] != "source" {
		t.Fatal(page, plan.Summary(), err)
	}
	badNames := `<definedName name="_xlnm.Print_Area" localSheetId="0">OFFSET('Data'!$A$1,0,0,5,5)</definedName>`
	source, _, _, _ = fixture(t, table(5, 5, settings), badNames)
	if plan, err := New(context.Background(), source, Options{}); err == nil || plan != nil {
		t.Fatal("dynamic source name silently became an indexed range")
	}
	o := Options{Areas: []workbook.CellMerge{{Row: 2, Col: 2, EndRow: 3, EndCol: 3}}}
	plan, err = New(context.Background(), source, o)
	if err != nil || plan.Summary().Pages != 1 || plan.Summary().Origins["areas"] != "preview-override" {
		t.Fatal(plan, err)
	}
	page, _ = plan.Page(context.Background(), 0)
	if page.Rows[0].First != 2 || page.Rows[0].Last != 3 {
		t.Fatal(page)
	}
}

func TestPlanExplicitEmptyNeedsNoPaperAndAllHiddenNeedsNoBlankPage(t *testing.T) {
	source, _, _, _ := fixture(t, `<sheetData/>`, "")
	empty, err := New(context.Background(), source, Options{})
	if err != nil || empty.Summary().Pages != 0 {
		t.Fatal(empty, err)
	}
	source, _, _, _ = fixture(t, `<sheetFormatPr defaultRowHeight="10" zeroHeight="1" baseColWidth="8"/><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>`, "")
	plan, err := New(context.Background(), source, Options{UsePreviewDefaults: true})
	if err != nil || plan.Summary().Pages != 0 {
		t.Fatal("all hidden source became an artificial blank page", plan, err)
	}
}
