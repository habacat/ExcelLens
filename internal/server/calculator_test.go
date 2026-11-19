package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/archives"
	"excellens/internal/calculator"
	"excellens/internal/workbook"
)

func calculatorTestWorkbook(t *testing.T) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "money.csv")
	data := []byte("金额,备注\n0.1,甲\n0.2,乙\n3,丙\n0000123,编号\n\"(1,234.50)\",负数\n13%,比例\n1/2,文本日期\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	return a, wb.Fingerprint
}
func TestCalculatorCaptureExactValuesSourceIdentityAndSelection(t *testing.T) {
	a, key := calculatorTestWorkbook(t)
	original, _ := os.ReadFile(a.currentWorkbook().Path)
	defer func() {
		after, _ := os.ReadFile(a.currentWorkbook().Path)
		if !bytes.Equal(original, after) {
			t.Error("source was modified")
		}
	}()
	capture := func(req calculatorCaptureRequest) calculatorOperand {
		body, _ := json.Marshal(req)
		return decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", string(body)))
	}
	for ref, want := range map[string]string{"A2": "1/10", "A3": "1/5", "A6": "-2469/2", "A7": "13/100"} {
		got := capture(calculatorCaptureRequest{Kind: "cell", WorkbookKey: key, Sheet: "数据", Ref: ref})
		if got.Value != want || got.Ref != ref || got.WorkbookKey != key || got.File != "money.csv" {
			t.Fatal(got)
		}
	}
	for _, ref := range []string{"A5", "A8", "B2", "A100"} {
		body, _ := json.Marshal(calculatorCaptureRequest{Kind: "cell", WorkbookKey: key, Sheet: "数据", Ref: ref})
		res := rawArchiveCall(a, "POST", "/api/calculator/capture", string(body))
		if res.Code != 422 {
			t.Fatal("non-amount became zero/numeric", ref, res.Code, res.Body.String())
		}
	}
	sum := capture(calculatorCaptureRequest{Kind: "sum", WorkbookKey: key, Sheet: "数据", Ranges: []selectionStatRect{{Start: "A2", End: "A3"}, {Start: "A2", End: "A2"}}})
	if sum.Value != "3/10" || sum.Total != 2 || sum.Numeric != 2 {
		t.Fatal("overlap counted twice", sum)
	}
	manual := capture(calculatorCaptureRequest{Kind: "manual", Raw: "(1,234.500001)"})
	if manual.Value != "-1234500001/1000000" {
		t.Fatal(manual)
	}
	bad := rawArchiveCall(a, "POST", "/api/calculator/capture", `{"kind":"cell","workbookKey":"other","sheet":"数据","ref":"A2"}`)
	if bad.Code != 409 {
		t.Fatal("different workbook was read", bad.Body.String())
	}
	status := featureCall(t, a, "GET", "/api/status", "")
	if !bytes.Contains(status.Body.Bytes(), []byte(key)) {
		t.Fatal("source identity missing")
	}
}

func TestCalculatorAssetsAreEmbeddedAndServedWithoutWorkbook(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, name := range []string{"calculator-model.js", "calculator.js", "calculator.css", "cellview.js"} {
		res := featureCall(t, a, "GET", "/assets/"+name, "")
		if res.Body.Len() < 500 {
			t.Fatal("asset missing", name)
		}
	}
	page := featureCall(t, a, "GET", "/", "").Body.String()
	if !strings.Contains(page, `aria-modal="false"`) || !strings.Contains(page, `id="calculatorOpenBtn"`) {
		t.Fatal("modeless calculator not connected")
	}
	got := decodeResponse[calculator.Result](t, featureCall(t, a, "POST", "/api/calculator/formula", `{"expression":"=0.1+0.2"}`))
	if got.Exact != "0.3" {
		t.Fatal("formula requires a workbook", got)
	}
}

func TestCalculatorFormulaBuildsUnopenedReferencedSheet(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "cross-sheet.xlsx")
	writeMultiSheetWorkbook(t, path)
	a := prepareFeatureApp(t, path)
	defer a.Close()
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("Second")
	if wb.GetCachedIndex(meta.Path) != nil {
		t.Fatal("fixture already indexed")
	}
	body, _ := json.Marshal(map[string]string{"expression": "=A1+Second!C1", "sheet": "First", "workbookKey": wb.Fingerprint})
	got := decodeResponse[calculator.Result](t, featureCall(t, a, "POST", "/api/calculator/formula", string(body)))
	if got.Exact != "3" || wb.GetCachedIndex(meta.Path) == nil {
		t.Fatal("typed cross-sheet reference failed", got)
	}
}

