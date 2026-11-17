package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestSelectionQuickActualTextHTMLAndBIFFAdapters(t *testing.T) {
	for _, tc := range []struct{ name, format, body string }{
		{"source.csv", "csv", "编号,金额,右侧\n001,9007199254740993.31,9007199254740993.30\n002,-0.01,0.01\n"},
		{"source.tsv", "tsv", "编号\t金额\t右侧\n001\t9007199254740993.31\t9007199254740993.30\n002\t-0.01\t0.01\n"},
		{"source.txt", "txt", "编号\t金额\t右侧\n001\t9007199254740993.31\t9007199254740993.30\n002\t-0.01\t0.01\n"},
		{"source.xls", "xls-html", "<html><table><tr><td>编号</td><td>金额</td><td>右侧</td></tr><tr><td>001</td><td>9007199254740993.31</td><td>9007199254740993.30</td></tr><tr><td>002</td><td>-0.01</td><td>0.01</td></tr></table></html>"},
		{"native.xls", "xls", ""},
	} {
		t.Run(tc.format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.name)
			if tc.body == "" {
				printBIFFSource(t, file, 4)
			} else if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			if a.currentWorkbook().Format != tc.format {
				t.Fatal("wrong adapter", a.currentWorkbook().Format)
			}
			req := quickRequest(t, a, meta.Name, rowViewRequest{}, "A1", "C3")
			req.SkipFirstRow, req.Minuend, req.Subtrahend = true, 3, 2
			if tc.format == "xls" {
				req.End, req.Minuend, req.Subtrahend = "B4", 0, 0
			}
			r := quickOK(t, a, req)
			if r.Columns[0].Counts.Other != int64(r.Rows) || r.Columns[0].Counts.Number != 0 {
				t.Fatal("leading-zero identifiers converted", r.Columns[0])
			}
			if tc.format != "xls" {
				if r.Columns[1].Summary.Sum != "9007199254740993.3" || r.Difference.Summary.Sum != "0.01" || r.Difference.Paired != 2 {
					t.Fatal("text amounts rounded or comparison changed", r)
				}
			} else if r.Columns[1].Summary.Sum != "10.5" {
				t.Fatal("native numeric values changed", r.Columns[1])
			}
			if printScaleHash(t, file) != before {
				t.Fatal("source changed")
			}
		})
	}
}

