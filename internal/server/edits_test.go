package server

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"excellens/internal/editdoc"
	"excellens/internal/editfile"
	"excellens/internal/workbook"
)

func editHTTP(t *testing.T, app *App, path string, req editRequest, status int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/edits/"+path, bytes.NewReader(data))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("X-ExcelLens-Token", app.token)
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s got %d, want %d: %s", path, w.Code, status, w.Body.String())
	}
	return w
}

func TestEditCopyLifecycleAndIndependentPublishedRevision(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "原始台账.csv")
	data := []byte("ID,说明,金额\r\n00123,原值,0.3\r\n00124,=公式,\r\n短行\r\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	newApp := func() *App {
		a, err := New()
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := newApp()
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	book := sourceBookID(a.currentWorkbook())
	opened := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: book}, 200))
	doc := opened.Document.Key
	if opened.Document.Revision != 1 || len(opened.Sheets) != 1 || opened.Sheets[0].Rows != 4 || opened.Sheets[0].Cols != 3 {
		t.Fatal(opened)
	}
	addr := func(row, col int) editdoc.Address {
		return editdoc.Address{Sheet: opened.Sheets[0].Name, Row: row, Col: col}
	}
	id := func(i int) string { return fmt.Sprintf("%032x", i) }
	op := editdoc.Operation{Revision: 1, Request: id(1), Action: "change", Changes: []editdoc.Change{{Address: addr(2, 1), Value: editdoc.Value{Kind: "text", Raw: "000001234567890123456789"}}, {Address: addr(4, 3), Value: editdoc.Value{Kind: "text", Raw: "missing"}}}}
	editHTTP(t, a, "commit", editRequest{Document: doc, Operation: op}, 422)
	op.Changes = op.Changes[:1]
	editHTTP(t, a, "commit", editRequest{Document: doc, Operation: op}, 200)
	read := func(app *App, rev uint64, want string) {
		t.Helper()
		cell := decodeResponse[struct{ Base, Value editdoc.Value }](t, editHTTP(t, app, "read", editRequest{Document: doc, Revision: rev, Sheet: addr(1, 1).Sheet, Ref: "A2"}, 200))
		if cell.Base.Raw != "00123" || cell.Value.Raw != want || cell.Value.Kind != "text" {
			t.Fatal(cell)
		}
	}
	read(a, 2, op.Changes[0].Value.Raw)
	grid := editRequest{Book: book, Document: doc, Revision: 2}
	view := decodeResponse[map[string]map[string]*workbook.CellData](t, editHTTP(t, a, "grid", grid, 200))
	cell := view[addr(2, 1).Sheet]["A2"]
	if len(view) != 1 || cell == nil || cell.Type != "inlineStr" || cell.Raw != op.Changes[0].Value.Raw || cell.Display != cell.Raw {
		t.Fatalf("draft grid lost the text identifier: %+v", cell)
	}
	req := editRequest{Document: doc, Revision: 2, Options: editfile.TextOptions{Encoding: "source", Protect: true}}
	output := decodeResponse[editArtifact](t, editHTTP(t, a, "save", req, 200))
	if output.Revision != 2 || output.Protected != 1 || output.DownloadURL == "" {
		t.Fatal(output)
	}
	editHTTP(t, a, "commit", editRequest{Document: doc, Operation: editdoc.Operation{Revision: 1, Request: id(2), Action: "undo"}}, 409)
	editHTTP(t, a, "commit", editRequest{Document: doc, Operation: editdoc.Operation{Revision: 2, Request: id(2), Action: "undo"}}, 200)
	read(a, 3, "00123")
	editHTTP(t, a, "grid", grid, 409)
	grid.Revision, grid.Cells = 3, []editdoc.Address{addr(2, 1)}
	view = decodeResponse[map[string]map[string]*workbook.CellData](t, editHTTP(t, a, "grid", grid, 200))
	if cleared, exists := view[addr(2, 1).Sheet]["A2"]; !exists || cleared != nil {
		t.Fatal("delta did not restore source", view)
	}
	a.Close()
	b := newApp()
	defer b.Close()
	restored := decodeResponse[editState](t, editHTTP(t, b, "state", editRequest{Document: doc}, 200))
	if restored.Document.Revision != 3 || !restored.Document.Redo || len(restored.Artifacts) != 1 || restored.Artifacts[0].SHA256 != output.SHA256 {
		t.Fatal(restored)
	}
	read(b, 3, "00123")
	editHTTP(t, b, "grid", grid, 409)
	editHTTP(t, b, "base", editRequest{Document: doc}, 200)
	editHTTP(t, b, "grid", grid, 200)
	editHTTP(t, b, "commit", editRequest{Document: doc, Operation: op}, 200) // repeat committed request after restart
	editHTTP(t, b, "commit", editRequest{Document: doc, Operation: editdoc.Operation{Revision: 3, Request: id(3), Action: "redo"}}, 200)
	read(b, 4, op.Changes[0].Value.Raw)
	retry := decodeResponse[editArtifact](t, editHTTP(t, b, "save", req, 200))
	if retry.Key != output.Key || retry.SHA256 != output.SHA256 || retry.DownloadURL == output.DownloadURL {
		t.Fatal(retry)
	}
	r := httptest.NewRequest("GET", retry.DownloadURL, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	parser := csv.NewReader(w.Body)
	parser.FieldsPerRecord = -1
	rows, err := parser.ReadAll()
	if w.Code != 200 || err != nil || len(rows) != 4 || rows[1][0] != op.Changes[0].Value.Raw || rows[2][1] != "'=公式" || len(rows[3]) != 1 {
		t.Fatal(w.Code, rows, err)
	}
	r = httptest.NewRequest("GET", output.DownloadURL, nil)
	r.RemoteAddr = "127.0.0.1:12345"
	w = httptest.NewRecorder()
	b.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("expired app ticket was accepted", w.Code)
	}
	editHTTP(t, b, "open", editRequest{Document: doc, Artifact: retry.Key, Book: book}, 200)
	if c := b.currentWorkbook(); c.Path == path || sourceBookID(c) == book || c.DisplayName != output.Name {
		t.Fatal(c.Path, c.DisplayName)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(data, got) {
		t.Fatal("source changed", err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "00123", "00999", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	editHTTP(t, b, "read", editRequest{Document: doc, Revision: 4, Sheet: addr(1, 1).Sheet, Ref: "A2"}, 409)
	editHTTP(t, b, "save", req, 200) // the already verified r2 artifact remains recoverable
	for _, history := range []editdoc.Operation{
		{Revision: 2, Request: id(2), Action: "undo"},
		{Revision: 3, Request: id(3), Action: "redo"},
	} {
		replay := decodeResponse[struct {
			Committed uint64 `json:"committed,string"`
		}](t, editHTTP(t, b, "commit", editRequest{Document: doc, Operation: history}, 200))
		if replay.Committed != history.Revision+1 {
			t.Fatal("history retry returned the wrong revision", replay)
		}
	}
	editHTTP(t, b, "commit", editRequest{Document: doc, Operation: editdoc.Operation{Revision: 4, Request: id(3), Action: "undo"}}, 409)
	editHTTP(t, b, "commit", editRequest{Document: doc, Operation: editdoc.Operation{Revision: 4, Request: id(4), Action: "undo"}}, 409)
}

func editWorkbookFixture(t *testing.T, sheets map[string]string, styles, properties string) string {
	t.Helper()
	const main = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	const rel = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	const pkg = "http://schemas.openxmlformats.org/package/2006/relationships"
	const mime = "application/vnd.openxmlformats-officedocument.spreadsheetml."
	parts := map[string]string{
		"_rels/.rels": `<Relationships xmlns="` + pkg + `"><Relationship Id="rId1" Type="` + rel + `/officeDocument" Target="xl/workbook.xml"/></Relationships>`,
	}
	book := `<workbook xmlns="` + main + `" xmlns:r="` + rel + `">` + properties + `<sheets>`
	links := `<Relationships xmlns="` + pkg + `">`
	types := `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Override PartName="/xl/workbook.xml" ContentType="` + mime + `sheet.main+xml"/>`
	var i int
	for _, name := range slices.Sorted(maps.Keys(sheets)) {
		i++
		part := fmt.Sprintf("worksheets/sheet%d.xml", i)
		book += fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, name, i, i)
		links += fmt.Sprintf(`<Relationship Id="rId%d" Type="%s/worksheet" Target="%s"/>`, i, rel, part)
		types += `<Override PartName="/xl/` + part + `" ContentType="` + mime + `worksheet+xml"/>`
		parts["xl/"+part] = `<worksheet xmlns="` + main + `">` + sheets[name] + `</worksheet>`
	}
	if styles != "" {
		links += `<Relationship Id="sty" Type="` + rel + `/styles" Target="styles.xml"/>`
		types += `<Override PartName="/xl/styles.xml" ContentType="` + mime + `styles+xml"/>`
		parts["xl/styles.xml"] = `<styleSheet xmlns="` + main + `">` + styles + `</styleSheet>`
	}
	parts["xl/workbook.xml"] = book + `</sheets></workbook>`
	parts["xl/_rels/workbook.xml.rels"] = links + `</Relationships>`
	parts["[Content_Types].xml"] = types + `</Types>`
	path := filepath.Join(t.TempDir(), "original.xlsx")
	writeZipArchive(t, path, parts)
	return path
}

func TestEditCopyFormulaStateAcrossUndoAndPublicReads(t *testing.T) {
	isolateTestStorage(t)
	path := editWorkbookFixture(t, map[string]string{
		"Data": `<sheetData><row r="1"><c r="A1"><v>4</v></c><c r="B1"><f>A1*2</f><v>8</v></c><c r="C1"/></row></sheetData>`,
		"Summary": `<sheetData><row r="1"><c r="A1"><f>Data!B1+1</f><v>9</v></c>` +
			`<c r="B1" t="str"><f>""</f><v/></c><c r="C1" t="n"><f>1+1</f><v/></c></row></sheetData>`,
	}, "", "")
	before := printScaleHash(t, path)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	opened := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(a.currentWorkbook())}, 200))
	doc := opened.Document.Key
	read := func(rev uint64, ref, state, formula, raw string, hasCache bool) {
		t.Helper()
		cell := decodeResponse[struct {
			Value            editdoc.Value
			FormulaState     string
			SavedResult      *editdoc.Value
			CalculatedResult *editdoc.Value
		}](t, editHTTP(t, a, "read", editRequest{Document: doc, Revision: rev, Sheet: "Summary", Ref: ref}, 200))
		result := cell.SavedResult
		if state == "calculated" {
			if result != nil {
				t.Fatal("a revision result was presented as a saved cache")
			}
			result = cell.CalculatedResult
		}
		if cell.Value != (editdoc.Value{Kind: "formula", Raw: formula, Style: "0"}) || cell.FormulaState != state || (result != nil) != hasCache || hasCache && result.Raw != raw {
			t.Fatalf("r%d %s: %+v", rev, ref, cell)
		}
	}
	read(1, "A1", "saved", "=Data!B1+1", "9", true)
	read(1, "B1", "saved", `=""`, "", true)
	read(1, "C1", "calculated", "=1+1", "2", true)
	changes := []editdoc.Change{
		{Address: editdoc.Address{Sheet: "Data", Row: 1, Col: 1}, Value: editdoc.Value{Kind: "number", Raw: "7.25"}},
		{Address: editdoc.Address{Sheet: "Data", Row: 1, Col: 3}, Value: editdoc.Value{Kind: "formula", Raw: "=SUM([Other]Data!A1)"}},
	}
	commit := func(rev uint64, action string, changes []editdoc.Change, status int) {
		t.Helper()
		op := editdoc.Operation{Revision: rev, Request: fmt.Sprintf("%032x", rev), Action: action, Changes: changes}
		editHTTP(t, a, "commit", editRequest{Document: doc, Operation: op}, status)
	}
	commit(1, "change", changes, 422)
	read(1, "A1", "saved", "=Data!B1+1", "9", true)
	changes[1].Value.Raw = "=A1+$B$1"
	commit(1, "change", changes, 200)
	read(2, "A1", "calculated", "=Data!B1+1", "15.5", true)
	read(2, "B1", "saved", `=""`, "", true)
	generated := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: doc, Revision: 2}, 200))
	if generated.FormulasPending != 5 {
		t.Fatal(generated)
	}
	commit(2, "undo", nil, 200)
	read(3, "A1", "saved", "=Data!B1+1", "9", true)
	read(3, "C1", "calculated", "=1+1", "2", true)
	pristine := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: doc, Revision: 3}, 200))
	if pristine.FormulasPending != 1 || pristine.SHA256 == generated.SHA256 {
		t.Fatal(pristine)
	}
	commit(3, "redo", nil, 200)
	read(4, "A1", "calculated", "=Data!B1+1", "15.5", true)
	editHTTP(t, a, "open", editRequest{Document: doc, Artifact: generated.Key, Book: sourceBookID(a.currentWorkbook())}, 200)
	wb := a.currentWorkbook()
	for _, sheet := range wb.Sheets {
		if _, err := wb.BuildIndex(sheet, nil, t.Context().Done()); err != nil {
			t.Fatal(err)
		}
	}
	cell := cellExplanationOK(t, a, cellExplanationSource(t, a, "Summary", "A1", rowViewRequest{}))
	if cell.FormulaState != "missing" || cell.Numeric.Reason != "missing-result" || cell.Cell.Formula != "=Data!B1+1" || cell.Cell.Raw != "" {
		t.Fatal(cell)
	}
	quick := quickOK(t, a, quickRequest(t, a, "Data", rowViewRequest{}, "A1", "C1"))
	if len(quick.Columns) != 3 || quick.Columns[0].Counts.Number != 1 || quick.Columns[0].Summary.Sum != "7.25" || quick.Columns[1].Counts.Missing != 1 || quick.Columns[2].Counts.Missing != 1 {
		t.Fatal(quick)
	}
	for _, expression := range []string{"=A1+1", "=SUM(A1:C1)"} {
		response := rowViewCall(t, a, "POST", "/api/calculator/formula", fmt.Sprintf(`{"sheet":"Summary","expression":%q,"workbookKey":%q}`, expression, wb.Fingerprint))
		if response.Code != 422 || !strings.Contains(response.Body.String(), "缺少") {
			t.Fatal(expression, response.Code, response.Body.String())
		}
	}
	if printScaleHash(t, path) != before {
		t.Fatal("source changed")
	}
}