func TestCalculatorFormulaUsesNativeAddressesAndRangeFunctions(t *testing.T) {
	a, key := calculatorTestWorkbook(t)
	for expression, want := range map[string]string{
		"=A2+A3": "0.3", "='数据'!$A$2+A3": "0.3", "=SUM(A2:A4)": "3.3", "=AVERAGE(A2:A4)": "1.1", "=MIN(A2:A4)": "0.1", "=MAX(A2:A4)": "3", "=SUM(A2:A4)/3": "1.1", "=A100+1": "1", "=0.1+0.2": "0.3", "=AVERAGE(A2,A100)": "0.1", "=SUM(B1,A2)": "0.1", "=SUM(A100:A110)": "0", "=AVERAGE（A2，A3）": "0.15",
	} {
		body, _ := json.Marshal(map[string]string{"expression": expression, "sheet": "数据", "workbookKey": key})
		got := decodeResponse[calculator.Result](t, featureCall(t, a, "POST", "/api/calculator/formula", string(body)))
		if got.Exact != want {
			t.Fatal(expression, got, "want", want)
		}
	}
	for _, expression := range []string{"=B2+1", "=A2/0", "=XFE1+1", "=A1000000001+1", "='不存在'!A2"} {
		body, _ := json.Marshal(map[string]string{"expression": expression, "sheet": "数据", "workbookKey": key})
		res := rawArchiveCall(a, "POST", "/api/calculator/formula", string(body))
		if res.Code != 422 {
			t.Fatal("invalid formula accepted", expression, res.Body.String())
		}
	}
	bad := rawArchiveCall(a, "POST", "/api/calculator/formula", `{"expression":"=A2+1","sheet":"数据","workbookKey":"wrong"}`)
	if bad.Code != 422 {
		t.Fatal("other workbook was read", bad.Body.String())
	}
}

func TestCalculatorFormulaTextBooleanAndFunctionCatalog(t *testing.T) {
	a, key := calculatorTestWorkbook(t)
	for expression, want := range map[string]string{"=1=1": "TRUE", "=IF(A2=A2,\"一致\",\"差异\")": "一致", "=B2": "甲", "=A5": "0000123", "=LEFT(A5,3)": "000", "=B2&\"/\"&B3": "甲/乙", "=TEXTJOIN(\"/\",TRUE,B2:B4)": "甲/乙/丙", "=COUNTA(A1:B4)": "8", "=ISNUMBER(A2)": "FALSE"} {
		body, _ := json.Marshal(map[string]string{"expression": expression, "sheet": "数据", "workbookKey": key})
		got := decodeResponse[calculator.Result](t, featureCall(t, a, "POST", "/api/calculator/formula", string(body)))
		if got.Display != want {
			t.Fatal(expression, got, "want", want)
		}
	}
	catalog := decodeResponse[struct{ Items []calculator.FunctionSpec }](t, featureCall(t, a, "GET", "/api/calculator/functions", ""))
	if len(catalog.Items) < 20 {
		t.Fatal("completion catalog incomplete")
	}
}

