package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/printing"
	"excellens/internal/workbook"
)

func printServerParts() map[string]string {
	parts := catalogueServerParts(0)
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<sheetData>`, `<sheetFormatPr defaultRowHeight="15"/><sheetData>`, 1)
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `</sheetData>`, `</sheetData><mergeCells count="1"><mergeCell ref="A1:B1"/></mergeCells><pageMargins left="0" right="0" top="0" bottom="0" header="0" footer="0"/><pageSetup paperSize="9" orientation="portrait"/>`, 1)
	return parts
}

func printServerFixture(t *testing.T, change func(map[string]string)) (*App, string) {
	t.Helper()
	isolateTestStorage(t)
	parts := printServerParts()
	if change != nil {
		change(parts)
	}
	file := filepath.Join(t.TempDir(), "printing.xlsx")
	writeZipArchive(t, file, parts)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	return a, file
}

func printOptions() printing.Options {
	return printing.Options{UsePreviewDefaults: true, Paper: &printing.Paper{Width: "100", Height: "45"}, Orientation: "landscape", Margins: &printing.Margins{Left: "0", Right: "0", Top: "0", Bottom: "0", Header: "0", Footer: "0"}, ScalePercent: "100", MissingColumnWidth: "20", RepeatRows: []printing.Interval{{First: 1, Last: 1}}}
}

func printStart(t *testing.T, a *App, options printing.Options) *printTask {
	t.Helper()
	wb := a.currentWorkbook()
	response := reconcilePost(t, a, "/api/printing/start", printStartRequest{wb.Fingerprint, sourceBookID(wb), "明细", options})
	id := decodeResponse[struct{ JobID string }](t, response).JobID
	a.mu.RLock()
	job := a.printJobs[id]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("printing start did not retain task", response.Code, response.Body.String())
	}
	return job
}

func printWait(t *testing.T, a *App, job *printTask) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("print task did not finish")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if job.Status != "done" || job.Snapshot == nil {
		t.Fatal(job.Status, job.Error)
	}
}

type printPageResponse struct {
	ID, WorkbookKey, Book, Sheet string
	Page                         int
	Content                      printPageContent
}

func printPageFor(t *testing.T, a *App, job *printTask, index int) printPageResponse {
	t.Helper()
	return decodeResponse[printPageResponse](t, analysisRequest(t, a, "GET", fmt.Sprintf("/api/printing/page?id=%s&page=%d", job.ID, index), ""))
}

func TestPrintHTTPPreparedSourceRepeatedMergedTitlesAndSourceTruth(t *testing.T) {
	a, file := printServerFixture(t, nil)
	wb := a.currentWorkbook()
	before, _ := os.ReadFile(file)
	if response := reconcilePost(t, a, "/api/printing/start", printStartRequest{wb.Fingerprint, sourceBookID(wb), "明细", printOptions()}); response.Code != 409 {
		t.Fatal("printing implicitly indexed source", response.Code)
	}
	prepareDataRuleSheet(t, a)
	job := printStart(t, a, printOptions())
	printWait(t, a, job)
	summary := decodeResponse[struct {
		Summary          printing.Summary
		SourceProperties map[string]map[string]workbook.SourcePrintProperty
	}](t, analysisRequest(t, a, "GET", "/api/printing/summary?id="+job.ID, ""))
	if summary.Summary.Pages != 2 || len(summary.SourceProperties) == 0 || summary.Summary.Origins["paper"] != "preview-override" {
		t.Fatal("source/derived settings or page count lost", summary)
	}
	values := map[string]workbook.CellData{}
	for index := 0; index < summary.Summary.Pages; index++ {
		response := printPageFor(t, a, job, index)
		if response.ID != job.ID || response.WorkbookKey != wb.Fingerprint || response.Book != sourceBookID(wb) || response.Page != index || response.Sheet != "明细" {
			t.Fatal("page mixed source identity", response)
		}
		titles := 0
		for _, item := range response.Content.Cells {
			if item.Clip.Width <= 0 || item.Clip.Height <= 0 || item.Box.Width < item.Clip.Width || item.Box.Height < item.Clip.Height {
				t.Fatal("invalid physical source slice", item)
			}
			if item.Cell != nil {
				if item.Ref != item.Cell.Ref {
					t.Fatal("page relocated source cell", item)
				}
				values[item.Ref] = *item.Cell
			}
			if item.Merge != nil {
				titles++
				if item.Ref != "A1" || item.Merge.EndCol != 2 || item.Clip.Width != 40*formats.PrintGeometryUnitsPerPoint {
					t.Fatal("merged repeated title lost source or dimensions", item)
				}
			}
			if item.Ref == "B1" {
				t.Fatal("covered merge cell was rendered as independent source")
			}
		}
		if titles != 1 || !response.Content.Rows[0].Repeated {
			t.Fatal("repeated merged title was not included exactly once", titles, response.Content.Rows)
		}
	}
	if values["A2"].Raw != "001" || values["A2"].Type != "inlineStr" || values["B2"].Raw != "9007199254740993.31" || values["B2"].Type != "" || values["B3"].Raw != "0" {
		t.Fatal("page converted source values or text types", values)
	}
	if !values["B4"].HasFormula || !values["B4"].CacheMissing || values["B4"].Formula != "=SUM(B2:B3)" {
		t.Fatal("page invented formula cache", values["B4"])
	}
	meta, _ := wb.FindSheet("汇总")
	if wb.GetCachedIndex(meta.Path) != nil {
		t.Fatal("preview indexed an unrelated worksheet")
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("preview wrote source")
	}
}

func TestPrintHTTPOversizeRowKeepsWholePhysicalCoverageAndHiddenAnchor(t *testing.T) {
	t.Run("oversize-source-row", func(t *testing.T) {
		a, _ := printServerFixture(t, func(parts map[string]string) {
			parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<row r="3">`, `<row r="3" ht="3000" customHeight="1">`, 1)
		})
		prepareDataRuleSheet(t, a)
		options := printOptions()
		options.RepeatRows = []printing.Interval{}
		job := printStart(t, a, options)
		printWait(t, a, job)
		var total int64
		var slices int
		for index := 0; index < job.Snapshot.plan.Summary().Pages; index++ {
			page := printPageFor(t, a, job, index)
			for _, item := range page.Content.Cells {
				if item.Ref == "A3" {
					slices++
					total += item.Clip.Height
					if item.Cell == nil || item.Cell.Raw != "002" || item.Box.Height != 3000*formats.PrintGeometryUnitsPerPoint || item.Clip.Height > 45*formats.PrintGeometryUnitsPerPoint {
						t.Fatal("oversized row was clamped, normalized or truncated", item)
					}
				}
			}
		}
		if total != 3000*formats.PrintGeometryUnitsPerPoint || slices < 60 {
			t.Fatal("physical slices did not exactly cover oversized source", total, slices)
		}
	})
	t.Run("hidden-merge-anchor", func(t *testing.T) {
		a, _ := printServerFixture(t, func(parts map[string]string) {
			text := strings.Replace(parts["xl/worksheets/sheet1.xml"], `<row r="1">`, `<row r="1" hidden="1">`, 1)
			parts["xl/worksheets/sheet1.xml"] = strings.Replace(text, `ref="A1:B1"`, `ref="A1:A2"`, 1)
		})
		prepareDataRuleSheet(t, a)
		options := printOptions()
		options.RepeatRows = []printing.Interval{}
		job := printStart(t, a, options)
		printWait(t, a, job)
		page := printPageFor(t, a, job, 0)
		found := false
		for _, item := range page.Content.Cells {
			if item.Merge != nil {
				found = true
				if !item.HiddenAnchor || item.Row != 1 || item.Cell == nil || item.Cell.Ref != "A1" || item.Cell.Raw != "编号" {
					t.Fatal("hidden merge anchor was moved into a visible source cell", item)
				}
			}
			if item.Ref == "A2" {
				t.Fatal("covered visible cell replaced hidden source anchor")
			}
		}
		if !found {
			t.Fatal("hidden source merge was lost")
		}
	})
}

