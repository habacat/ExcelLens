package server

import (
	"fmt"
	"testing"

	"excellens/internal/editdoc"
)

// Copy between independent style tables, including a destination whose
// package does not have a styles part. Replaying the same paste is idempotent.
func TestPortableRangeImportsStylesAndMergesAsOneRevision(t *testing.T) {
	isolateTestStorage(t)
	src := editWorkbookFixture(t, map[string]string{"Source": `<sheetData><row r="1"><c r="A1" s="1"><v>0.125</v></c><c r="B1"><f>A1*2</f><v>0.25</v></c></row><row r="2"><c r="A2" t="inlineStr"><is><t>00123</t></is></c></row></sheetData><mergeCells count="1"><mergeCell ref="A2:B2"/></mergeCells>`}, `<numFmts count="1"><numFmt numFmtId="168" formatCode="0.0%"/></numFmts><cellXfs count="2"><xf/><xf numFmtId="168" applyNumberFormat="1"/></cellXfs>`, "")
	dest := editWorkbookFixture(t, map[string]string{"Dest": `<sheetData/>`}, "", "")
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	open := func(path string) editdoc.Key {
		t.Helper()
		if err := app.OpenPath(path); err != nil {
			t.Fatal(err)
		}
		return decodeResponse[editState](t, editHTTP(t, app, "start", editRequest{Book: sourceBookID(app.currentWorkbook())}, 200)).Document.Key
	}
	from := open(src)
	clip := decodeResponse[editClipboard](t, editHTTP(t, app, "copy", editRequest{Document: from, Revision: 1, Sheet: "Source", Ref: "A1", End: "B2", Mode: "grid"}, 200)).Portable
	if clip == nil || len(clip.Range.Merges) != 1 || len(clip.Range.Styles) != 2 {
		t.Fatal(clip)
	}
	to := open(dest)
	// Excel creates an external reference here, even if both sheets happen
	// to share a name. The destination must not silently reinterpret it.
	scoped := *clip.Range
	scoped.Cells = append([]editdoc.PortableCell(nil), scoped.Cells...)
	scoped.Cells[1].Value.Raw = "=Dest!A1"
	signed, err := app.signedRange(&scoped)
	if err != nil {
		t.Fatal(err)
	}
	other := editdoc.Structure{Kind: "pasteRange", Sheet: "Dest", Range: "D4:E5", Paste: &scoped}
	editHTTP(t, app, "preview", editRequest{Document: to, Revision: 1, Mode: "portable", Proof: signed.Proof, Operation: editdoc.Operation{Structure: &other}}, 422)
	op := editdoc.Structure{Kind: "pasteRange", Sheet: "Dest", Range: "D4:E5", Paste: clip.Range}
	request := editRequest{Document: to, Operation: editdoc.Operation{Action: "structure", Request: fmt.Sprintf("%032x", 1), Revision: 1, Structure: &op}}
	editHTTP(t, app, "commit", request, 422)
	request.Proof = clip.Proof
	for i := 0; i < 2; i++ {
		res := decodeResponse[editCommitResult](t, editHTTP(t, app, "commit", request, 200))
		if res.Committed != 2 || res.Document.Revision != 2 {
			t.Fatal(res)
		}
	}
	read := func(rev uint64, ref string) editdoc.Value {
		t.Helper()
		return decodeResponse[struct{ Value editdoc.Value }](t, editHTTP(t, app, "read", editRequest{Document: to, Revision: rev, Sheet: "Dest", Ref: ref}, 200)).Value
	}
	for rev := uint64(2); rev <= 4; rev++ {
		if rev > 2 {
			action := "undo"
			if rev == 4 {
				action = "redo"
			}
			editHTTP(t, app, "commit", editRequest{Document: to, Operation: editdoc.Operation{Action: action, Request: fmt.Sprintf("%032x", rev), Revision: rev - 1}}, 200)
		}
		value := read(rev, "D4")
		if rev == 3 {
			if value.Kind != "missing" {
				t.Fatal(value)
			}
			continue
		}
		if value.Kind != "number" || value.Raw != "0.125" || value.Style == "" || value.Style == "0" {
			t.Fatal(value)
		}
		if f := read(rev, "E4"); f.Kind != "formula" || f.Raw != "=D4*2" {
			t.Fatal(f)
		}
		if id := read(rev, "D5"); id.Kind != "text" || id.Raw != "00123" {
			t.Fatal(id)
		}
	}
}