func TestCalculatorLargeContiguousSelectionStreamsMoreThanQuickStatLimit(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "large.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	_, _ = w.WriteString("甲,乙\n")
	const count = 200001
	for i := 0; i < count; i++ {
		_, _ = w.WriteString("0.1,0.2\n")
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	collector := calculatorCollector{sum: new(big.Rat)}
	start := time.Now()
	err = scanCalculatorSelection(context.Background(), wb, idx, nil, selectionStatRequest{Ranges: []selectionStatRect{{Start: "A2", End: fmt.Sprintf("B%d", count+1)}}}, &collector)
	if err != nil || collector.sum.RatString() != "600003/10" || collector.total != count*2 || collector.number != count*2 {
		t.Fatal(collector, err)
	}
	t.Logf("400002 numeric cells, exact sum 60000.3, streaming scan %s", time.Since(start))
	tooLarge := calculatorCollector{sum: new(big.Rat)}
	if err := scanCalculatorSelection(context.Background(), wb, idx, nil, selectionStatRequest{Ranges: []selectionStatRect{{Start: "A1", End: "B2000001"}}}, &tooLarge); err == nil {
		t.Fatal("unbounded explicit scan accepted")
	}
}

func TestCalculatorFilteredScanMatchesIndependentSelectionPlanner(t *testing.T) {
	a, _ := calculatorTestWorkbook(t)
	wb := a.currentWorkbook()
	idx := wb.GetCachedIndex(wb.Sheets[0].Path)
	rng := rand.New(rand.NewSource(1700))
	for iteration := 0; iteration < 80; iteration++ {
		order := rng.Perm(idx.RowCount())
		filter := &FilterResult{Rows: rowOrderFixture(t, idx.RowCount(), order...)}
		frozen := rng.Intn(3)
		visible := []int{}
		for _, i := range order {
			if idx.RowAt(i).Number > frozen {
				visible = append(visible, i)
			}
		}
		v1, v2 := 1+rng.Intn(len(visible)), 1+rng.Intn(len(visible))
		r1, r2 := idx.RowAt(visible[v1-1]).Number, idx.RowAt(visible[v2-1]).Number
		if frozen > 0 && iteration%3 == 0 {
			r1 = 1
			v1 = 1
		}
		if frozen > 0 && iteration%3 == 1 {
			r2 = 1
			v2 = 1
		}
		req := selectionStatRequest{ExcludeThroughRow: frozen, Ranges: []selectionStatRect{{Start: fmt.Sprintf("A%d", r1), End: fmt.Sprintf("B%d", r2), VisualStart: v1, VisualEnd: v2}}}
		selected, total, err := makeStatRows(req, idx, filter)
		if err != nil {
			t.Fatal(err)
		}
		want := calculatorCollector{sum: new(big.Rat), total: total}
		for row, spans := range selected {
			for _, span := range spans {
				rows, err := wb.ReadRowsContext(context.Background(), idx, idx.FindRowOffset(row), 1, span.lo, span.hi)
				if err != nil {
					t.Fatal(err)
				}
				if err := want.add(rows[0]); err != nil {
					t.Fatal(err)
				}
			}
		}
		got := calculatorCollector{sum: new(big.Rat)}
		if err := scanCalculatorSelection(context.Background(), wb, idx, filter, req, &got); err != nil {
			t.Fatal(err)
		}
		if got.sum.Cmp(want.sum) != 0 || got.total != want.total || got.number != want.number || got.nonEmpty != want.nonEmpty {
			t.Fatalf("scan differs at %d: %+v / %+v", iteration, got, want)
		}
	}
}

func TestCalculatorMissingFormulaOrExcelErrorNeverYieldsPartialSum(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "errors.xlsx")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, data := range map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="First" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><v>7.28</v></c><c r="B1"><f>A1*2</f></c><c r="C1" t="e"><v>#DIV/0!</v></c><c r="D1" t="inlineStr"><is><t>0000123</t></is></c><c r="E1" t="inlineStr"><is><t>12.50</t></is></c></row></sheetData></worksheet>`,
	} {
		entry, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	a := prepareFeatureApp(t, path)
	defer a.Close()
	key := a.currentWorkbook().Fingerprint
	for _, kind := range []string{"cell", "sum"} {
		for _, ref := range []string{"B1", "C1"} {
			body, _ := json.Marshal(calculatorCaptureRequest{Kind: kind, WorkbookKey: key, Sheet: "First", Ref: ref, Ranges: []selectionStatRect{{Start: "A1", End: ref}}})
			res := rawArchiveCall(a, "POST", "/api/calculator/capture", string(body))
			if res.Code != 422 {
				t.Fatal("bad source produced amount", kind, ref, res.Code, res.Body.String())
			}
		}
	}
	body, _ := json.Marshal(calculatorCaptureRequest{Kind: "sum", WorkbookKey: key, Sheet: "First", Ranges: []selectionStatRect{{Start: "D1", End: "F1"}}})
	got := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", string(body)))
	if got.Value != "25/2" || got.Numeric != 1 || got.Text != 1 || got.Blank != 1 || got.TextNumbers != 1 {
		t.Fatal("source classification wrong", got)
	}
}