func TestPrintHTTPQueueCancellationPinsReplacementAndLateCompletion(t *testing.T) {
	a, file := printServerFixture(t, nil)
	prepareDataRuleSheet(t, a)
	wb := a.currentWorkbook()
	for at := 0; at < cap(a.querySem); at++ {
		a.querySem <- struct{}{}
	}
	first, second := printStart(t, a, printOptions()), printStart(t, a, printOptions())
	if response := reconcilePost(t, a, "/api/printing/start", printStartRequest{wb.Fingerprint, sourceBookID(wb), "明细", printOptions()}); response.Code != 429 {
		t.Fatal("print active task limit not enforced", response.Code)
	}
	a.sourceMu.Lock()
	pins := a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 2 {
		t.Fatal("queued previews did not pin source", pins)
	}
	reconcilePost(t, a, "/api/printing/cancel", map[string]string{"id": first.ID})
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled queued print did not finish")
	}
	if first.Status != "cancelled" || first.Snapshot != nil {
		t.Fatal("cancelled print published partial plan")
	}
	copyPath := filepath.Join(t.TempDir(), "equal-copy.xlsx")
	data, _ := os.ReadFile(file)
	if err := os.WriteFile(copyPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(copyPath); err != nil {
		t.Fatal(err)
	}
	for at := 0; at < cap(a.querySem); at++ {
		<-a.querySem
	}
	select {
	case <-second.done:
	default:
		t.Fatal("workbook replacement left print worker running")
	}
	a.sourceMu.Lock()
	pins = a.sourcePins[wb]
	a.sourceMu.Unlock()
	if pins != 0 || len(a.printJobs) != 0 || second.Snapshot != nil {
		t.Fatal("replacement leaked print pins or snapshots", pins)
	}
	if response := reconcilePost(t, a, "/api/printing/start", printStartRequest{wb.Fingerprint, sourceBookID(wb), "明细", printOptions()}); response.Code != 409 {
		t.Fatal("same-byte different path accepted old print identity", response.Code)
	}
	prepareDataRuleSheet(t, a)
	job := printStart(t, a, printOptions())
	printWait(t, a, job)
	reconcilePost(t, a, "/api/printing/cancel", map[string]string{"id": job.ID})
	if response := analysisRequest(t, a, "GET", "/api/printing/status?id="+job.ID, ""); response.Code != 404 {
		t.Fatal("late cancel retained unadopted finished snapshot")
	}
}

