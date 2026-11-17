package export

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func validateStyleXML(raw string) error {
	d := xml.NewDecoder(strings.NewReader(raw))
	for {
		_, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func TestStyleXMLPatchesPreserveUnknownBytesAliasesAndCanonicalSectionOrder(t *testing.T) {
	x := `<?xml version="1.0"?><m:styleSheet xmlns:m="urn:main" xmlns:x="urn:extension" x:hint="x:Kind"><m:fonts count="1"><m:font><m:name val="宋体"/><x:color val="x:Paint"/><m:b/></m:font></m:fonts><m:cellXfs count="1"><m:xf fontId="0"/></m:cellXfs><m:dxfs count="0"/><x:extra x:kind="x:Kind"/></m:styleSheet>`
	d, err := parseStyleXML(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := d.record("fonts", 0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseStyleFragment(context.Background(), d.wrapper("fonts"), raw)
	if err != nil {
		t.Fatal(err)
	}
	font, err := rewriteStyleFragment(f, nil, map[string]string{"b": `<m:b val="0"/>`, "color": `<m:color rgb="FFFF0000"/>`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.appendRecord("fonts", font)
	if err != nil || id != 1 {
		t.Fatal(id, err)
	}
	if again, err := d.appendRecord("fonts", font); err != nil || again != id {
		t.Fatal("font was not deduplicated", again, err)
	}
	if _, err := d.appendRecord("fills", `<m:fill><m:patternFill patternType="none"/></m:fill>`); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := d.write(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if err := validateStyleXML(out.String()); err != nil {
		t.Fatal(err, out.String())
	}
	for _, keep := range []string{`x:hint="x:Kind"`, `<x:extra x:kind="x:Kind"/>`, raw, `<x:color val="x:Paint"/>`, `<m:b val="0"/>`, `<m:fonts count="2">`} {
		if !strings.Contains(out.String(), keep) {
			t.Fatal("source markup was lost", keep, out.String())
		}
	}
	if strings.Index(out.String(), "<m:fills") > strings.Index(out.String(), "<m:cellXfs") {
		t.Fatal("new section violates source style order", out.String())
	}
}

func TestStyleXMLRewritesEmptyContainersAndRecordsWithQuotedValues(t *testing.T) {
	for _, x := range []string{`<styleSheet/>`, `<styleSheet><fonts count='0'/><cellXfs count='0'/></styleSheet>`} {
		d, err := parseStyleXML(context.Background(), x)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.appendRecord("fonts", `<font><name val="A&amp;B"/></font>`); err != nil {
			t.Fatal(err)
		}
		if _, err := d.appendRecord("cellXfs", `<xf fontId="0"/>`); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := d.write(context.Background(), &out); err != nil {
			t.Fatal(err)
		}
		if err := validateStyleXML(out.String()); err != nil {
			t.Fatal(err, out.String())
		}
		reparsed, err := parseStyleXML(context.Background(), out.String())
		if err != nil || reparsed.section("fonts").count() != 1 || reparsed.section("cellXfs").count() != 1 {
			t.Fatal(out.String(), err)
		}
	}
	head, err := patchStyleHead(`<m:xf x:q='keep &amp; value' fontId = '0'/>`, map[string]string{"fontId": "7", "applyFont": "1"}, true)
	if err != nil || head != `<m:xf x:q='keep &amp; value' fontId = '7' applyFont="1">` {
		t.Fatal("unknown attributes or quotes changed", head, err)
	}
	if _, err := patchStyleHead(`<font a="1" a="2"/>`, nil, false); err == nil {
		t.Fatal("duplicate attributes accepted")
	}
}

func TestStyleXMLInsertsBeforeAReplacedSiblingAndKeepsSamePositionInsertionOrder(t *testing.T) {
	x := `<styleSheet><borders count="1"><border><top style="thin"/><bottom style="double"/></border></borders></styleSheet>`
	d, err := parseStyleXML(context.Background(), x)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := d.record("borders", 0)
	f, err := parseStyleFragment(context.Background(), d.wrapper("borders"), raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := rewriteStyleFragment(f, nil, map[string]string{"left": `<left style="dashed"/>`, "right": `<right style="dotted"/>`, "top": `<top style="thick"/>`}, []string{"left", "right", "top", "bottom"})
	want := `<border><left style="dashed"/><right style="dotted"/><top style="thick"/><bottom style="double"/></border>`
	if err != nil || got != want {
		t.Fatal("insertion/replacement at the same byte lost schema order", got, err)
	}
	if err := validateStyleXML(got); err != nil {
		t.Fatal(err)
	}
}

type styleWriteFunc func([]byte) (int, error)

func (f styleWriteFunc) Write(p []byte) (int, error) { return f(p) }

func TestStyleXMLOutputRejectsShortWritesAndCancelsWithinLargeSpans(t *testing.T) {
	raw := `<styleSheet><extLst>` + strings.Repeat("x", 128<<10) + `</extLst></styleSheet>`
	if err := writeStyleEdits(context.Background(), styleWriteFunc(func(p []byte) (int, error) { return len(p) / 2, nil }), raw, nil, len(raw)); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short successful write accepted", err)
	}
	want := errors.New("disk full")
	if err := writeStyleEdits(context.Background(), styleWriteFunc(func([]byte) (int, error) { return 0, want }), raw, nil, len(raw)); !errors.Is(err, want) {
		t.Fatal("write failure lost", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	written := 0
	err := writeStyleEdits(ctx, styleWriteFunc(func(p []byte) (int, error) {
		written += len(p)
		cancel()
		return len(p), nil
	}), raw, nil, len(raw))
	if !errors.Is(err, context.Canceled) || written > 32<<10 || written == 0 {
		t.Fatal("cancellation waited for the whole XML part", written, err)
	}
	var out bytes.Buffer
	if err := writeStyleEdits(context.Background(), &out, raw, nil, len(raw)-1); err == nil || out.Len() != 0 {
		t.Fatal("oversized output partially written", out.Len(), err)
	}
}

func TestStyleXMLRejectsAmbiguousStructureAndFormatLimits(t *testing.T) {
	for _, raw := range []string{
		`<styleSheet><fonts/><fonts/></styleSheet>`,
		`<styleSheet a="1" a="2"/>`,
		`<styleSheet xmlns:a="urn:one" xmlns:b="urn:one"><fonts a:count="1" b:count="2"/></styleSheet>`,
		`<styleSheet/><styleSheet/>`,
		`<styleSheet><fonts>`,
		`<!DOCTYPE styleSheet><styleSheet/>`,
		`<styleSheet>` + strings.Repeat(`<x>`, formats.XLSXStyleXMLDepth) + strings.Repeat(`</x>`, formats.XLSXStyleXMLDepth) + `</styleSheet>`,
		`<styleSheet><fonts extra="` + strings.Repeat("x", formats.XLSXStyleHeaderBytes) + `"/></styleSheet>`,
	} {
		if _, err := parseStyleXML(context.Background(), raw); err == nil {
			t.Fatal("ambiguous/over-limit XML accepted", raw[:min(len(raw), 80)])
		}
	}
	raw := `<styleSheet><cellXfs>` + strings.Repeat(`<xf/>`, formats.XLSXStyleRecords) + `</cellXfs></styleSheet>`
	d, err := parseStyleXML(context.Background(), raw)
	if err != nil {
		t.Fatal("exact record limit must be readable", err)
	}
	if _, err := d.appendRecord("cellXfs", `<xf fontId="1"/>`); err == nil {
		t.Fatal("style count exceeded the target format limit")
	}
	fragment := `<font>` + strings.Repeat(`<unknown/>`, formats.XLSXStyleChildRecords+1) + `</font>`
	if _, err := parseStyleFragment(context.Background(), d.wrapper("fonts"), fragment); err == nil {
		t.Fatal("unbounded direct-child metadata was retained")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseStyleXML(ctx, `<styleSheet/>`); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled parse succeeded", err)
	}
}

func FuzzStyleHeadAttributePatchRetainsUnchangedAttributes(f *testing.F) {
	f.Add("A&B", "x:Kind", false)
	f.Add("中😀'\"", "some = text", true)
	f.Fuzz(func(t *testing.T, value, unknown string, self bool) {
		if len(value) > 1024 || len(unknown) > 1024 {
			return
		}
		// EscapeText normalizes disallowed XML controls; compare decoded source.
		tail := ">"
		if self {
			tail = "/>"
		}
		head := `<font keep="` + xmlAttr(unknown) + `" value='old'` + tail
		patched, err := patchStyleHead(head, map[string]string{"value": value}, !self)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(patched, `keep="`+xmlAttr(unknown)+`"`) {
			t.Fatal("unknown raw attribute changed", patched)
		}
		complete := patched
		if !self {
			complete += "</font>"
		}
		var got, want struct {
			Value string `xml:"value,attr"`
			Keep  string `xml:"keep,attr"`
		}
		if err := xml.Unmarshal([]byte(complete), &got); err != nil {
			t.Fatal(err, patched)
		}
		if err := xml.Unmarshal([]byte(`<font keep="`+xmlAttr(unknown)+`" value="`+xmlAttr(value)+`"/>`), &want); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatal("attribute meaning changed", got, want)
		}
	})
}