func TestCalculatorHistoryRetentionBudgetBackupAndCacheCleanup(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for i := 0; i < 55; i++ {
		featureCall(t, a, "POST", "/api/calculator/history", fmt.Sprintf(`{"name":"核对%d","expression":"%d+0.1","precision":-1}`, i, i))
	}
	history, err := readCalculatorHistory(calculatorHistoryPath())
	if err != nil || len(history.Records) != 50 || history.Records[0].Name != "核对54" || history.Records[49].Name != "核对5" {
		t.Fatal("retention wrong", len(history.Records), err)
	}
	// Simulate interruption between renaming the original and promoting temp.
	if err = os.Rename(calculatorHistoryPath(), calculatorHistoryPath()+".backup"); err != nil {
		t.Fatal(err)
	}
	recovered, err := readCalculatorHistory(calculatorHistoryPath())
	if err != nil || len(recovered.Records) != 50 {
		t.Fatal("backup recovery failed", err)
	}
	featureCall(t, a, "POST", "/api/calculator/history", `{"name":"recovered","expression":"1+2","precision":-1}`)
	before, err := os.ReadFile(calculatorHistoryPath())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(workbook.CacheRoot()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(calculatorHistoryPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("cache cleanup removed calculation history", err)
	}
	// Large chained calculations retain newest whole records within 4 MiB.
	large := calculatorHistoryFile{Version: 1, Records: make([]savedCalculation, 50)}
	for i := range large.Records {
		large.Records[i] = savedCalculation{calculatorSnapshot: calculatorSnapshot{Name: fmt.Sprint(i), Note: strings.Repeat("a", 100000)}}
	}
	if err = writeCalculatorHistory(calculatorHistoryPath(), large); err != nil {
		t.Fatal(err)
	}
	trimmed, err := readCalculatorHistory(calculatorHistoryPath())
	if err != nil || len(trimmed.Records) >= 50 || len(trimmed.Records) == 0 || trimmed.Records[0].Name != "0" {
		t.Fatal("size retention wrong", err)
	}
	info, err := os.Stat(calculatorHistoryPath())
	if err != nil || info.Size() > 4<<20 {
		t.Fatal("history exceeds budget", err)
	}
}

// Optional private-source integration, never distributed with the source ZIP.
func TestCalculatorProvidedLedgerArchive(t *testing.T) {
	path := os.Getenv("EXCELLENS_CALCULATOR_REAL_ARCHIVE")
	if path == "" {
		t.Skip("set EXCELLENS_CALCULATOR_REAL_ARCHIVE to the provided ledger 7z")
	}
	isolateTestStorage(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(before)
	archive, err := archives.Open(context.Background(), path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	file, member, err := archive.Extract(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	wb.SetDisplayName(member.Name)
	if len(wb.Sheets) != 1 {
		t.Fatal("original single sheet changed", len(wb.Sheets))
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 75475 || idx.MaxCol != 14 {
		t.Fatal("source dimensions changed", idx.MaxRow, idx.MaxCol)
	}
	sources := []calculatorOperand{}
	for i, ref := range []string{"F12", "G12"} {
		body, _ := json.Marshal(calculatorCaptureRequest{Kind: "cell", WorkbookKey: wb.Fingerprint, Sheet: wb.Sheets[0].Name, Ref: ref})
		op := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", string(body)))
		if op.Raw != "38,403.75" || op.Value != "153615/4" || op.File != member.Name {
			t.Fatal("original amount/source lost", op)
		}
		op.ID = fmt.Sprint(i + 1)
		sources = append(sources, op)
	}
	body, _ := json.Marshal(calculatorCaptureRequest{Kind: "sum", WorkbookKey: wb.Fingerprint, Sheet: wb.Sheets[0].Name, Ranges: []selectionStatRect{{Start: "F12", End: "G12"}}})
	sum := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", string(body)))
	if sum.Value != "153615/2" || sum.Numeric != 2 {
		t.Fatal("real-source sum wrong", sum)
	}
	snapshot := calculatorSnapshot{Name: "原账借方核对", Expression: "[1]-[2]", Precision: 2, Operands: sources}
	body, _ = json.Marshal(snapshot)
	saved := decodeResponse[struct {
		ID string `json:"id"`
	}](t, featureCall(t, a, "POST", "/api/calculator/history", string(body)))
	restored := decodeResponse[savedCalculation](t, featureCall(t, a, "GET", "/api/calculator/history?id="+saved.ID, ""))
	if !restored.Result.Zero || restored.Result.Copy != "0.00" || len(restored.Operands) != 2 {
		t.Fatal("saved reconciliation wrong", restored)
	}
	for expression, want := range map[string]string{"=F12-G12": "0", "=SUM(F12:G12)": "76807.5", "=(F12+G12)/2": "38403.75"} {
		body, _ := json.Marshal(map[string]string{"expression": expression, "workbookKey": wb.Fingerprint, "sheet": wb.Sheets[0].Name})
		got := decodeResponse[calculator.Result](t, featureCall(t, a, "POST", "/api/calculator/formula", string(body)))
		if got.Exact != want {
			t.Fatal(expression, got)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(after) != digest {
		t.Fatal("original archive changed", err)
	}
	t.Log("provided 7z -> one 75475x14 sheet -> F12/G12 original 38,403.75 -> =F12-G12 exact 0; =SUM(F12:G12) 76807.5; =(F12+G12)/2 38403.75; original archive hash unchanged")
}

func TestCalculatorFilteredSelectionsKeepSourceCoordinates(t *testing.T) {
	a, key := calculatorTestWorkbook(t)
	wb := a.currentWorkbook()
	idx := wb.GetCachedIndex(wb.Sheets[0].Path)
	a.filterResults["sorted"] = &FilterResult{ID: "sorted", Sheet: "数据", SheetPath: idx.SheetPath, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 0, 3, 1, 2)}
	for _, tc := range []struct {
		start, end string
		v1, v2     int
		want       string
		total      int64
	}{
		{"A4", "A2", 1, 2, "31/10", 2}, {"A1", "A2", 1, 2, "31/10", 3}, {"A4", "A1", 1, 1, "3", 2},
	} {
		req := calculatorCaptureRequest{Kind: "sum", WorkbookKey: key, Sheet: "数据", FilterID: "sorted", ExcludeThroughRow: 1, Ranges: []selectionStatRect{{Start: tc.start, End: tc.end, VisualStart: tc.v1, VisualEnd: tc.v2}}}
		body, _ := json.Marshal(req)
		got := decodeResponse[calculatorOperand](t, featureCall(t, a, "POST", "/api/calculator/capture", string(body)))
		if got.Value != tc.want || got.Total != tc.total || !got.Filtered {
			t.Fatal(got, tc)
		}
	}
	bad := rawArchiveCall(a, "POST", "/api/calculator/capture", fmt.Sprintf(`{"kind":"sum","workbookKey":%q,"sheet":"数据","filterId":"sorted","excludeThroughRow":1,"ranges":[{"start":"A1","end":"A2","visualStart":1,"visualEnd":0}]}`, key))
	if bad.Code != 422 {
		t.Fatal("invalid filtered range lost data silently", bad.Body.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collector := calculatorCollector{sum: new(big.Rat)}
	if err := scanCalculatorSelection(ctx, wb, idx, nil, selectionStatRequest{Ranges: []selectionStatRect{{Start: "A1", End: "A3"}}}, &collector); err == nil {
		t.Fatal("cancelled range scan completed")
	}
}

func TestCalculatorHistoryRecomputesAndPersistsIndependentConcurrentRecords(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var wg sync.WaitGroup
	errors := make(chan string, 8)
	for i := 1; i <= 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"name":"核对%d","expression":"0.1+0.2+%d","precision":-1,"percent":false,"operands":[]}`, n, n)
			res := rawArchiveCall(a, "POST", "/api/calculator/history", body)
			if res.Code != 200 {
				errors <- res.Body.String()
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	list := decodeResponse[struct {
		Items []struct {
			ID string `json:"id"`
		}
	}](t, featureCall(t, a, "GET", "/api/calculator/history", ""))
	if len(list.Items) != 8 {
		t.Fatal("concurrent save lost records", len(list.Items))
	}
	item := decodeResponse[savedCalculation](t, featureCall(t, a, "GET", "/api/calculator/history?id="+list.Items[0].ID, ""))
	if item.Result.Value == "" || item.Result.Exact == "" {
		t.Fatal(item)
	}
	reopened, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	detail := featureCall(t, reopened, "GET", "/api/calculator/history?id="+item.ID, "")
	if detail.Code != http.StatusOK {
		t.Fatal(detail.Body.String())
	}
	before, _ := os.ReadFile(calculatorHistoryPath())
	if err := os.WriteFile(calculatorHistoryPath(), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	res := rawArchiveCall(a, "POST", "/api/calculator/history", `{"name":"new","expression":"1+2","precision":-1}`)
	if res.Code != 500 {
		t.Fatal("corrupt history was overwritten", res.Body.String())
	}
	after, _ := os.ReadFile(calculatorHistoryPath())
	if string(after) != "broken" {
		t.Fatal("evidence was destroyed")
	}
	_ = os.WriteFile(calculatorHistoryPath(), before, 0600)
	result := decodeResponse[calculator.Result](t, featureCall(t, a, "POST", "/api/calculator/evaluate", `{"expression":"1/3*3","operands":{},"precision":-1}`))
	if result.Exact != "1" {
		t.Fatal(result)
	}
}