func TestPrintHTTPHistorySessionsInvalidOptionsAndChangedSource(t *testing.T) {
	a, file := printServerFixture(t, nil)
	prepareDataRuleSheet(t, a)
	var first string
	var last *printTask
	for at := 0; at < formats.PrintPreviewTaskHistory+2; at++ {
		last = printStart(t, a, printOptions())
		printWait(t, a, last)
		if first == "" {
			first = last.ID
		}
	}
	a.mu.RLock()
	snapshots, bytes := 0, int64(0)
	for _, job := range a.printJobs {
		if job.Snapshot != nil {
			snapshots++
			bytes += job.Snapshot.BudgetBytes()
		}
	}
	count := len(a.printJobs)
	a.mu.RUnlock()
	if snapshots > formats.PrintPreviewSnapshots || count > formats.PrintPreviewTaskHistory || bytes > formats.PrintPreviewSnapshotBytes {
		t.Fatal("unbounded print history", snapshots, count, bytes)
	}
	if response := analysisRequest(t, a, "GET", "/api/printing/status?id="+first, ""); response.Code != 404 {
		t.Fatal("evicted print task still exists")
	}
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	if response := analysisRequest(t, child.app, "GET", "/api/printing/summary?id="+last.ID, ""); response.Code != 409 {
		t.Fatal("print crossed browser sessions")
	}
	for _, body := range []string{`{}`, `{"sheet":"明细","unexpected":1}`, strings.Repeat("x", formats.PrintPreviewRequestBytes+1)} {
		if response := analysisRequest(t, a, "POST", "/api/printing/start", body); response.Code != 400 {
			t.Fatal("malformed print options accepted", response.Code)
		}
	}
	for _, page := range []string{"", "-1", "2", "1.5", "99999999999999999999999"} {
		if response := analysisRequest(t, a, "GET", "/api/printing/page?id="+last.ID+"&page="+url.QueryEscape(page), ""); response.Code != 400 {
			t.Fatal("invalid print page accepted", page, response.Code)
		}
	}
	parts := printServerParts()
	parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], "9007199254740993.31", "9007199254740993.32", 1)
	writeZipArchive(t, file, parts)
	if response := analysisRequest(t, a, "GET", "/api/printing/page?id="+last.ID+"&page=0", ""); response.Code != 409 {
		t.Fatal("changed source served old print cells", response.Code)
	}
}

