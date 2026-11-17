package workbook

import (
	"encoding/json"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func styledText(t *testing.T, w *Workbook, input string) (string, *RichText, error) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(input))
	first, err := decoder.Token()
	if err != nil {
		return "", nil, err
	}
	return w.readRichTextStyled(decoder, first.(xml.StartElement))
}

func TestRichTextStyledPreservesValuesAndExplicitFontOverrides(t *testing.T) {
	input := `<si><r><rPr><rFont val="宋体"/><sz val="14"/><color rgb="FF1255AA"/><b/><i val="false"/><u val="doubleAccounting"/><strike val="0"/><vertAlign val="superscript"/></rPr><t xml:space="preserve">中😀 &amp;</t></r><r><rPr><b val="0"/><u val="none"/><vertAlign val="baseline"/></rPr><t>文</t></r><rPh sb="0" eb="2"><t>phonetics</t></rPh></si>`
	text, rich, err := styledText(t, &Workbook{}, input)
	if err != nil || text != "中😀 &文" || rich == nil || rich.Notice != "" || len(rich.Runs) != 2 {
		t.Fatal(text, rich, err)
	}
	first, second := rich.Runs[0], rich.Runs[1]
	if first.End != 5 || second.End != 6 || first.Font.Name != "宋体" || first.Font.Size != 14 || first.Font.Color != "#1255AA" || first.Font.Bold == nil || !*first.Font.Bold || first.Font.Italic == nil || *first.Font.Italic || first.Font.Strike == nil || *first.Font.Strike || first.Font.Underline != "doubleAccounting" || first.Font.Vertical != "superscript" {
		t.Fatal(first)
	}
	if second.Font.Bold == nil || *second.Font.Bold || second.Font.Italic != nil || second.Font.Underline != "none" || second.Font.Vertical != "baseline" {
		t.Fatal("inheritance and explicit false were conflated", second)
	}
	if err := ValidateRichText(text, rich); err != nil {
		t.Fatal(err)
	}
	var ordinary sharedStringItem
	if err := xml.Unmarshal([]byte(input), &ordinary); err != nil || ordinary.Text != text {
		t.Fatal("styled decoding changed cell evidence", ordinary.Text, err)
	}
	encoded, _ := json.Marshal(rich)
	var reopened RichText
	if err := json.Unmarshal(encoded, &reopened); err != nil || !reflect.DeepEqual(rich, &reopened) {
		t.Fatal("font false state lost on persistence", string(encoded), err)
	}
}

func TestRichTextPlainAndPhoneticOnlyContentAllocatesNoStyledResult(t *testing.T) {
	for _, input := range []string{`<si><t>001</t></si>`, `<si><r><t>001</t></r><r><t>原文</t></r></si>`, `<is><t>&lt;safe&gt;&#13;值</t><rPh><t>注音</t></rPh></is>`, `<si><rPh><t>ignored</t></rPh></si>`} {
		text, rich, err := styledText(t, &Workbook{}, input)
		if err != nil || rich != nil {
			t.Fatal(input, rich, err)
		}
		var ordinary sharedStringItem
		if err := xml.Unmarshal([]byte(input), &ordinary); err != nil || ordinary.Text != text {
			t.Fatal(input, text, ordinary.Text, err)
		}
	}
}

func TestRichTextThemeIndexedAutoAndTintColorsUseWorkbookPalette(t *testing.T) {
	w := &Workbook{ThemeColors: []string{"#224466"}, IndexedColors: []string{"#ABCDEF"}}
	text, rich, err := styledText(t, w, `<is><r><rPr><color theme="0" tint="0.5"/></rPr><t>甲</t></r><r><rPr><color indexed="0"/></rPr><t>乙</t></r><r><rPr><color auto="1"/></rPr><t>丙</t></r></is>`)
	if err != nil || rich.Notice != "" || text != "甲乙丙" || rich.Runs[0].Font.Color != tintHex("#224466", 0.5) || rich.Runs[1].Font.Color != "#ABCDEF" || !rich.Runs[2].Font.AutoColor || rich.Runs[2].Font.Color != "" {
		t.Fatal(text, rich, err)
	}
	if err := ValidateRichText(text, rich); err != nil {
		t.Fatal(err)
	}
}

