package workbook

import (
	"testing"
)

func TestSourceStylesKeepExplicitNoneRaisedFontAndExplicitCellStyleInheritance(t *testing.T) {
	isolateTestStorage(t)
	styles := `<styleSheet><numFmts count="1"><numFmt numFmtId="170" formatCode="0.000"/></numFmts><fonts count="2"><font><name val="Arial"/><sz val="90"/><u val="none"/></font><font><name val="宋体"/><sz val="12"/><b/><u val="doubleAccounting"/><vertAlign val="subscript"/><color rgb="FF1255AA"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFFFFF00"/></patternFill></fill></fills><borders count="2"><border/><border><bottom style="thin"><color rgb="FF0000FF"/></bottom></border></borders><cellStyleXfs count="1"><xf numFmtId="170" fontId="1" fillId="1" borderId="1"><alignment horizontal="left" wrapText="1"/></xf></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyFont="0" applyFill="false" applyBorder="0" applyNumberFormat="0" applyAlignment="0"><alignment horizontal="right"/></xf><xf fontId="0" numFmtId="0"/></cellXfs></styleSheet>`
	file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1.25</v></c></row><row r="2"><c r="A2" s="1" t="inlineStr"><is><t>001</t></is></c></row></sheetData></worksheet>`, styles)
	w, err := OpenWorkbook(file)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited := w.VisualStyles[0]
	if inherited.FontName != "宋体" || inherited.FontSize != 12 || inherited.FontVertical != "subscript" || inherited.UnderlineStyle != "doubleAccounting" || !inherited.Underline || !inherited.Bold || inherited.Color != "#1255AA" || inherited.Background != "#FFFF00" || inherited.BorderBottom != "1px solid #0000FF" || inherited.Horizontal != "left" || !inherited.Wrap {
		t.Fatal("explicit inheritance was lost", inherited)
	}
	if w.VisualStyles[1].Underline || w.VisualStyles[1].UnderlineStyle != "none" || w.VisualStyles[1].FontSize != 90 {
		t.Fatal("missing flag inherited or explicit none/size was lost", w.VisualStyles[1])
	}
	row, err := w.ReadRow(idx, 0, 1, 1)
	if err != nil || row.Cells[0].Raw != "1.25" || row.Cells[0].Display != "1.250" || row.Cells[0].Style != 0 {
		t.Fatal("inherited number format changed source evidence", row, err)
	}
	text, err := w.ReadRow(idx, 1, 1, 1)
	if err != nil || text.Cells[0].Raw != "001" || text.Cells[0].Type != "inlineStr" {
		t.Fatal("native text was converted", text, err)
	}
}