func TestPrintHTTPDensePagesAndUnreliableSettingsNeverPublishPartialSuccess(t *testing.T) {
	a, _ := printServerFixture(t, nil)
	prepareDataRuleSheet(t, a)
	options := printOptions()
	options.ScalePercent = ""
	options.Fit = &printing.Fit{Width: 1, Height: 1}
	options.Areas = []workbook.CellMerge{{Row: 1, Col: 1, EndRow: 10000, EndCol: 1}}
	options.RepeatRows = []printing.Interval{}
	job := printStart(t, a, options)
	printWait(t, a, job)
	response := analysisRequest(t, a, "GET", "/api/printing/page?id="+job.ID+"&page=0", "")
	if response.Code != 413 || strings.Contains(response.Body.String(), `"content"`) {
		t.Fatal("dense page became truncated success", response.Code, response.Body.String())
	}
	options = printOptions()
	options.Paper.Width = "-1"
	failed := printStart(t, a, options)
	select {
	case <-failed.done:
	case <-time.After(time.Second):
		t.Fatal("invalid paper did not terminate")
	}
	if failed.Status != "error" || failed.Snapshot != nil || failed.Error == "" {
		t.Fatal("invalid paper became partial success", failed.Status, failed.Error)
	}
	if response := analysisRequest(t, a, "GET", "/api/printing/summary?id="+failed.ID, ""); response.Code != 409 {
		t.Fatal("failed plan served a successful summary")
	}
}

type printAxisOracle struct {
	values map[int]int64
	calls  int
}

func (axis *printAxisOracle) Extent(ctx context.Context, _ string, first, last int) (workbook.PrintGeometryExtent, error) {
	axis.calls++
	var sum int64
	for at, value := range axis.values {
		if at >= first && at <= last {
			sum += value
		}
	}
	return workbook.PrintGeometryExtent{Units: sum}, ctx.Err()
}

func TestPrintPageAxisIndependentPhysicalReferenceHiddenGapsAndCancellation(t *testing.T) {
	random := rand.New(rand.NewPCG(0x147ed, 0x204a3))
	for trial := 0; trial < 1200; trial++ {
		axis := &printAxisOracle{values: map[int]int64{}}
		first, last := 1+random.IntN(10), 50+random.IntN(50)
		var units int64
		for at := first; at <= last; at++ {
			value := int64(random.IntN(6))
			axis.values[at] = value
			units += value
		}
		part := printing.Part{First: first, Last: last, Length: units, Repeated: trial%2 == 0}
		if trial%3 == 0 {
			part.Last = first
			axis.values[first] = 10
			part.Offset, part.Length = int64(random.IntN(8)), 2
		}
		want := []printPageAxis{}
		position := -part.Offset
		for at := part.First; at <= part.Last; at++ {
			size := axis.values[at]
			left, right := max(position, 0), min(position+size, part.Length)
			if right > left {
				want = append(want, printPageAxis{Number: at, Start: position, Size: size, ClipStart: left, ClipSize: right - left, Repeated: part.Repeated})
			}
			position += size
		}
		work := &printPageWork{ctx: context.Background()}
		got, err := work.axis(axis, "row", []printing.Part{part}, 100)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("independent physical oracle trial %d: %v\n got=%+v\nwant=%+v", trial, err, got, want)
		}
	}
	axis := &printAxisOracle{values: map[int]int64{1000000: 20}}
	work := &printPageWork{ctx: context.Background()}
	got, err := work.axis(axis, "row", []printing.Part{{First: 1, Last: 1000000, Length: 20}}, 1)
	if err != nil || len(got) != 1 || got[0].Number != 1000000 || axis.calls > 30 {
		t.Fatal("hidden million source interval expanded instead of prefix lookup", got, err, axis.calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	work.ctx = ctx
	if _, err := work.axis(axis, "row", []printing.Part{{First: 1, Last: 1000000, Length: 20}}, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled page geometry kept working", err)
	}
}

func TestPrintHTTPUnsupportedRegistryAndReplacedExactIndex(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		a, _ := namedViewFixture(t)
		wb := a.currentWorkbook()
		if response := reconcilePost(t, a, "/api/printing/start", printStartRequest{wb.Fingerprint, sourceBookID(wb), "any", printOptions()}); response.Code != 422 {
			t.Fatal("print formats inferred outside registry", response.Code)
		}
	})
	t.Run("index-replaced", func(t *testing.T) {
		a, _ := printServerFixture(t, nil)
		prepareDataRuleSheet(t, a)
		job := printStart(t, a, printOptions())
		printWait(t, a, job)
		wb := a.currentWorkbook()
		wb.ClearIndexes()
		prepareDataRuleSheet(t, a)
		if response := analysisRequest(t, a, "GET", "/api/printing/summary?id="+job.ID, ""); response.Code != 409 {
			t.Fatal("stale exact index served print snapshot", response.Code)
		}
	})
}