func TestEditCopyTypedClipboardAndStyleOnlyRoundTrip(t *testing.T) {
	isolateTestStorage(t)
	path := editWorkbookFixture(t, map[string]string{"Data": `<cols><col min="4" max="4" style="1" width="18"/></cols>` +
		`<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>00123</t></is></c><c r="B1"><v>0.30</v></c><c r="C1"><f>$B1+B$1</f><v>0.60</v></c></row>` +
		`<row r="2"><c r="A2" s="2"/><c r="B2" t="inlineStr"><is><t>旧值</t></is></c><c r="C2"/><c r="D2"/></row>` +
		`<row r="3"><c r="B3"/><c r="C3" s="2"><f>B3+1</f><v>1</v></c></row></sheetData>`},
		`<cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="4"/><xf numFmtId="2"/></cellXfs>`, "")
	before := printScaleHash(t, path)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { a.Close() }()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	state := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(a.currentWorkbook())}, 200))
	doc := state.Document.Key
	copyRange := func(rev uint64, start, end string) editClipboard {
		t.Helper()
		return decodeResponse[editClipboard](t, editHTTP(t, a, "copy", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: start, End: end}, 200))
	}
	preview := func(rev uint64, ref, mode string, clip editClipboard, status int) editBatchPreview {
		t.Helper()
		r := editHTTP(t, a, "preview", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: ref, Mode: mode, Clipboard: &clip}, status)
		if status == 200 {
			return decodeResponse[editBatchPreview](t, r)
		}
		return editBatchPreview{}
	}
	commit := func(rev uint64, action string, plan editBatchPreview, status int) {
		t.Helper()
		editHTTP(t, a, "commit", editRequest{Document: doc, Proof: plan.Proof, Operation: editdoc.Operation{Request: fmt.Sprintf("%032x", rev), Revision: rev, Action: action, Changes: plan.Changes}}, status)
	}
	read := func(rev uint64, ref string) editdoc.Value {
		t.Helper()
		return decodeResponse[struct{ Value editdoc.Value }](t, editHTTP(t, a, "read", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: ref}, 200)).Value
	}
	values := copyRange(1, "A1", "C1")
	formula := copyRange(1, "C1", "")
	styles := copyRange(1, "D1", "D2")
	if styles.Cells[0].Style != "1" || styles.Cells[1].Style != "0" || styles.Proof == "" {
		t.Fatal(styles)
	}
	plan := preview(1, "A2", "values", values, 200)
	if len(plan.Changes) != 3 || plan.Changes[0].Value.Kind != "text" || plan.Changes[0].Value.Raw != "00123" || plan.Changes[2].Value.Raw != "0.60" {
		t.Fatal(plan)
	}
	commit(1, "change", plan, 200)
	plan = preview(2, "C2", "formulas", formula, 200)
	if len(plan.Changes) != 1 || plan.Changes[0].Value.Raw != "=$B2+B$1" {
		t.Fatal(plan)
	}
	commit(2, "change", plan, 200)
	pasted := preview(3, "B2", "values", copyRange(3, "C2", ""), 200)
	if len(pasted.Changes) != 1 || pasted.Changes[0].Value != (editdoc.Value{Kind: "number", Raw: "0.6"}) {
		t.Fatal(pasted)
	}
	proof := styles.Proof
	styles.Proof = "forged"
	preview(3, "C2", "styles", styles, 422)
	styles.Proof = proof
	plan = preview(3, "C2", "styles", styles, 200)
	plan.Changes[0].Value.Style = "2"
	commit(3, "change", plan, 422)
	plan.Changes[0].Value.Style = "1"
	commit(3, "change", plan, 200)
	if got := read(4, "C2"); got != (editdoc.Value{Kind: "formula", Raw: "=$B2+B$1", Style: "1"}) {
		t.Fatal(got)
	}
	artifact := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: doc, Revision: 4}, 200))
	if artifact.FormulasPending != 3 || artifact.ChangedParts != 2 { // worksheet and workbook calc properties
		t.Fatal(artifact)
	}
	a.Close()
	a, err = New()
	if err != nil {
		t.Fatal(err)
	}
	commit(4, "undo", editBatchPreview{}, 200)
	if got := read(5, "C2"); got.Style != "0" || got.Raw != "=$B2+B$1" {
		t.Fatal(got)
	}
	commit(5, "redo", editBatchPreview{}, 200)
	editHTTP(t, a, "open", editRequest{Document: doc, Artifact: artifact.Key}, 200)
	wb := a.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, t.Context().Done())
	if err != nil {
		t.Fatal(err)
	}
	row, err := wb.ReadRowAll(idx, 1)
	if err != nil || len(row.Cells) < 3 || row.Cells[0].Raw != "00123" || row.Cells[0].Type != "inlineStr" || row.Cells[0].Style != 2 || row.Cells[2].Style != 1 || !row.Cells[2].CacheMissing {
		t.Fatal(row, err)
	}
	row, err = wb.ReadRowAll(idx, 2)
	if err != nil || len(row.Cells) < 2 || row.Cells[1].Style != 0 || row.Cells[1].Formula != "=B3+1" || !row.Cells[1].CacheMissing {
		t.Fatal(row, err)
	}
	if printScaleHash(t, path) != before {
		t.Fatal("source changed")
	}
}

