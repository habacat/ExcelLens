package server

import (
	"fmt"
	"testing"

	"excellens/internal/editdoc"
)

func TestGridEntryKeepsFormatAndRevisionAcrossFillAndRejectedPaste(t *testing.T) {
	isolateTestStorage(t)
	path := editWorkbookFixture(t, map[string]string{"Data": `<sheetData><row r="1"><c r="A1" s="1"><v>0.25</v></c><c r="B1" t="inlineStr"><is><t>001</t></is></c></row></sheetData>`},
		`<cellXfs count="2"><xf numFmtId="0"/><xf numFmtId="10" applyNumberFormat="1"/></cellXfs>`, "")
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	doc := decodeResponse[editState](t, editHTTP(t, app, "start", editRequest{Book: sourceBookID(app.currentWorkbook())}, 200)).Document.Key
	at := func(row, col int) editdoc.Address { return editdoc.Address{Sheet: "Data", Row: row, Col: col} }
	input := func(a editdoc.Address, kind, raw string) editdoc.Change {
		return editdoc.Change{Address: a, Value: editdoc.Value{Kind: kind, Raw: raw}}
	}
	commit := func(revision uint64, op editdoc.Operation, status int) {
		t.Helper()
		op.Request = fmt.Sprintf("%032x", revision)
		op.Revision = revision
		editHTTP(t, app, "commit", editRequest{Document: doc, Operation: op}, status)
	}
	req := editRequest{Document: doc, Revision: 1, Sheet: "Data", Cells: []editdoc.Address{at(1, 1), at(1, 2), at(3, 3)}}
	kinds := decodeResponse[[]string](t, editHTTP(t, app, "input-kinds", req, 200))
	if len(kinds) != 3 || kinds[0] != "number" || kinds[1] != "text" || kinds[2] != "missing" {
		t.Fatal(kinds)
	}
	seed := at(2, 2)
	cells := []editdoc.Change{input(seed, "formula", "=$A1+1"), input(at(2, 3), "formula", "=$A1+1"), input(at(3, 2), "formula", "=$A2+1")}
	commit(1, editdoc.Operation{Action: "change", Changes: cells, InferNumberFormat: true, EntryAnchor: &seed}, 200)
	editHTTP(t, app, "input-kinds", req, 409)
	read := func(rev uint64, ref string) editdoc.Value {
		t.Helper()
		return decodeResponse[struct{ Value editdoc.Value }](t, editHTTP(t, app, "read", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: ref}, 200)).Value
	}
	for _, ref := range []string{"B2", "C2", "B3"} {
		v := read(2, ref)
		if v.NumberStyle != "1" || v.Style != "0" {
			t.Fatalf("%s: %+v", ref, v)
		}
	}
	// A forged style mutation and a batch with a bad final address must
	// leave the first cell, the style descriptor and the revision intact.
	forged := input(seed, "number", "9")
	forged.Value.Style = "0"
	forged.Value.NumberStyle = "1"
	commit(2, editdoc.Operation{Action: "change", Changes: []editdoc.Change{forged}}, 422)
	forged = input(seed, "formula", "=A1")
	forged.Value.BuiltinFormat = 14
	commit(2, editdoc.Operation{Action: "change", InferNumberFormat: true, Changes: []editdoc.Change{forged}}, 422)
	commit(2, editdoc.Operation{Action: "change", Changes: []editdoc.Change{input(seed, "blank", ""), input(at(1, 16385), "number", "3")}}, 422)
	if v := read(2, "B2"); v.Raw != "=$A1+1" || v.NumberStyle != "1" {
		t.Fatal(v)
	}
	commit(2, editdoc.Operation{Action: "undo"}, 200)
	if v := read(3, "B2"); v.NumberStyle != "" || v.Kind != "missing" {
		t.Fatal(v)
	}
	commit(3, editdoc.Operation{Action: "redo"}, 200)
	preview := decodeResponse[editBatchPreview](t, editHTTP(t, app, "preview", editRequest{Document: doc, Revision: 4, Sheet: "Data", Ref: "B4", End: "B4", Mode: "fill-down"}, 200))
	if len(preview.Changes) != 1 || preview.Changes[0].Value.Raw != "=$A3+1" || preview.Changes[0].Value.NumberStyle != "1" {
		t.Fatal(preview)
	}
	editHTTP(t, app, "preview", editRequest{Document: doc, Revision: 4, Sheet: "Data", Ref: "A1", End: "A1", Mode: "fill-right"}, 422)
}