func TestPrintPageRawHTMLRemainsJSONText(t *testing.T) {
	a, _ := printServerFixture(t, func(parts map[string]string) {
		parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<t>001</t>`, `<t>&lt;img src=x onerror=alert(1)&gt;</t>`, 1)
	})
	prepareDataRuleSheet(t, a)
	job := printStart(t, a, printOptions())
	printWait(t, a, job)
	response := printPageFor(t, a, job, 0)
	found := false
	for _, item := range response.Content.Cells {
		if item.Ref == "A2" {
			found = true
			if item.Cell == nil || item.Cell.Raw != `<img src=x onerror=alert(1)>` || item.Cell.Type != "inlineStr" {
				t.Fatal("page interpreted source text as markup", item)
			}
		}
	}
	if !found {
		t.Fatal("source text missing")
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(string(encoded), `<img`) {
		t.Fatal("JSON response did not escape source markup")
	}
}

func TestPrintHTTPEmptySourceDoesNotInventA1Page(t *testing.T) {
	a, _ := printServerFixture(t, func(parts map[string]string) {
		parts["xl/worksheets/sheet1.xml"] = `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`
	})
	prepareDataRuleSheet(t, a)
	job := printStart(t, a, printOptions())
	printWait(t, a, job)
	if job.Snapshot.plan.Summary().Pages != 0 {
		t.Fatal("empty index invented source A1 and a page")
	}
	if response := analysisRequest(t, a, "GET", "/api/printing/page?id="+job.ID+"&page=0", ""); response.Code != 400 {
		t.Fatal("empty source returned a successful fake page", response.Code)
	}
}

func TestPrintHTTPQueuedPageClearAndRequestCancellationReleasePins(t *testing.T) {
	for _, mode := range []string{"clear", "request-cancel", "history-eviction"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := printServerFixture(t, nil)
			prepareDataRuleSheet(t, a)
			job := printStart(t, a, printOptions())
			printWait(t, a, job)
			for at := 0; at < cap(a.querySem); at++ {
				a.querySem <- struct{}{}
			}
			defer func() {
				for at := 0; at < cap(a.querySem); at++ {
					<-a.querySem
				}
			}()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request := httptest.NewRequest("GET", "/api/printing/page?id="+job.ID+"&page=0", nil).WithContext(ctx)
			response, done := httptest.NewRecorder(), make(chan struct{})
			go func() { a.routes().ServeHTTP(response, request); close(done) }()
			deadline := time.Now().Add(time.Second)
			for {
				a.sourceMu.Lock()
				pins := a.sourcePins[job.Workbook]
				a.sourceMu.Unlock()
				if pins == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("queued page did not acquire source pin")
				}
				time.Sleep(time.Millisecond)
			}
			switch mode {
			case "clear":
				reconcilePost(t, a, "/api/printing/clear", map[string]string{"id": job.ID})
			case "history-eviction":
				a.mu.Lock()
				a.prunePrintTasksLocked("", 0)
				a.mu.Unlock()
			default:
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("obsolete page waited for a query slot after cancellation")
			}
			a.sourceMu.Lock()
			pins := a.sourcePins[job.Workbook]
			a.sourceMu.Unlock()
			if pins != 0 {
				t.Fatal("page request leaked source pin", pins)
			}
			if mode != "request-cancel" && response.Code != 409 {
				t.Fatal("obsolete page became an empty success", response.Code)
			}
		})
	}
}

func TestPrintHTTPBadMergeDeclarationsNeverBecomeSuccessfulUnmergedPages(t *testing.T) {
	for _, fragment := range []string{
		`<mergeCells count="2"><mergeCell ref="A1:B1"/></mergeCells>`,
		`<mergeCells count="1"><mergeCell ref="A1:XFE1"/></mergeCells>`,
		`<mergeCells count="1"><mergeCell ref="B2:A1"/></mergeCells>`,
		`<mergeCells count="1"><foreign ref="A1:B1"/></mergeCells>`,
		`<mergeCells count="1"><mergeCell xmlns="urn:foreign" ref="A1:B1"/></mergeCells>`,
		`<mergeCells count="0"/><mergeCells count="0"/>`,
	} {
		t.Run(fragment, func(t *testing.T) {
			a, _ := printServerFixture(t, func(parts map[string]string) {
				parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<mergeCells count="1"><mergeCell ref="A1:B1"/></mergeCells>`, fragment, 1)
			})
			prepareDataRuleSheet(t, a)
			job := printStart(t, a, printOptions())
			select {
			case <-job.done:
			case <-time.After(time.Second):
				t.Fatal("invalid merge read did not finish")
			}
			if job.Status != "error" || job.Snapshot != nil || job.Error == "" {
				t.Fatal("invalid merge definition became success", job.Status, job.Error)
			}
		})
	}
}

