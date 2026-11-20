package server

import (
	"fmt"
	"testing"

	"excellens/internal/editdoc"
)

func TestRangeTransferRetainsCopiedRevisionAcrossOverlapAndRecovery(t *testing.T) {
	isolateTestStorage(t)
	file := editWorkbookFixture(t, map[string]string{
		"Data": `<sheetData><row r="1"><c r="A1"><v>7</v></c></row><row r="2" xmlns:x="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><x:c r="A2"><x:v>3</x:v></x:c></row></sheetData><mergeCells count="1"><mergeCell ref="A1:B1"/></mergeCells>`,
		"Sum":  `<sheetData/>`,
	}, "", "")
	original := printScaleHash(t, file)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { app.Close() }()
	if err = app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	state := decodeResponse[editState](t, editHTTP(t, app, "start", editRequest{Book: sourceBookID(app.currentWorkbook())}, 200))
	key := state.Document.Key
	apply := func(op editdoc.Operation, status int) editCommitResult {
		t.Helper()
		raw := editHTTP(t, app, "commit", editRequest{Document: key, Operation: op}, status)
		if status != 200 {
			return editCommitResult{}
		}
		return decodeResponse[editCommitResult](t, raw)
	}
	op := func(id int, rev uint64, action string) editdoc.Operation {
		return editdoc.Operation{Request: fmt.Sprintf("%032x", id), Revision: rev, Action: action}
	}
	change := op(1, 1, "change")
	change.Changes = []editdoc.Change{{Address: editdoc.Address{Sheet: "Data", Row: 1, Col: 1}, Value: editdoc.Value{Kind: "number", Raw: "11"}}}
	apply(change, 200)
	copy := op(2, 2, "structure")
	copy.Structure = &editdoc.Structure{Kind: "copyRange", Sheet: "Data", Range: "A1:B2", DestinationSheet: "Data", Destination: "A2", SourceRevision: 2}
	result := apply(copy, 200)
	if replay := apply(copy, 200); replay.Committed != result.Committed || replay.Book != result.Book {
		t.Fatal(replay)
	}
	app.Close()
	app, err = New()
	if err != nil {
		t.Fatal(err)
	}
	repeat := op(3, 3, "structure")
	repeat.Structure = &editdoc.Structure{Kind: "copyRange", Sheet: "Data", Range: "A1:B2", DestinationSheet: "Sum", Destination: "C4", SourceRevision: 2}
	apply(repeat, 200)
	check := func(ref string, revision uint64, want string) {
		t.Helper()
		cell := decodeResponse[struct{ Value editdoc.Value }](t, editHTTP(t, app, "read", editRequest{Document: key, Sheet: "Sum", Ref: ref, Revision: revision}, 200))
		if cell.Value.Raw != want {
			t.Fatalf("revision %d, %s: %+v, want %s", revision, ref, cell, want)
		}
	}
	check("C4", 4, "11")
	check("C5", 4, "3")
	stale := op(4, 4, "structure")
	invalid := *repeat.Structure
	invalid.Kind = "cutRange"
	stale.Structure = &invalid
	apply(stale, 422)
	check("C5", 4, "3")
	apply(op(5, 4, "undo"), 200)
	check("C4", 5, "")
	apply(op(6, 5, "redo"), 200)
	check("C4", 6, "11")
	check("C5", 6, "3")
	if printScaleHash(t, file) != original {
		t.Fatal("source workbook changed")
	}
}
