package export

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func conditionalPaletteCell(style int, d workbook.DifferentialStyle) workbook.CellData {
	return workbook.CellData{Style: style, Conditional: &workbook.ConditionalPresentation{ConditionalFormatResult: workbook.ConditionalFormatResult{Style: d}, Visual: &workbook.VisualStyle{}}}
}

func TestConditionalXLSXPaletteKeepsUnknownNamespacesAndInheritedFontAlignmentAndBorderProperties(t *testing.T) {
	const main = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	x := `<m:styleSheet xmlns:m="` + main + `" xmlns:x="urn:extra" x:kind="x:Kind"><m:fonts xmlns:f="` + main + `" count="2"><f:font><f:name val="Default"/></f:font><f:font><f:name val="Source"/><f:sz val="12"/><f:b/><x:effect x:q="x:Kind"/><f:scheme val="major"/></f:font></m:fonts><m:fills count="1"><m:fill><m:patternFill patternType="none"/></m:fill></m:fills><m:borders count="1"><m:border diagonalUp="1"><m:left style="thin"><m:color rgb="FF112233"/></m:left><m:diagonal style="dotted"><m:color rgb="FF445566"/></m:diagonal><x:edge x:q="x:Kind"/></m:border></m:borders><m:cellStyleXfs xmlns:a="` + main + `" count="1"><m:xf fontId="1"><a:alignment horizontal="right" indent="3" shrinkToFit="1" x:q="x:Kind"/></m:xf></m:cellStyleXfs><m:cellXfs count="1"><m:xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyFont="0" applyAlignment="0" x:flag="x:Kind"><m:protection locked="0" hidden="1"/><m:extLst><x:extra x:q="x:Kind"/></m:extLst></m:xf></m:cellXfs><m:dxfs count="0"/></m:styleSheet>`
	p := newXLSXStylePalette(context.Background(), x)
	no := false
	red := "#FF0033"
	wrap := false
	id, err := p.styleID(conditionalPaletteCell(0, workbook.DifferentialStyle{Font: workbook.RichFont{Bold: &no, Color: red}, Wrap: &wrap, Border: &workbook.DifferentialBorders{Left: &workbook.DifferentialBorderSide{Color: &red}}}))
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	var out bytes.Buffer
	if err := p.write(&out); err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{`<x:effect x:q="x:Kind"/>`, `<f:scheme val="major"/>`, `<m:protection locked="0" hidden="1"/>`, `<m:extLst><x:extra x:q="x:Kind"/></m:extLst>`, `diagonalUp="1"`, `<m:diagonal style="dotted"><m:color rgb="FF445566"/></m:diagonal>`, `indent="3"`, `shrinkToFit="1"`, `x:q="x:Kind"`, `wrapText="0"`, `<f:b val="0"/>`} {
		if !strings.Contains(out.String(), keep) {
			t.Fatal("unknown or inherited property lost", keep, out.String())
		}
	}
	d, err := parseStyleXML(context.Background(), out.String())
	if err != nil {
		t.Fatal(err, out.String())
	}
	fontRaw, _ := d.record("fonts", 2)
	if !strings.Contains(fontRaw, `val="Source"`) {
		t.Fatal("font inheritance was not retained", fontRaw)
	}
	xfRaw, _ := d.record("cellXfs", 1)
	f, err := parseStyleFragment(context.Background(), d.wrapper("cellXfs"), xfRaw)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range f.children {
		if child.name.Local == "alignment" {
			if child.name.Space != main {
				t.Fatal("transplanted alignment lost its namespace", child, xfRaw)
			}
		}
	}
}