func TestPrintPageAxisPreservesInt64BoundaryAndFullCellLength(t *testing.T) {
	axis := &printAxisOracle{values: map[int]int64{1: math.MaxInt64}}
	work := &printPageWork{ctx: context.Background()}
	items, err := work.axis(axis, "row", []printing.Part{{First: 1, Last: 1, Offset: math.MaxInt64 - 10, Length: 10}}, 1)
	if err != nil || len(items) != 1 || items[0].Size != math.MaxInt64 || items[0].Start != -(math.MaxInt64-10) || items[0].ClipStart != 0 || items[0].ClipSize != 10 {
		t.Fatal("large source length overflowed or lost its page slice", items, err)
	}
}

func TestPrintHTTPRichConditionalPaletteAndOverlappingMergeFailure(t *testing.T) {
	t.Run("common-presentation", func(t *testing.T) {
		a, _ := printServerFixture(t, func(parts map[string]string) {
			parts["xl/styles.xml"] = `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><name val="Arial"/><sz val="11"/></font><font><name val="Arial"/><sz val="10"/><b/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border/></borders><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/><xf numFmtId="4" fontId="1" fillId="0" borderId="0"/></cellXfs><dxfs count="1"><dxf><font><color rgb="FFFF0000"/></font></dxf></dxfs></styleSheet>`
			text := strings.Replace(parts["xl/worksheets/sheet1.xml"], `<c r="B2">`, `<c r="B2" s="1">`, 1)
			text = strings.Replace(text, `<is><t>001</t></is>`, `<is><r><rPr><b/></rPr><t>00</t></r><r><rPr><i/></rPr><t>1</t></r></is>`, 1)
			parts["xl/worksheets/sheet1.xml"] = strings.Replace(text, `</worksheet>`, `<conditionalFormatting sqref="B2"><cfRule type="cellIs" dxfId="0" priority="1" operator="greaterThan"><formula>0</formula></cfRule></conditionalFormatting></worksheet>`, 1)
		})
		prepareDataRuleSheet(t, a)
		job := printStart(t, a, printOptions())
		printWait(t, a, job)
		page := printPageFor(t, a, job, 0)
		actual := map[string]workbook.CellData{}
		for _, cell := range page.Content.Cells {
			if cell.Cell != nil {
				actual[cell.Ref] = *cell.Cell
			}
		}
		rows, err := job.Workbook.ReadRowsByNumbersWithOptionsContext(context.Background(), job.Index, []int{2}, 1, 2, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true, RichText: true, ConditionalFormats: true})
		if err != nil || len(rows) != 1 || len(rows[0].Cells) != 2 {
			t.Fatal(rows, err)
		}
		for _, source := range rows[0].Cells {
			if !reflect.DeepEqual(source, actual[source.Ref]) {
				t.Fatal("print packet diverged from common source/presentation contract", source, actual[source.Ref])
			}
		}
		if actual["A2"].RichText == nil || actual["B2"].Conditional == nil || !page.Content.Styles[1].Bold || page.Content.Styles[1].FontSize != 10 {
			t.Fatal("print path did not actually request rich/conditional/source style", actual, page.Content.Styles)
		}
	})
	t.Run("overlapping-source-merges", func(t *testing.T) {
		a, _ := printServerFixture(t, func(parts map[string]string) {
			parts["xl/worksheets/sheet1.xml"] = strings.Replace(parts["xl/worksheets/sheet1.xml"], `<mergeCells count="1"><mergeCell ref="A1:B1"/></mergeCells>`, `<mergeCells count="2"><mergeCell ref="A1:B1"/><mergeCell ref="A1:A2"/></mergeCells>`, 1)
		})
		prepareDataRuleSheet(t, a)
		job := printStart(t, a, printOptions())
		printWait(t, a, job)
		response := analysisRequest(t, a, "GET", "/api/printing/page?id="+job.ID+"&page=0", "")
		if response.Code < 400 || strings.Contains(response.Body.String(), `"content"`) {
			t.Fatal("overlapping merges became a successful arbitrary layout", response.Code, response.Body.String())
		}
	})
}