func TestRichTextUnsupportedOrInvalidEffectsKeepTextAndReportIncompletePresentation(t *testing.T) {
	for _, property := range []string{`<shadow/>`, `<b val="maybe"/>`, `<sz val="NaN"/>`, `<sz val="410"/>`, `<color theme="999"/>`, `<color theme="abc"/>`, `<color rgb="FF112233" tint="abc"/>`, `<color rgb="FF112233" tint="NaN"/>`, `<u val="mystery"/>`, `<vertAlign val="mystery"/>`, `<b/><b val="0"/>`, `<scheme val="major"/>`} {
		text, rich, err := styledText(t, &Workbook{}, `<si><r><rPr>`+property+`</rPr><t>001 &lt;原文&gt;</t></r></si>`)
		if err != nil || text != "001 <原文>" || rich == nil || rich.Notice == "" {
			t.Fatal(property, text, rich, err)
		}
		if err := ValidateRichText(text, rich); err != nil {
			t.Fatal(property, err)
		}
	}
}

func TestRichTextBoundedRunMetadataDoesNotTruncateSourceText(t *testing.T) {
	input := `<si>` + strings.Repeat(`<r><rPr><b/></rPr><t>😀</t></r><r><rPr><b val="0"/></rPr><t>😀</t></r>`, formats.MaxCellRichRuns/2+1) + `</si>`
	text, rich, err := styledText(t, &Workbook{}, input)
	if err != nil || text != strings.Repeat("😀", formats.MaxCellRichRuns+2) || len(rich.Runs) != formats.MaxCellRichRuns || rich.Notice == "" {
		t.Fatal(len(text), rich, err)
	}
	if err := ValidateRichText(text, rich); err != nil {
		t.Fatal(err)
	}
	// Direct text before/after runs retains its position and inherited font.
	text, rich, err = styledText(t, &Workbook{}, `<si><t>前</t><r><rPr><i/></rPr><t>中</t></r><t>后</t></si>`)
	if err != nil || text != "前中后" || len(rich.Runs) != 3 || rich.Runs[0].Font.Italic != nil || rich.Runs[1].Font.Italic == nil || rich.Runs[2].Font.Italic != nil {
		t.Fatal(text, rich, err)
	}
	if err := ValidateRichText(text, rich); err != nil {
		t.Fatal(err)
	}
}

func TestRichTextValidationRejectsSurrogateSplitsMalformedRangesAndStyleInjection(t *testing.T) {
	for _, rich := range []*RichText{
		{Runs: []RichTextRun{{End: 2}}}, // middle of the emoji in 中😀文
		{Runs: []RichTextRun{{End: 1}, {End: 1}, {End: 4}}},
		{Runs: []RichTextRun{{End: 5}}},
		{Runs: []RichTextRun{{End: 3}}}, // silently missing the tail
		{Runs: []RichTextRun{{End: 4, Font: RichFont{Color: "red;position:fixed"}}}},
		{Runs: []RichTextRun{{End: 4, Font: RichFont{Size: 410}}}},
		{Runs: []RichTextRun{{End: 4, Font: RichFont{Underline: "<script>"}}}},
	} {
		if err := ValidateRichText("中😀文", rich); err == nil {
			t.Fatal("invalid presentation metadata accepted", rich)
		}
	}
	for _, input := range []string{`<si><r><rPr><b/>`, `<si><r><t>原文</t></r>`, `<si><t>原文</t><unknown>`} {
		if _, _, err := styledText(t, &Workbook{}, input); err == nil {
			t.Fatal("truncated rich XML accepted", input)
		}
	}
}