func TestConditionalXLSXNumberFormatsReuseCodesWithoutChangingSourceIDsAndGeneralUsesZero(t *testing.T) {
	x := `<styleSheet><numFmts count="1"><numFmt numFmtId="164" formatCode="0.0000"/></numFmts><fonts count="1"><font/></fonts><fills count="1"><fill/></fills><borders count="1"><border/></borders><cellXfs count="2"><xf numFmtId="164"/><xf numFmtId="165"/></cellXfs></styleSheet>`
	p := newXLSXStylePalette(context.Background(), x)
	first, err := p.styleID(conditionalPaletteCell(0, workbook.DifferentialStyle{NumberFormat: &workbook.DifferentialNumberFormat{ID: 164, Code: "0.00"}}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.styleID(conditionalPaletteCell(1, workbook.DifferentialStyle{NumberFormat: &workbook.DifferentialNumberFormat{ID: 200, Code: "0.00"}}))
	if err != nil {
		t.Fatal(err)
	}
	if first < 2 || second < 2 {
		t.Fatal(first, second)
	}
	if len(p.doc.section("numFmts").added) != 1 || p.numberCodes["0.00"] != 166 || p.numberCodes["0.0000"] != 164 {
		t.Fatal("source format IDs were overwritten", p.numberCodes)
	}
	general, err := p.styleID(conditionalPaletteCell(0, workbook.DifferentialStyle{NumberFormat: &workbook.DifferentialNumberFormat{ID: 900, Code: "General"}}))
	if err != nil {
		t.Fatal(err)
	}
	f, err := p.fragment("cellXfs", general)
	if err != nil {
		t.Fatal(err)
	}
	id, err := styleIntAttr(f.attrs, "numFmtId", -1)
	if err != nil || id != 0 {
		t.Fatal("General was not an exact source spelling", id, err)
	}
	var out bytes.Buffer
	if err := p.write(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `numFmtId="164" formatCode="0.0000"`) {
		t.Fatal("old numeric format lost")
	}
}

func TestConditionalXLSXRejectsMissingInheritedComponentsAndUndefinedUnchangedNumberFormats(t *testing.T) {
	color := "#FF0000"
	base := `<styleSheet><fonts count="1"><font/></fonts><fills count="1"><fill/></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf fontId="9"/></cellStyleXfs><cellXfs count="1"><xf %s/></cellXfs></styleSheet>`
	for _, attrs := range []string{`fontId="9"`, `fillId="9"`, `borderId="9"`, `fontId="0" applyFont="0" xfId="0"`, `numFmtId="165"`, `applyAlignment="0" xfId="2"`, `applyBorder="invalid"`} {
		p := newXLSXStylePalette(context.Background(), fmt.Sprintf(base, attrs))
		if _, err := p.styleID(conditionalPaletteCell(0, workbook.DifferentialStyle{Font: workbook.RichFont{Color: color}})); err == nil {
			t.Fatal("unresolved source dependency became a complete snapshot", attrs)
		}
	}
	p := newXLSXStylePalette(context.Background(), fmt.Sprintf(base, `fillId="9" numFmtId="165"`))
	if _, err := p.styleID(conditionalPaletteCell(0, workbook.DifferentialStyle{Background: &color, NumberFormat: &workbook.DifferentialNumberFormat{Code: "0.00"}})); err != nil {
		t.Fatal("fully replaced components still depended on missing old definitions", err)
	}
}

func TestConditionalXLSXPaletteStopsAtWorkBudgetWithLargeUnknownSourceFontData(t *testing.T) {
	unknown := `<x:payload>` + strings.Repeat("kept", 16<<10) + `</x:payload>`
	source := `<styleSheet xmlns:x="urn:keep"><fonts><font>` + unknown + `</font></fonts><cellXfs><xf/></cellXfs></styleSheet>`
	p := newXLSXStylePalette(context.Background(), source)
	var budgetErr error
	for n := 1; n < 1024; n++ {
		_, budgetErr = p.styleID(conditionalPaletteCell(0, workbook.DifferentialStyle{Font: workbook.RichFont{Color: fmt.Sprintf("#%06X", n)}}))
		if budgetErr != nil {
			break
		}
	}
	if budgetErr == nil || !strings.Contains(budgetErr.Error(), "工作容量") {
		t.Fatal("large unknown source components produced unbounded palette work", budgetErr)
	}
	if p.raw != source || p.doc.section("fonts").count() >= 1024 {
		t.Fatal("source XML was modified or component count was not bounded")
	}
}
