package server

import (
	"fmt"
	"testing"

	"excellens/internal/editdoc"
)

func TestStructuralRevisionRecoveryAfterCellEdit(t *testing.T) {
	isolateTestStorage(t)
	path := editWorkbookFixture(t, map[string]string{
		"Data": `<sheetData><row r="1"><c r="A1"><v>4</v></c></row></sheetData>`,
		"Sum":  `<sheetData><row r="1"><c r="A1"><f>Data!A1+1</f><v>5</v></c></row></sheetData>`,
	}, "", "")
	hash := printScaleHash(t, path)
	newApp := func() *App {
		t.Helper()
		a, err := New()
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newApp()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	original := sourceBookID(a.currentWorkbook())
	state := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: original}, 200))
	key := state.Document.Key
	commit := func(op editdoc.Operation, status int) editCommitResult {
		t.Helper()
		data := editHTTP(t, a, "commit", editRequest{Document: key, Operation: op}, status)
		if status != 200 {
			return editCommitResult{}
		}
		return decodeResponse[editCommitResult](t, data)
	}
	op := func(n int, action string) editdoc.Operation {
		return editdoc.Operation{Request: fmt.Sprintf("%032x", n), Revision: uint64(n), Action: action}
	}
	value := op(1, "change")
	value.Changes = []editdoc.Change{{Address: editdoc.Address{Sheet: "Data", Row: 1, Col: 1}, Value: editdoc.Value{Kind: "number", Raw: "9"}}}
	commit(value, 200)
	insert := op(2, "structure")
	insert.Structure = &editdoc.Structure{Kind: "insertRows", Sheet: "Data", At: 1, Count: 1}
	applied := commit(insert, 200)
	if applied.Book == original || applied.Document.Structures != 1 {
		t.Fatal(applied)
	}
	commit(insert, 200)
	read := func(sheet, ref string, revision uint64, want string) {
		t.Helper()
		cell := decodeResponse[struct{ Value editdoc.Value }](t, editHTTP(t, a, "read", editRequest{Document: key, Revision: revision, Sheet: sheet, Ref: ref}, 200))
		if cell.Value.Raw != want {
			t.Fatalf("%s!%s: %+v want %s", sheet, ref, cell, want)
		}
	}
	read("Data", "A2", 3, "9")
	read("Sum", "A1", 3, "=Data!A2+1")
	a.Close()
	a = newApp()
	defer a.Close()
	base := decodeResponse[struct{ Book string }](t, editHTTP(t, a, "base", editRequest{Document: key, Revision: 3}, 200))
	if base.Book != applied.Book {
		t.Fatal(base)
	}
	reopened := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: base.Book}, 200))
	if reopened.Document.Key != key || reopened.OriginalBook != original {
		t.Fatal(reopened)
	}
	conflict := op(3, "structure")
	conflict.Revision = 1
	conflict.Structure = insert.Structure
	commit(conflict, 409)
	refused := op(3, "structure")
	refused.Structure = &editdoc.Structure{Kind: "renameSheet", Sheet: "Data", Name: "Sum"}
	commit(refused, 422)
	undo := commit(op(3, "undo"), 200)
	if undo.Book != original {
		t.Fatal(undo)
	}
	read("Data", "A1", 4, "9")
	read("Sum", "A1", 4, "=Data!A1+1")
	commit(op(4, "undo"), 200)
	read("Data", "A1", 5, "4")
	commit(op(5, "redo"), 200)
	redo := commit(op(6, "redo"), 200)
	if redo.Book != applied.Book {
		t.Fatal(redo)
	}
	read("Data", "A2", 7, "9")
	read("Sum", "A1", 7, "=Data!A2+1")
	if printScaleHash(t, path) != hash {
		t.Fatal("original workbook changed")
	}
}
