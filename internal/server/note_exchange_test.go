package server

import (
	"excellens/internal/editdoc"
	"excellens/internal/workbook"
	"fmt"
	"testing"
)

func TestReviewNotesSurviveSaveUndoRedoAndPreserveExistingNote(t *testing.T) {
	isolateTestStorage(t)
	path := editWorkbookFixture(t, map[string]string{"台账": `<sheetData><row r="1"><c r="A1"><v>1</v></c></row></sheetData>`}, "", "")
	original := printScaleHash(t, path)
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	s := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(a.currentWorkbook())}, 200))
	key := s.Document.Key
	rev := s.Document.Revision
	nonce := 0
	commit := func(action string, notes []editdoc.ReviewNote, status int) {
		t.Helper()
		nonce++
		op := editdoc.Operation{Action: action, Revision: rev, Request: fmt.Sprintf("%032x", nonce)}
		if notes != nil {
			op.Structure = &editdoc.Structure{Kind: "reviewNotes", Sheet: "台账", Notes: notes}
		}
		resp := editHTTP(t, a, "commit", editRequest{Document: key, Operation: op}, status)
		if status == 200 {
			rev = decodeResponse[editCommitResult](t, resp).Committed
		}
	}
	check := func(want ...string) {
		t.Helper()
		editHTTP(t, a, "base", editRequest{Document: key, Revision: rev, Book: sourceBookID(a.currentWorkbook())}, 200)
		items := decodeResponse[struct{ Items []workbook.SourceNote }](t, featureCall(t, a, "GET", "/api/notes?sheet=台账&book="+sourceBookID(a.currentWorkbook()), ""))
		if len(items.Items) != len(want) {
			t.Fatal(items, rev)
		}
		for i, x := range want {
			if items.Items[i].Text != x || items.Items[i].Author != "ExcelLens" {
				t.Fatal(items)
			}
		}
	}
	text := "待复核\n  金额<&>\n"
	commit("structure", []editdoc.ReviewNote{{Sheet: "台账", Ref: "A1", Text: text}}, 200)
	check(text)
	commit("structure", []editdoc.ReviewNote{{Sheet: "台账", Ref: "D9", Text: "第二条"}}, 200)
	check(text, "第二条")
	commit("undo", nil, 200)
	check(text)
	commit("redo", nil, 200)
	check(text, "第二条")
	art := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: key, Revision: rev}, 200))
	editHTTP(t, a, "open", editRequest{Document: key, Artifact: art.Key, Book: sourceBookID(a.currentWorkbook())}, 200)
	wb := a.currentWorkbook()
	items, _, err := wb.SheetNotes(t.Context(), wb.Sheets[0])
	if err != nil || len(items) != 2 || items[0].Ref != "A1" || items[1].Ref != "D9" || printScaleHash(t, path) != original {
		t.Fatal(items, err)
	}
}
