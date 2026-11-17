package workbook

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestFlatRichValueFastPathMatchesReferenceAndNeverReadsFontMetadata(t *testing.T) {
	for _, input := range []string{
		`<r><t>中😀001</t></r><r><rPr><b val="0"/><color rgb="FF1255AA"/></rPr><t xml:space="preserve"> 尾 </t></r>`,
		` <r><rPr><t val="ignored"/><font val="x &gt; y"/></rPr><t>正文</t></r> `,
		`<r><rPr><unknown><t>metadata</t></unknown></rPr><t>正文</t></r>`,
		`<r><t>&lt;source&gt;</t></r><rPh><t>注音</t></rPh>`,
		`<r><rPr><i a='one' b="two"/></rPr><t>文本</t></r>`,
		`<r><rPr><b/></rPr><t><![CDATA[中😀]]></t></r>`,
		`<r><rPr><b/></rPr><t>A&#13;&#10;B</t></r>`,
		`<r><t></t></r><r><t>尾</t></r>`,
	} {
		var reference sharedStringItem
		if err := xml.Unmarshal([]byte("<si>"+input+"</si>"), &reference); err != nil {
			t.Fatal(err)
		}
		value, ok := inlineStringValue([]byte("<is>" + input + "</is>"))
		if !ok || value != reference.Text {
			t.Fatalf("value differs from reference for %s: %q / %q", input, value, reference.Text)
		}
	}
	for _, bad := range []string{`<r><rPr><b val=1/></rPr><t>正文</t></r>`, `<r><rPr><b x="a"x="b"/></rPr><t>x</t></r>`, `<r><rPr>text<b/></rPr><t>x</t></r>`, `<r><t>正文</t><extra><t>metadata</t></extra></r>`, `<r><rPr><b x="bad<value"/></rPr><t>x</t></r>`, `<r><t>正文</t></rPh>`, `<r><rPr><b/></rPr><t>`} {
		if _, ok := fastSimpleRichContent([]byte(bad)); ok {
			t.Fatal("complex or malformed XML accepted by flat fast path", bad)
		}
	}
}

func FuzzFlatRichValueAgreesWithXML(f *testing.F) {
	for _, value := range []string{`<r><t>中😀001</t></r>`, `<r><rPr><b val="0"/><color rgb="FF1255AA"/></rPr><t>文本</t></r>`, `<r><rPr><x/></rPr><t></t></r>`, `<r><rPr><x a='one' b='two'/></rPr><t>尾</t></r>`, `<r><rPr><x a='</rPr>'/></rPr><t>尾</t></r>`} {
		f.Add(value)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 64<<10 {
			t.Skip()
		}
		got, ok := fastSimpleRichContent([]byte(input))
		if !ok {
			return
		}
		var item sharedStringItem
		if err := xml.Unmarshal([]byte("<si>"+input+"</si>"), &item); err != nil || got != item.Text {
			t.Fatalf("accepted fast value differs from XML: %q / %q (%v), input %s", got, item.Text, err, strings.TrimSpace(input))
		}
	})
}
