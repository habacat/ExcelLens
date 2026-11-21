package server

import (
	"excellens/internal/editdoc"
	"strings"
	"testing"
)

func TestWhatIfSolvesAndSweepsWithoutWritingRevisionOrSource(t *testing.T) {
	isolateTestStorage(t)
	bad := editWorkbookFixture(t, map[string]string{"模型": `<sheetData><row r="1"><c r="A1"><f>_xlfn.UNKNOWN(1)</f><v>42</v></c></row></sheetData>`}, "", "")
	invalid, _ := New()
	defer invalid.Close()
	if err := invalid.OpenPath(bad); err != nil {
		t.Fatal(err)
	}
	editHTTP(t, invalid, "start", editRequest{Book: sourceBookID(invalid.currentWorkbook())}, 422)
	path := editWorkbookFixture(t, map[string]string{"模型": `<sheetData><row r="1"><c r="A1"><v>2</v></c><c r="B1"><v>3</v></c><c r="C1"><f>A1*B1</f><v>99</v></c><c r="D1"><f>D1+A1</f><v>100</v></c><c r="E1"><f>A1/0</f><v>42</v></c><c r="F1"><f>IF(A1&gt;0,10,-10)</f><v>10</v></c></row></sheetData>`}, "", "")
	before := printScaleHash(t, path)
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	book := sourceBookID(a.currentWorkbook())
	state := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: book}, 200))
	at := func(col int) editdoc.Address { return editdoc.Address{Sheet: "模型", Col: col, Row: 1} }
	req := editRequest{Book: book, Document: state.Document.Key, Revision: state.Document.Revision, Trial: &whatIfSpec{Mode: "seek", Input: at(1), Target: at(3), Lower: "0", Upper: "20", Goal: "30", Tolerance: "0.00001"}}
	run := func() whatIfResult { return decodeResponse[whatIfResult](t, editHTTP(t, a, "what-if", req, 200)) }
	out := run()
	if out.Status != "达到目标" || out.Points[0].Input != "10" || out.Points[0].Result != "30" || out.Residual != "0" {
		t.Fatal(out)
	}
	req.Trial.Goal = "1000"
	out = run()
	if out.Status != "区间两端未夹住目标" {
		t.Fatal(out)
	}
	req.Trial.Mode = "table"
	req.Trial.Values = []string{"1", "3"}
	req.Trial.Second = at(2)
	req.Trial.SecondValues = []string{"2", "5"}
	out = run()
	if len(out.Points) != 4 || out.Points[0].Result != "2" || out.Points[3].Result != "15" {
		t.Fatal(out)
	}
	req.Trial.Target = at(4)
	out = run()
	if !strings.Contains(out.Points[0].Error, "循环") {
		t.Fatal(out)
	}
	req.Trial.Target = at(5)
	out = run()
	if out.Points[0].Error == "" || out.Points[0].Result == "42" {
		t.Fatal(out)
	}
	after := decodeResponse[editState](t, editHTTP(t, a, "state", editRequest{Document: req.Document}, 200))
	if after.Document.Revision != req.Revision || printScaleHash(t, path) != before {
		t.Fatal("trial changed the workbook or draft")
	}
}