func TestPrintHTTPMergePerimeterReadsCoveredSourceCornersOutsideCurrentPage(t *testing.T) {
	a, file := printServerFixture(t, func(parts map[string]string) {
		parts["xl/styles.xml"] = `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><name val="Arial"/><sz val="11"/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="3"><border/><border><right style="thin"><color rgb="FFFF0000"/></right></border><border><bottom style="double"><color rgb="FF0000FF"/></bottom></border></borders><cellXfs count="3"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/><xf numFmtId="0" fontId="0" fillId="0" borderId="1"/><xf numFmtId="0" fontId="0" fillId="0" borderId="2"/></cellXfs></styleSheet>`
		text := strings.Replace(parts["xl/worksheets/sheet1.xml"], `ref="A1:B1"`, `ref="A1:B4"`, 1)
		text = strings.Replace(text, `<c r="B1"`, `<c r="B1" s="1"`, 1)
		parts["xl/worksheets/sheet1.xml"] = strings.Replace(text, `<row r="4">`, `<row r="4"><c r="A4" s="2"/>`, 1)
	})
	before := printScaleHash(t, file)
	prepareDataRuleSheet(t, a)
	job := printStart(t, a, printOptions())
	printWait(t, a, job)
	page := printPageFor(t, a, job, 0)
	count := 0
	for _, cell := range page.Content.Cells {
		if cell.Merge == nil {
			continue
		}
		count++
		if cell.Borders == nil || !strings.Contains(strings.ToLower(cell.Borders.Right), "#ff0000") || !strings.Contains(strings.ToLower(cell.Borders.Bottom), "#0000ff") || cell.Cell == nil || cell.Cell.Ref != "A1" || cell.Cell.Raw != "编号" {
			t.Fatal("source perimeter on covered/out-of-page corners was lost or moved into values", cell)
		}
	}
	if count == 0 || page.Content.Rows[len(page.Content.Rows)-1].Number >= 4 || printScaleHash(t, file) != before {
		t.Fatal("test did not exercise out-of-page source corner or source changed", count, page.Content.Rows)
	}
}
