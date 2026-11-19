package server

import (
	"fmt"
	"slices"
	"testing"

	"excellens/internal/editdoc"
)

func TestEditCopyReferencedListUsesProposedBatchAndRestores(t *testing.T) {
	isolateTestStorage(t)
	const rangeFormula = "$D$1:$D$2"
	path := editWorkbookFixture(t, map[string]string{"Data": `<sheetData>` +
		`<row r="1"><c r="A1" t="inlineStr"><is><t>00123</t></is></c><c r="D1" t="inlineStr"><is><t>00123</t></is></c></row>` +
		`<row r="2"><c r="A2"/><c r="D2" t="inlineStr"><is><t>00055</t></is></c></row>` +
		`</sheetData><dataValidations count="1"><dataValidation type="list" sqref="A1:A2" prompt="选编号"><formula1>` + rangeFormula + `</formula1></dataValidation></dataValidations>`}, "", "")
	sourceHash := printScaleHash(t, path)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { a.Close() }()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	opened := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(a.currentWorkbook())}, 200))
	doc := opened.Document.Key
	address := func(row, col int) editdoc.Address { return editdoc.Address{Sheet: "Data", Row: row, Col: col} }
	read := func(rev uint64, ref string, wantRule bool, raw string) {
		t.Helper()
		cell := decodeResponse[struct {
			Rule    *editdoc.Rule
			Choices []string
			Value   editdoc.Value
		}](t, editHTTP(t, a, "read", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: ref}, 200))
		if len(cell.Choices) != 0 || wantRule != (cell.Rule != nil) || cell.Rule != nil && (!cell.Rule.Reference || cell.Rule.First != rangeFormula) || cell.Value.Raw != raw {
			t.Fatal(cell)
		}
	}
	choices := func(rev uint64, ref string, code int, want ...string) {
		t.Helper()
		w := editHTTP(t, a, "choices", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: ref}, code)
		if code == 200 {
			out := decodeResponse[struct {
				Revision uint64 `json:"revision,string"`
				Choices  []string
			}](t, w)
			if out.Revision != rev || !slices.Equal(out.Choices, want) {
				t.Fatal(out)
			}
		}
	}
	commit := func(rev uint64, action string, plan editBatchPreview, code int) {
		t.Helper()
		op := editdoc.Operation{Action: action, Revision: rev, Request: fmt.Sprintf("%032x", rev), Changes: plan.Changes, Rule: plan.Rule}
		editHTTP(t, a, "commit", editRequest{Document: doc, Proof: plan.Proof, Operation: op}, code)
	}
	change := func(row, col int, kind, raw string) editdoc.Change {
		return editdoc.Change{Address: address(row, col), Value: editdoc.Value{Kind: kind, Raw: raw}}
	}
	read(1, "A1", true, "00123")
	choices(1, "A1", 200, "00123", "00055")
	// The proposed input is valid only with the candidate change later in the
	// same batch. Conversely a candidate removed by the batch is unavailable.
	batch := []editdoc.Change{change(2, 1, "text", "00123"), change(1, 4, "text", "00077")}
	request := editRequest{Document: doc, Revision: 1, Mode: "changes", Sheet: "Data", Ref: "A1", End: "D2", Operation: editdoc.Operation{Changes: batch}}
	editHTTP(t, a, "preview", request, 422)
	commit(1, "change", editBatchPreview{Changes: batch}, 422)
	choices(1, "A2", 200, "00123", "00055")
	batch[0].Value.Raw = "00077"
	plan := decodeResponse[editBatchPreview](t, editHTTP(t, a, "preview", request, 200))
	commit(1, "change", plan, 200)
	choices(1, "A1", 409)
	choices(2, "A2", 200, "00077", "00055")
	read(2, "A2", true, "00077")
	for _, invalid := range []editdoc.Change{change(2, 4, "number", "55"), change(2, 4, "blank", ""), change(2, 4, "formula", "=1+1")} {
		commit(2, "change", editBatchPreview{Changes: []editdoc.Change{change(1, 2, "text", "拒绝整批"), invalid}}, 422)
	}
	read(2, "B1", false, "")
	commit(2, "undo", editBatchPreview{}, 200)
	choices(3, "A2", 200, "00123", "00055")
	read(3, "A2", true, "")
	commit(3, "redo", editBatchPreview{}, 200)
	choices(4, "A2", 200, "00077", "00055")
	newRule := editdoc.Rule{Area: editdoc.Area{Start: address(1, 2), EndRow: 2, EndCol: 2}, Type: "list", First: "=" + rangeFormula, Reference: true}
	for _, invalid := range []string{"D1:D2", "'Data'!$D$1:$D$2", "$C$1:$D$2", "$D$1:$D$257", "$Z$1", "$B$1:$B$2", "INDIRECT(\"$D$1\")"} {
		bad := newRule
		bad.First = invalid
		editHTTP(t, a, "preview", editRequest{Document: doc, Revision: 4, Mode: "rule", Rule: &bad}, 422)
	}
	plan = decodeResponse[editBatchPreview](t, editHTTP(t, a, "preview", editRequest{Document: doc, Revision: 4, Mode: "rule", Rule: &newRule}, 200))
	if plan.Rule.First != rangeFormula || !plan.Rule.Reference {
		t.Fatal(plan)
	}
	commit(4, "rule", plan, 200)
	artifact := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: doc, Revision: 5}, 200))
	a.Close()
	a, err = New()
	if err != nil {
		t.Fatal(err)
	}
	choices(5, "B2", 200, "00077", "00055")
	editHTTP(t, a, "open", editRequest{Document: doc, Artifact: artifact.Key}, 200)
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	index, err := wb.BuildIndex(meta, nil, t.Context().Done())
	if err != nil {
		t.Fatal(err)
	}
	rules, err := wb.SourceDataRules(t.Context(), meta, index, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := rules.Page(0, 2)
	if err != nil || !page.ScopeComplete || page.Total != 2 || page.Items[0].Prompt != "选编号" || page.Items[0].Formula1 != rangeFormula || page.Items[1].Formula1 != rangeFormula {
		t.Fatal(page, err)
	}
	doc = decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(wb)}, 200)).Document.Key
	choices(1, "B1", 200, "00077", "00055")
	if printScaleHash(t, path) != sourceHash {
		t.Fatal("source changed")
	}
}
