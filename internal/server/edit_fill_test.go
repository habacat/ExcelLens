package server

import (
	"fmt"
	"testing"

	"excellens/internal/editdoc"
)

func TestEditCopyCalendarAndFormulaFillAcrossDateSystems(t *testing.T) {
	for _, tc := range []struct {
		name, properties, start, firstDay, lastDay, lastDate, minimum, minimumSerial, maximum string
	}{
		{"1900", "", "59", "60", "61", "1900-03-01", "1900-02-29", "60", "1900-03-02"},
		{"1904", `<workbookPr date1904="true"/>`, "0", "1", "2", "1904-01-03", "1904-01-01", "0", "1904-01-04"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTestStorage(t)
			path := editWorkbookFixture(t, map[string]string{"Data": `<sheetData><row r="1"><c r="A1" s="1"><v>` + tc.start + `</v></c><c r="B1" s="2"><v>0.25</v></c><c r="C1"><f>$A1+$A$1</f><v>0</v></c>` +
				`<c r="D1" t="inlineStr"><is><t>1900-02-28</t></is></c><c r="E1" t="inlineStr"><is><t>01/02/2020</t></is></c><c r="F1"><f>XFD1</f><v>0</v></c><c r="G1"/></row>` +
				`<row r="3"><c r="D3"/></row></sheetData>`},
				`<numFmts count="1"><numFmt numFmtId="164" formatCode="yyyy-mm-dd"/></numFmts><cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="164" applyNumberFormat="1"/><xf numFmtId="21" applyNumberFormat="1"/></cellXfs>`, tc.properties)
			before := printScaleHash(t, path)
			a, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if err := a.OpenPath(path); err != nil {
				t.Fatal(err)
			}
			doc := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(a.currentWorkbook())}, 200)).Document.Key
			fill := func(rev uint64, mode, start, end, step string, code int) editBatchPreview {
				t.Helper()
				w := editHTTP(t, a, "preview", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: start, End: end, Mode: mode, Step: step}, code)
				if code == 200 {
					return decodeResponse[editBatchPreview](t, w)
				}
				return editBatchPreview{}
			}
			commit := func(rev uint64, action string, plan editBatchPreview) {
				t.Helper()
				op := editdoc.Operation{Request: fmt.Sprintf("%032x", rev), Revision: rev, Action: action, Changes: plan.Changes, Rule: plan.Rule}
				editHTTP(t, a, "commit", editRequest{Document: doc, Proof: plan.Proof, Operation: op}, 200)
			}
			fill(1, "date", "E1", "E3", "1", 422) // ambiguous text stays untouched
			fill(1, "time", "B1", "B3", "86400", 422)
			fill(1, "formula-fill", "F1", "G1", "", 422) // reference would move beyond XFD
			plan := fill(1, "date", "A1", "A3", "1", 200)
			if len(plan.Changes) != 2 || plan.Changes[0].Value.Raw != tc.firstDay || plan.Changes[1].Value.Raw != tc.lastDay || plan.Changes[1].Value.Style != "1" || plan.LastValue != tc.lastDate || plan.Proof == "" {
				t.Fatal(plan)
			}
			commit(1, "change", plan)
			fill(1, "time", "B1", "B3", "1800", 409)
			plan = fill(2, "time", "B1", "B3", "1800", 200)
			if plan.LastValue != "07:00:00" || plan.Changes[0].Value.Raw != "0.270833333333333" {
				t.Fatal(plan)
			}
			commit(2, "change", plan)
			plan = fill(3, "formula-fill", "C1", "C3", "", 200)
			if plan.LastValue != "=$A3+$A$1" || len(plan.Changes) != 2 {
				t.Fatal(plan)
			}
			commit(3, "change", plan)
			plan = fill(4, "date", "D1", "D3", "1", 200)
			if plan.LastValue != "1900-03-02" || plan.Changes[0].Value.Kind != "text" || plan.Changes[0].Value.Raw != "1900-03-01" {
				t.Fatal(plan)
			}
			commit(4, "change", plan)
			rule := editdoc.Rule{Area: editdoc.Area{Start: editdoc.Address{Sheet: "Data", Row: 1, Col: 1}, EndRow: 3, EndCol: 1}, Type: "date", Operator: "between", First: tc.minimum, Second: tc.maximum}
			plan = decodeResponse[editBatchPreview](t, editHTTP(t, a, "preview", editRequest{Document: doc, Revision: 5, Mode: "rule", Rule: &rule}, 200))
			if plan.Rule == nil || plan.Rule.First != tc.minimumSerial {
				t.Fatal(plan)
			}
			commit(5, "rule", plan)
			artifact := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: doc, Revision: 6}, 200))
			if artifact.FormulasPending != 4 {
				t.Fatal(artifact)
			}
			commit(6, "undo", editBatchPreview{}) // undo rule
			commit(7, "undo", editBatchPreview{}) // undo both date text cells
			for _, ref := range []string{"D2", "D3"} {
				cell := decodeResponse[struct{ Base, Value editdoc.Value }](t, editHTTP(t, a, "read", editRequest{Document: doc, Revision: 8, Sheet: "Data", Ref: ref}, 200))
				if cell.Base != cell.Value {
					t.Fatal(ref, cell)
				}
			}
			editHTTP(t, a, "open", editRequest{Document: doc, Artifact: artifact.Key, Book: sourceBookID(a.currentWorkbook())}, 200)
			wb := a.currentWorkbook()
			idx, err := wb.BuildIndex(wb.Sheets[0], nil, t.Context().Done())
			if err != nil {
				t.Fatal(err)
			}
			row, err := wb.ReadRowAll(idx, 2)
			if err != nil || len(row.Cells) != 4 || row.Cells[0].Raw != tc.lastDay || row.Cells[0].Display != tc.lastDate || row.Cells[1].Display != "07:00:00" || row.Cells[2].Formula != "=$A3+$A$1" || !row.Cells[2].CacheMissing || row.Cells[3].Type != "inlineStr" {
				t.Fatal(row, err)
			}
			if tc.name == "1900" {
				row, err := wb.ReadRowAll(idx, 1)
				if err != nil || row.Cells[0].Raw != "60" || row.Cells[0].Display != "1900-02-29" {
					t.Fatal(row, err)
				}
			}
			if printScaleHash(t, path) != before {
				t.Fatal("source changed")
			}
		})
	}
}