func TestFormulaEntryShowsExistingNumberFormatOnStyledBlank(t *testing.T) {
	isolateTestStorage(t)
	path := editWorkbookFixture(t, map[string]string{"Data": `<sheetData><row r="1"><c r="A1" s="1"><v>0.5</v></c><c r="B1" s="2"/></row></sheetData>`},
		`<cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="10"/><xf numFmtId="2"/></cellXfs>`, "")
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	book := sourceBookID(app.currentWorkbook())
	doc := decodeResponse[editState](t, editHTTP(t, app, "start", editRequest{Book: book}, 200)).Document.Key
	a := editdoc.Address{Sheet: "Data", Row: 1, Col: 2}
	op := editdoc.Operation{Revision: 1, Request: fmt.Sprintf("%032x", 1), Action: "change", InferNumberFormat: true, Changes: []editdoc.Change{{Address: a, Value: editdoc.Value{Kind: "formula", Raw: "=SUM(A1,A1)"}}}}
	editHTTP(t, app, "commit", editRequest{Document: doc, Operation: op, Direct: true}, 200)
	grid := decodeResponse[struct {
		Calculated map[string]map[string]struct {
			Display string
			Style   int
		}
	}](t, editHTTP(t, app, "grid", editRequest{Document: doc, Book: book, Revision: 2, Mode: "projection"}, 200))
	cell := grid.Calculated["Data"]["B1"]
	if cell.Display != "1.00" || cell.Style != 2 {
		t.Fatalf("formatted target: %+v", cell)
	}
}

func TestReenterFormulaInUnlockedGeneralCell(t *testing.T) {
	isolateTestStorage(t)
	const formula = "=SUM(A1,A1)"
	path := editWorkbookFixture(t, map[string]string{"Data": `<sheetData><row r="1"><c r="A1" s="1"><v>0.25</v></c><c r="B1" s="2"><f>SUM(A1,A1)</f><v>0.5</v></c></row></sheetData><sheetProtection sheet="1"/>`},
		`<cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="10"/><xf numFmtId="0" applyProtection="1"><protection locked="0"/></xf></cellXfs>`, "")
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	book := sourceBookID(app.currentWorkbook())
	doc := decodeResponse[editState](t, editHTTP(t, app, "start", editRequest{Book: book}, 200)).Document.Key
	target := editdoc.Address{Sheet: "Data", Row: 1, Col: 2}
	op := editdoc.Operation{Action: "change", Revision: 1, Request: fmt.Sprintf("%032x", 1), InferNumberFormat: true, Changes: []editdoc.Change{{Address: target, Value: editdoc.Value{Kind: "formula", Raw: formula}}}}
	editHTTP(t, app, "commit", editRequest{Document: doc, Operation: op, Direct: true}, 200)
	value := decodeResponse[struct{ Value editdoc.Value }](t, editHTTP(t, app, "read", editRequest{Document: doc, Revision: 2, Sheet: "Data", Ref: "B1"}, 200)).Value
	if value.Raw != formula || value.Style != "2" || value.NumberStyle != "1" {
		t.Fatal(value)
	}
	editHTTP(t, app, "save", editRequest{Document: doc, Revision: 2}, 200)
	op.Revision, op.Request, op.Changes[0].Col = 2, fmt.Sprintf("%032x", 2), 3
	editHTTP(t, app, "commit", editRequest{Document: doc, Operation: op, Direct: true}, 422)
}
