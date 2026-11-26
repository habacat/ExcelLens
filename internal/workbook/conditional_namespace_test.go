package workbook

import (
	"context"
	"math/big"
	"math/rand"
	"strconv"
	"strings"
	"testing"
)

func TestConditionalNamespaceAliasesAreBoundToTheWorksheetAndForeignFormulaIsNotExecuted(t *testing.T) {
	isolateTestStorage(t)
	for _, tc := range []struct {
		xml   string
		known bool
	}{
		{`<m:worksheet xmlns:m="urn:main" xmlns:c="urn:main"><m:sheetData><m:row r="1"><m:c r="A1"><m:v>1</m:v></m:c></m:row></m:sheetData><c:conditionalFormatting sqref="A1"><c:cfRule type="expression" priority="1" dxfId="0"><c:formula>TRUE()</c:formula></c:cfRule></c:conditionalFormatting></m:worksheet>`, true},
		{`<worksheet xmlns="urn:main" xmlns:c="urn:main"><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><c:conditionalFormatting sqref="A1"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></c:conditionalFormatting></worksheet>`, true},
		{`<worksheet xmlns:foreign="urn:foreign"><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><conditionalFormatting sqref="A1"><cfRule type="expression" priority="1" dxfId="0"><foreign:formula>TRUE()</foreign:formula></cfRule></conditionalFormatting></worksheet>`, false},
		{`<worksheet xmlns:foreign="urn:foreign"><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><foreign:conditionalFormatting sqref="A1"><foreign:cfRule type="expression" priority="1" dxfId="0"><foreign:formula>TRUE()</foreign:formula></foreign:cfRule></foreign:conditionalFormatting></worksheet>`, false},
	} {
		file := sourceMetadataWorkbook(t, tc.xml, conditionalStylesFixture)
		w, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
		if err != nil {
			t.Fatal(err)
		}
		out := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil)
		if tc.known {
			if out == nil || out.Notice != "" || out.Style.Font.Color != "#FF0033" {
				t.Fatal("valid namespace aliases were lost", out, index.Summary())
			}
		} else if out == nil || out.Notice == "" || !out.Style.Empty() {
			t.Fatal("foreign metadata was applied", out, index.Summary())
		}
	}
}