func TestEditCopyRuleRevisionAndDropdownRoundTrip(t *testing.T) {
	isolateTestStorage(t)
	const input = `<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>01</t></is></c><c r="B1"><v>0.15</v></c><c r="C1"><v>3</v></c></row><row r="2"><c r="A2"/><c r="B2"/><c r="C2"/></row></sheetData>`
	const ruleXML = `<dataValidations count="2"><dataValidation type="list" sqref="A1:A2" allowBlank="1" prompt="请选择"><formula1>"01,02"</formula1></dataValidation>` +
		`<dataValidation type="whole" operator="greaterThan" sqref="C1:C2"><formula1>0</formula1></dataValidation></dataValidations>`
	path := editWorkbookFixture(t, map[string]string{"Data": input + ruleXML + `<pageMargins left="0.7" right="0.7" top="0.7" bottom="0.7" header="0.3" footer="0.3"/>`}, "", "")
	before := printScaleHash(t, path)
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
	addr := func(row, col int) editdoc.Address { return editdoc.Address{Sheet: "Data", Row: row, Col: col} }
	read := func(rev uint64, ref string) (editdoc.Value, []string) {
		t.Helper()
		out := decodeResponse[struct {
			Value   editdoc.Value
			Choices []string
		}](t, editHTTP(t, a, "read", editRequest{Document: doc, Revision: rev, Sheet: "Data", Ref: ref}, 200))
		return out.Value, out.Choices
	}
	commit := func(rev uint64, action string, plan editBatchPreview, status int) {
		t.Helper()
		op := editdoc.Operation{Request: fmt.Sprintf("%032x", rev), Revision: rev, Action: action, Changes: plan.Changes, Rule: plan.Rule}
		editHTTP(t, a, "commit", editRequest{Document: doc, Proof: plan.Proof, Operation: op}, status)
	}
	previewRule := func(rev uint64, r editdoc.Rule, status int) editBatchPreview {
		t.Helper()
		w := editHTTP(t, a, "preview", editRequest{Document: doc, Revision: rev, Mode: "rule", Rule: &r}, status)
		if status == 200 {
			return decodeResponse[editBatchPreview](t, w)
		}
		return editBatchPreview{}
	}
	change := func(row, col int, kind, raw string) editdoc.Change {
		return editdoc.Change{Address: addr(row, col), Value: editdoc.Value{Kind: kind, Raw: raw}}
	}
	if original, choices := read(1, "A1"); original.Raw != "01" || !slices.Equal(choices, []string{"01", "02"}) {
		t.Fatal(original, choices)
	}
	commit(1, "change", editBatchPreview{Changes: []editdoc.Change{change(1, 2, "number", "0.2"), change(2, 1, "number", "2")}}, 422)
	if original, _ := read(1, "B1"); original.Raw != "0.15" {
		t.Fatal(original)
	}
	commit(1, "change", editBatchPreview{Changes: []editdoc.Change{change(2, 1, "text", "02")}}, 200)
	modified := editdoc.Rule{Area: editdoc.Area{Start: addr(1, 1), EndRow: 2, EndCol: 1}, Type: "list", First: "02\n03", AllowBlank: true}
	overlap := modified
	overlap.Start.Row = 2
	previewRule(2, overlap, 422)
	commit(2, "rule", previewRule(2, modified, 200), 200)
	if existing, choices := read(3, "A1"); existing.Raw != "01" || !slices.Equal(choices, []string{"02", "03"}) {
		t.Fatal(existing, choices)
	}
	decimal := editdoc.Rule{Area: editdoc.Area{Start: addr(1, 2), EndRow: 2, EndCol: 2}, Type: "decimal", Operator: "between", First: "0.10", Second: "0.30"}
	commit(3, "rule", previewRule(3, decimal, 200), 200)
	changes := []editdoc.Change{change(1, 2, "number", "0.10"), change(2, 2, "text", "0.30")}
	request := editRequest{Document: doc, Revision: 4, Mode: "changes", Sheet: "Data", Ref: "B1", End: "B2", Operation: editdoc.Operation{Changes: changes}}
	editHTTP(t, a, "preview", request, 422)
	commit(4, "change", editBatchPreview{Changes: changes}, 422)
	changes[1].Value.Kind = "number"
	plan := decodeResponse[editBatchPreview](t, editHTTP(t, a, "preview", request, 200))
	commit(4, "change", plan, 200)
	artifact := decodeResponse[editArtifact](t, editHTTP(t, a, "save", editRequest{Document: doc, Revision: 5}, 200))
	a.Close()
	a, err = New()
	if err != nil {
		t.Fatal(err)
	}
	commit(5, "undo", editBatchPreview{}, 200)
	commit(6, "undo", editBatchPreview{}, 200)
	commit(7, "undo", editBatchPreview{}, 200)
	if _, choices := read(8, "A2"); !slices.Equal(choices, []string{"01", "02"}) {
		t.Fatal(choices)
	}
	editHTTP(t, a, "open", editRequest{Document: doc, Artifact: artifact.Key}, 200)
	wb := a.currentWorkbook()
	sheet := wb.Sheets[0]
	index, err := wb.BuildIndex(sheet, nil, t.Context().Done())
	if err != nil {
		t.Fatal(err)
	}
	rules, err := wb.SourceDataRules(t.Context(), sheet, index, nil)
	if err != nil {
		t.Fatal(err)
	}
	page, err := rules.Page(0, 3)
	if err != nil || !page.ScopeComplete || page.Total != 3 || !slices.Equal(page.Items[0].InlineList, []string{"02", "03"}) || page.Items[0].Prompt != "请选择" || page.Items[1].Type != "whole" || page.Items[2].Formula2 != "0.30" {
		t.Fatal(page, err)
	}
	if printScaleHash(t, path) != before {
		t.Fatal("source changed")
	}
}
