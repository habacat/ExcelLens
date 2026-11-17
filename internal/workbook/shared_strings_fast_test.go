package workbook

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func referenceSharedXML(data []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "si" {
			continue
		}
		var item sharedStringItem
		if err := dec.DecodeElement(&item, &start); err != nil {
			return nil, err
		}
		out = append(out, item.Text)
	}
}
func TestFastSharedStringsParity(t *testing.T) {
	cases := []string{
		`<?xml version="1.0"?><sst><si><t>ABC</t></si><si><t>你好世界</t></si><si><t xml:space="preserve">   pad   </t></si><si><t>0</t></si></sst>`,
		`<sst><si><t>an &amp; b</t></si><si><t>&#13;test</t></si><si><r><t>one</t></r><r><t>two</t></r><rPh><t>skip</t></rPh></si></sst>`,
		`<sst><si><t></t></si><si><t xml:space="default">X</t></si><si><r><t>&lt;safe&gt;</t></r></si></sst>`,
		`<sst><si><t>abc]]&gt;x</t></si></sst>`,
		`<sst><!-- si is allowed in comments: <si><t>trap</t></si> --><si><t>visible</t></si></sst>`,
		`<sst><si><t><![CDATA[hello > world]]></t></si></sst>`,
	}
	for i, s := range cases {
		got, ok := decodeSharedStringsFast([]byte(s))
		want, err := referenceSharedXML([]byte(s))
		if err != nil {
			t.Fatalf("case %d invalid fixture: %v", i, err)
		}
		if !ok { // For complex structures the strict decoder must remain equivalent.
			t.Logf("case %d uses full strict fallback", i)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("case %d got=%q want=%q", i, got, want)
		}
	}
	bad := []string{`<sst><si><t>foo</t></si>`, `<sst><si><t>foo]]>bar</t></si></sst>`, `<sst><si><t>x</t></si><!-- comment --></sst>`, `<sst><si><t>` + string([]byte{0}) + `</t></si></sst>`}
	for _, s := range bad {
		if _, ok := decodeSharedStringsFast([]byte(s)); ok {
			t.Errorf("accepted invalid/unsupported fixture %q", s)
		}
	}
}

// Real fixtures are opt-in and never included in the distributed source tree.
// EXCELLENS_REAL_FILES is a pipe-separated list of user-provided workbooks.
func TestSharedStringsExactRealParity(t *testing.T) {
	paths := os.Getenv("EXCELLENS_REAL_FILES")
	if paths == "" {
		t.Skip("real workbooks not provided")
	}
	for _, p := range strings.Split(paths, "|") {
		p := p
		t.Run(fmt.Sprintf("part-%d", len(p)), func(t *testing.T) {
			zr, err := zip.OpenReader(p)
			if err != nil {
				t.Fatal(err)
			}
			defer zr.Close()
			for _, f := range zr.File {
				if f.Name != "xl/sharedStrings.xml" {
					continue
				}
				r, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				data, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				fast, ok := decodeSharedStringsFast(data)
				if !ok {
					t.Fatal("unexpected fast decoder fallback for real workbook")
				}
				strict, err := referenceSharedXML(data)
				if err != nil {
					t.Fatal(err)
				}
				if len(fast) != len(strict) {
					t.Fatalf("count mismatch %d vs %d", len(fast), len(strict))
				}
				for i := range strict {
					if fast[i] != strict[i] {
						t.Fatalf("string index %d differs", i)
					}
				}
				t.Logf("verified %d shared strings, %.2f MiB of XML", len(fast), float64(len(data))/(1<<20))
				return
			}
			t.Fatal("no sharedStrings.xml")
		})
	}
}