func TestConditionalDXFUnknownNamespacesAndInvalidColorsKeepAVisibleCause(t *testing.T) {
	for _, style := range []string{
		`<styleSheet xmlns:x="urn:foreign"><x:dxfs count="1"><x:dxf><x:font><x:color rgb="FFFF0000"/></x:font></x:dxf></x:dxfs></styleSheet>`,
		`<styleSheet xmlns:x="urn:foreign"><dxfs count="1"><dxf><font><x:color rgb="FFFF0000"/></font></dxf></dxfs></styleSheet>`,
		`<styleSheet><dxfs count="1"><dxf><fill><patternFill patternType="solid"><fgColor theme="1" tint="NaN"/></patternFill></fill></dxf></dxfs></styleSheet>`,
		`<styleSheet><dxfs count="1"><dxf><border><left style="thin"><color rgb="FFFF0000" tint="NaN"/></left></border></dxf></dxfs></styleSheet>`,
		`<styleSheet><dxfs count="1"><dxf><fill><gradientFill/></fill></dxf></dxfs></styleSheet>`,
	} {
		isolateTestStorage(t)
		file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><conditionalFormatting sqref="A1"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting></worksheet>`, style)
		w, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
		if err != nil {
			t.Fatal(err)
		}
		out := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil)
		if out == nil || out.Notice == "" || !out.Style.Empty() {
			t.Fatal("unsupported style became a success", out)
		}
	}
}

func TestConditionalEmptyBorderContainerInheritsAndExplicitEmptySideClears(t *testing.T) {
	isolateTestStorage(t)
	for _, tc := range []struct {
		xml   string
		empty bool
	}{{`<border/>`, true}, {`<border><right/></border>`, false}, {`<border><right style="none"/></border>`, false}} {
		file := sourceMetadataWorkbook(t, `<worksheet><sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData><conditionalFormatting sqref="A1"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting></worksheet>`, `<styleSheet><dxfs count="1"><dxf>`+tc.xml+`</dxf></dxfs></styleSheet>`)
		w, err := OpenWorkbook(file)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := w.BuildIndex(w.Sheets[0], nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		index, err := w.SourceConditionalFormats(context.Background(), w.Sheets[0], idx)
		if err != nil {
			t.Fatal(err)
		}
		out := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil)
		if out == nil || out.Notice != "" || out.Style.Empty() != tc.empty || !tc.empty && (out.Style.Border == nil || out.Style.Border.Right == nil || out.Style.Border.Right.Style == nil) {
			t.Fatal("empty container and explicit clearing were confused", tc.xml, out)
		}
	}
}

func TestConditionalBlocksWithNoReadableRulesDoNotSilentlyDisappear(t *testing.T) {
	for _, block := range []string{
		`<conditionalFormatting sqref="A1"/>`,
		`<conditionalFormatting sqref="A1"><foreign:cfRule xmlns:foreign="urn:foreign"/></conditionalFormatting>`,
	} {
		_, _, index := conditionalFixture(t, block+`<conditionalFormatting sqref="A1:B1"><cfRule type="expression" priority="1" dxfId="0"><formula>TRUE()</formula></cfRule></conditionalFormatting>`)
		out := evaluateConditional(t, index, 1, 1, CellData{Raw: "1"}, nil)
		if out == nil || out.Notice == "" || !out.Style.Empty() || index.Summary().Pending == 0 {
			t.Fatal("unreadable rule block was treated as absent", block, out, index.Summary())
		}
		outside := evaluateConditional(t, index, 1, 2, CellData{Raw: "1"}, nil)
		if outside == nil || outside.Notice != "" || outside.Style.Font.Color != "#FF0033" {
			t.Fatal("unreadable range blocked unrelated proven rules", outside)
		}
	}
}

func TestConditionalRangeIndexAgreesWithIndependentMembershipOracleAndDeduplicatesOverlaps(t *testing.T) {
	random := rand.New(rand.NewSource(91))
	var xml strings.Builder
	groups := make([][]CellMerge, 60)
	for i := range groups {
		var refs []string
		for range 4 {
			r, c := 1+random.Intn(80), 1+random.Intn(20)
			area := CellMerge{Row: r, Col: c, EndRow: r + random.Intn(20), EndCol: min(20, c+random.Intn(5))}
			groups[i] = append(groups[i], area)
			refs = append(refs, ColumnName(area.Col)+big.NewInt(int64(area.Row)).String()+":"+ColumnName(area.EndCol)+big.NewInt(int64(area.EndRow)).String())
		}
		xml.WriteString(`<conditionalFormatting sqref="` + strings.Join(refs, " ") + `"><cfRule type="expression" priority="` + big.NewInt(int64(i+1)).String() + `"><formula>TRUE()</formula></cfRule></conditionalFormatting>`)
	}
	_, _, index := conditionalFixture(t, xml.String())
	e := index.NewEvaluator(context.Background())
	for range 500 {
		row, col := 1+random.Intn(105), 1+random.Intn(24)
		want := 0
		for _, areas := range groups {
			for _, a := range areas {
				if row >= a.Row && row <= a.EndRow && col >= a.Col && col <= a.EndCol {
					want++
					break
				}
			}
		}
		out, err := e.Evaluate(row, col, CellData{Raw: "1"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		if out != nil {
			if out.Notice != "" {
				t.Fatal(out)
			}
			got = out.Matched
		}
		if got != want {
			t.Fatal("range index differed from oracle", row, col, got, want)
		}
	}
	_, _, overlap := conditionalFixture(t, `<conditionalFormatting sqref="`+strings.TrimSpace(strings.Repeat("A1:XFD1048576 ", 1024))+`"><cfRule type="expression" priority="1"><formula>TRUE()</formula></cfRule></conditionalFormatting>`)
	e = overlap.NewEvaluator(context.Background())
	before := e.engine.Work
	out, err := e.Evaluate(1, 1, CellData{Raw: "1"}, nil)
	if err != nil || out == nil || out.Matched != 1 || before-e.engine.Work > 64 {
		t.Fatal("overlapping areas caused duplicate or unbounded predicate work", out, err, before-e.engine.Work)
	}
}

func FuzzConditionalDecimalComparisonMatchesExactRational(f *testing.F) {
	for _, pair := range [][2]string{{"9007199254740993.31", "9007199254740993.30"}, {"-0", "0.00"}, {"123.45e-2", "1.2345"}, {"+1.", "1"}} {
		f.Add(pair[0], pair[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if len(a) > 128 || len(b) > 128 {
			t.Skip()
		}
		x, ok := parseConditionalDecimal(a)
		if !ok {
			return
		}
		y, ok := parseConditionalDecimal(b)
		if !ok {
			return
		}
		if x.Exponent < -128 || x.Exponent > 128 || y.Exponent < -128 || y.Exponent > 128 {
			return
		}
		// A zero canonicalizes away its exponent. Bound the original oracle
		// input as well so big.Rat never expands 0e1000000000 during fuzzing.
		for _, raw := range []string{a, b} {
			if at := strings.IndexAny(raw, "eE"); at >= 0 {
				n, err := strconv.ParseInt(raw[at+1:], 10, 32)
				if err != nil || n < -128 || n > 128 {
					return
				}
			}
		}
		ar, aok := new(big.Rat).SetString(a)
		br, bok := new(big.Rat).SetString(b)
		if !aok || !bok {
			t.Fatal("accepted decimal rejected by rational oracle", a, b)
		}
		if got, want := compareConditionalDecimal(x, y), ar.Cmp(br); got != want {
			t.Fatal("exact comparison differed", a, b, got, want)
		}
	})
}