func TestSelectionQuickSharedTextBudgetAndQueueCancellation(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "shared.xlsx")
	var rows strings.Builder
	for row := 1; row <= 257; row++ {
		fmt.Fprintf(&rows, `<row r="%d"><c r="A%d" t="s"><v>0</v></c></row>`, row, row)
	}
	writeZipArchive(t, file, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="s1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="s1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>` + strings.Repeat("x", 128<<10) + `</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData>` + rows.String() + `</sheetData></worksheet>`,
	})
	a := prepareFeatureApp(t, file)
	defer a.Close()
	req := quickRequest(t, a, "First", rowViewRequest{}, "A1", "A257")
	if out := quickCall(t, a, req); out.Code != 413 || strings.Contains(out.Body.String(), `"columns"`) {
		t.Fatal("shared-string text overrun published a subset", out.Code, out.Body.String())
	}
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(a.querySem); i++ {
			<-a.querySem
		}
	}()
	data, _ := json.Marshal(req)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest("POST", "/api/selection/quick", bytes.NewReader(data)).WithContext(ctx)
	request.RemoteAddr = "127.0.0.1:777"
	request.Header.Set("X-ExcelLens-Token", a.token)
	out := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { a.Handler().ServeHTTP(out, request); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled request remained queued")
	}
	if out.Body.Len() != 0 || len(a.querySem) != cap(a.querySem) {
		t.Fatal("cancelled request acquired a slot or published a result", out.Body.String())
	}
}

func TestSelectionQuickActualMillionCSVBoundedFullSelection(t *testing.T) {
	if os.Getenv("EXCELLENS_QUICK_SCALE") != "1" {
		t.Skip("set EXCELLENS_QUICK_SCALE=1 for an actual million-row CSV")
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "million.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	for row := 1; row <= 1_000_000; row++ {
		if _, err := fmt.Fprintf(w, "%012d,9007199254740993.31,%d\n", row, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	before := printScaleHash(t, file)
	a, meta := recordOpen(t, file)
	wb := a.currentWorkbook()
	idx := wb.GetCachedIndex(meta.Path)
	if idx.RowCount() != 1_000_000 || idx.MaxRow != 1_000_000 {
		t.Fatal("not an actual million-row fixture", idx.RowCount(), idx.MaxRow)
	}
	req := quickRequest(t, a, meta.Name, rowViewRequest{}, "B800001", "C1000000")
	req.Minuend, req.Subtrahend = 2, 3
	started := time.Now()
	r := quickOK(t, a, req)
	elapsed := time.Since(started)
	amount, _ := new(big.Rat).SetString("9007199254740993.31")
	want := new(big.Rat).Mul(amount, big.NewRat(200000, 1))
	got, _ := new(big.Rat).SetString(r.Columns[0].Summary.Sum)
	rowSum := int64(800001+1000000) * 200000 / 2
	difference := new(big.Rat).Sub(want, new(big.Rat).SetInt64(rowSum))
	delta, _ := new(big.Rat).SetString(r.Difference.Summary.Sum)
	if r.Rows != 200000 || r.Difference.Paired != 200000 || len(r.Columns[0].Segments) != 60 || got.Cmp(want) != 0 || delta.Cmp(difference) != 0 {
		t.Fatal("full selection incomplete", r.Rows, got, delta)
	}
	for _, segment := range r.Columns[0].Segments {
		expected := new(big.Rat).Mul(amount, big.NewRat(int64(segment.Last-segment.First+1), 1))
		got, _ := new(big.Rat).SetString(segment.Summary.Sum)
		if got.Cmp(expected) != 0 || segment.FirstRow != 800000+segment.First || segment.LastRow != 800000+segment.Last {
			t.Fatal("incorrect complete segment", segment)
		}
	}
	// Fault an unselected cache row, proving it is not parsed. Source file stays
	// unchanged; selected rows must still succeed, the corrupt row must fail.
	bad := idx.RowAt(idx.FindRowOffset(333333))
	cache, err := os.OpenFile(idx.XMLPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cache.WriteAt(bytes.Repeat([]byte{0xff}, min(16, int(bad.End-bad.Start))), bad.Start)
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{333333}, 1, 3, workbook.RowReadOptions{}); err == nil {
		t.Fatal("corruption negative control ineffective")
	}
	req.Start, req.End = "B999991", "C1000000"
	if small := quickOK(t, a, req); small.Rows != 10 || small.Columns[0].Summary.Count != 10 {
		t.Fatal("unrelated cache was decoded", small)
	}
	req.Start, req.End, req.Minuend, req.Subtrahend = "A1", "A400001", 0, 0
	if out := quickCall(t, a, req); out.Code != 400 {
		t.Fatal("over-limit selection not rejected", out.Code)
	}
	if printScaleHash(t, file) != before {
		t.Fatal("source changed")
	}
	data, _ := json.Marshal(r)
	evidence := map[string]any{"result": "PASS", "physicalRows": idx.RowCount(), "selectedRows": r.Rows, "selectedCells": r.Rows * len(r.Columns), "segments": len(r.Columns[0].Segments), "sum": r.Columns[0].Summary.Sum,
		"difference": r.Difference.Summary.Sum, "resultBytes": len(data), "elapsedMs": elapsed.Milliseconds(), "unselectedCacheCorruptionControl": true, "sourceSHA256": before, "sourceUnchanged": true, "desktopManualAcceptance": false}
	if len(data) > formats.SelectionQuickResultBytes {
		t.Fatal("result exceeds operation capacity")
	}
	if output := os.Getenv("EXCELLENS_QUICK_SCALE_REPORT"); output != "" {
		encoded, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(output, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("%+v", evidence)
}
