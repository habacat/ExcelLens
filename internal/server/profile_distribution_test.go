package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func profileSource(t *testing.T, a *App, sheet, start, end string, view rowViewRequest) profileRequest {
	t.Helper()
	r := recordSource(t, a, sheet, view)
	return profileRequest{WorkbookKey: r.WorkbookKey, Book: r.Book, Sheet: sheet, BindingViewID: r.ViewID, BindingFilterID: r.FilterID, FilterID: r.FilterID, Start: start, End: end, IncludeDistribution: true}
}
func profileCall(t *testing.T, a *App, req profileRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	return rowViewCall(t, a, "POST", "/api/profile", string(data))
}
func profileOK(t *testing.T, a *App, req profileRequest) profileReport {
	t.Helper()
	r := profileCall(t, a, req)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	return decodeResponse[profileReport](t, r)
}

func assertProfileDistribution(t *testing.T, report profileReport, raw []string) {
	t.Helper()
	values := make([]*big.Rat, len(raw))
	sum := new(big.Rat)
	for at, text := range raw {
		n, ok := new(big.Rat).SetString(text)
		if !ok {
			t.Fatal(text)
		}
		values[at] = n
		sum.Add(sum, n)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Cmp(values[j]) < 0 })
	d := report.Distribution
	if d == nil || d.Method != "exact" || d.Definition != "inclusive-linear" || d.Count != int64(len(raw)) || report.Number != int64(len(raw)) {
		t.Fatal("distribution count/state", report)
	}
	got, ok := new(big.Rat).SetString(report.SumExact)
	if !ok || got.Cmp(sum) != 0 {
		t.Fatal("inexact total", report.SumExact, sum)
	}
	if len(raw) == 0 {
		if len(d.Quantiles) != 0 || len(d.Bins) != 0 {
			t.Fatal(d)
		}
		return
	}
	for _, q := range d.Quantiles {
		position := (len(values) - 1) * q.Percent
		lo, rem := position/100, position%100
		hi := lo
		if rem > 0 {
			hi++
		}
		want := new(big.Rat).Add(new(big.Rat).Mul(values[lo], big.NewRat(int64(100-rem), 100)), new(big.Rat).Mul(values[hi], big.NewRat(int64(rem), 100)))
		got, ok := new(big.Rat).SetString(q.Value)
		if !ok || got.Cmp(want) != 0 {
			t.Fatal(q, want)
		}
	}
	var total int64
	for _, bin := range d.Bins {
		lower, _ := new(big.Rat).SetString(bin.Lower)
		upper, _ := new(big.Rat).SetString(bin.Upper)
		want := int64(0)
		for _, value := range values {
			if value.Cmp(lower) >= 0 && (value.Cmp(upper) < 0 || bin.UpperInclusive && value.Cmp(upper) == 0) {
				want++
			}
		}
		if bin.Count != want {
			t.Fatal("wrong exact bin", bin, want)
		}
		total += bin.Count
	}
	if total != int64(len(raw)) {
		t.Fatal("incomplete bins", total)
	}
}

func TestProfileDistributionExactExtremesBlanksAndOriginalTypeEligibility(t *testing.T) {
	a, file := quickFixture(t)
	before := printScaleHash(t, file)
	req := profileSource(t, a, "First", "A2", "B6", rowViewRequest{})
	r := profileOK(t, a, req)
	assertProfileDistribution(t, r, []string{"9007199254740993.31", "9007199254740993.30", "-0.01", "0.01", "0", "1e-308", "0"})
	if r.Total != 10 || r.Blank != 3 || r.NonEmpty != 7 || r.WorkbookKey != req.WorkbookKey || r.Book != req.Book || r.BindingViewID != req.BindingViewID {
		t.Fatal(r)
	}
	req.Start, req.End = "C1", "C6"
	r = profileOK(t, a, req)
	assertProfileDistribution(t, r, []string{"0.5"})
	if r.Text != 2 {
		t.Fatal("native text converted", r)
	}
	req.Start, req.End = "D1", "F6"
	r = profileOK(t, a, req)
	assertProfileDistribution(t, r, nil)
	if r.Errors != 1 || r.CacheMissing != 1 || r.Number != 0 {
		t.Fatal(r)
	}
	req.Start, req.End, req.IncludeDistribution = "A2", "B6", false
	r = profileOK(t, a, req)
	if r.Distribution != nil || r.Number != 7 {
		t.Fatal("unrequested distribution", r)
	}
	if printScaleHash(t, file) != before {
		t.Fatal("source changed")
	}
}

func TestProfileDistributionManualSourceFilterAndSortedFrozenVisibleScopesStayDistinct(t *testing.T) {
	isolateTestStorage(t)
	a, _ := rowViewFixture(t)
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	idx := wb.GetCachedIndex(meta.Path)
	a.filterResults["profile-order"] = &FilterResult{ID: "profile-order", SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 5, 1, 0, 4, 3, 2)}
	req := profileSource(t, a, "First", "B2", "B7", rowViewRequest{FilterID: "profile-order"})
	manual := profileOK(t, a, req)
	if manual.Total != 5 {
		t.Fatal("typed source range lost full matches", manual)
	}
	req.Start, req.End = "B7", "B4"
	req.analysisSelectionCoordinates = analysisSelectionCoordinates{ViewID: req.BindingViewID, VisualStart: 1, VisualEnd: 2, ExcludeThroughRow: 1}
	visible := profileOK(t, a, req)
	if visible.Total != 2 || visible.Number != 2 {
		t.Fatal(visible)
	}
	stats := decodeResponse[selectionStatResult](t, featureCall(t, a, "POST", "/api/selection/stats", `{"sheet":"First","filterId":"profile-order","viewId":"`+req.BindingViewID+`","excludeThroughRow":1,"ranges":[{"start":"B7","end":"B4","visualStart":1,"visualEnd":2}]}`))
	if visible.Sum != stats.Sum || visible.Total != stats.Total {
		t.Fatal("visible profile changed selection", visible, stats)
	}
	// Binding follows the current view even for an explicit source query that
	// intentionally ignores its filter/visibility.
	req.analysisSelectionCoordinates = analysisSelectionCoordinates{}
	req.FilterID = ""
	req.Start, req.End = "B1", "B7"
	full := profileOK(t, a, req)
	if full.Total != 7 {
		t.Fatal(full)
	}
}

func TestProfileDistributionRejectsMalformedExpiredAndSameContentDifferentPath(t *testing.T) {
	a, file := quickFixture(t)
	req := profileSource(t, a, "First", "A2", "B6", rowViewRequest{})
	for _, change := range []func(*profileRequest){func(r *profileRequest) { r.Book = "other" }, func(r *profileRequest) { r.WorkbookKey = "other" }, func(r *profileRequest) { r.BindingViewID = "expired" }, func(r *profileRequest) { r.BindingFilterID = "foreign" }, func(r *profileRequest) { r.ViewID = "different" }} {
		copy := req
		change(&copy)
		if r := profileCall(t, a, copy); r.Code != 409 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	for _, change := range []func(*profileRequest){func(r *profileRequest) { r.Book = "" }, func(r *profileRequest) { r.WorkbookKey = "" }, func(r *profileRequest) { r.BindingViewID = "" }, func(r *profileRequest) { r.Start = "A0" }, func(r *profileRequest) { r.ExcludeThroughRow = 31 }} {
		copy := req
		change(&copy)
		if r := profileCall(t, a, copy); r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	data, _ := json.Marshal(req)
	for _, body := range []string{string(data) + ` {}`, strings.TrimSuffix(string(data), "}") + `,"unknown":1}`, strings.Repeat(" ", formats.ProfileRequestBytes+1)} {
		if r := rowViewCall(t, a, "POST", "/api/profile", body); r.Code != 400 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := rowViewCall(t, a, "GET", "/api/profile", ""); r.Code != 405 {
		t.Fatal(r.Code)
	}
	original, _ := os.ReadFile(file)
	copy := filepath.Join(t.TempDir(), "same-content.xlsx")
	if err := os.WriteFile(copy, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(copy); err != nil {
		t.Fatal(err)
	}
	if r := profileCall(t, a, req); r.Code != 409 {
		t.Fatal(r.Code)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("First")
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	req = profileSource(t, a, "First", "A2", "B6", rowViewRequest{})
	wb.ClearIndexes()
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := profileCall(t, a, req); r.Code != 409 {
		t.Fatal("replacement index accepted", r.Code)
	}
	req = profileSource(t, a, "First", "A2", "B6", rowViewRequest{})
	if err := os.WriteFile(copy, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := profileCall(t, a, req); r.Code != 409 {
		t.Fatal("changed source published", r.Code)
	}
}

func TestProfileDistributionCapacityAndTruncatedFrequencyNeverBecomePartialExactStatistics(t *testing.T) {
	idx := &workbook.SheetIndex{XMLPath: filepath.Join(t.TempDir(), "absent.xml"), XMLSize: formats.ProfileSourceRowBytes + 1, RowIndex: workbook.NewRowIndex([]workbook.RowOffset{{Number: 2, End: formats.ProfileSourceRowBytes + 1}})}
	rows := iter.Seq[workbook.SourceRowReference](func(yield func(workbook.SourceRowReference) bool) {
		yield(workbook.SourceRowReference{Number: 2, Position: 0})
	})
	_, err := readProfile(context.Background(), &workbook.Workbook{}, idx, profileRequest{}, rows, 1, 1, 1)
	if !errors.Is(err, errProfileCapacity) {
		t.Fatal("oversize reached IO", err)
	}
	a, _ := recordFixture(t, 10, strings.Repeat("<", 400_000))
	req := profileSource(t, a, "First", "I2", "I2", rowViewRequest{})
	if r := profileCall(t, a, req); r.Code != 413 || strings.Contains(r.Body.String(), `"distribution"`) {
		t.Fatal(r.Code, r.Body.String())
	}
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "many.csv")
	var csv strings.Builder
	for at := 0; at <= formats.ProfileDistinct; at++ {
		fmt.Fprintf(&csv, "value%05d\n", at)
	}
	if err := os.WriteFile(file, []byte(csv.String()), 0600); err != nil {
		t.Fatal(err)
	}
	b, meta := recordOpen(t, file)
	req = profileSource(t, b, meta.Name, "A1", "A10001", rowViewRequest{})
	r := profileOK(t, b, req)
	if !r.UniqueTruncated || r.Unique != formats.ProfileDistinct || r.Number != 0 || r.Total != 10001 || r.Warning == "" {
		t.Fatal(r)
	}
	assertProfileDistribution(t, r, nil)
}

func TestProfileDistributionQueuedCancellationEvictionAndMaintenanceReleasePins(t *testing.T) {
	for _, kind := range []string{"cancel", "evict", "maintenance"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := quickFixture(t)
			req := profileSource(t, a, "First", "A2", "B6", rowViewRequest{})
			wb := a.currentWorkbook()
			a.sourceMu.Lock()
			baseline := a.sourcePins[wb]
			a.sourceMu.Unlock()
			for at := 0; at < cap(a.querySem); at++ {
				a.querySem <- struct{}{}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			data, _ := json.Marshal(req)
			request := httptest.NewRequest("POST", "/api/profile", strings.NewReader(string(data))).WithContext(ctx)
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); a.handleProfile(response, request) }()
			deadline := time.Now().Add(2 * time.Second)
			for {
				a.sourceMu.Lock()
				pins := a.sourcePins[wb]
				a.sourceMu.Unlock()
				if pins > baseline {
					break
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatal("not pinned before queue")
				}
				time.Sleep(time.Millisecond)
			}
			switch kind {
			case "cancel":
				cancel()
			case "evict":
				a.mu.Lock()
				view := a.rowViews[req.BindingViewID]
				delete(a.rowViews, req.BindingViewID)
				view.close()
				a.mu.Unlock()
			case "maintenance":
				a.mu.Lock()
				a.maintenance = true
				a.mu.Unlock()
			}
			for at := 0; at < cap(a.querySem); at++ {
				<-a.querySem
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not exit")
			}
			a.mu.Lock()
			a.maintenance = false
			a.mu.Unlock()
			if kind == "cancel" && response.Body.Len() != 0 || kind != "cancel" && response.Code != 409 {
				t.Fatal(kind, response.Code, response.Body.String())
			}
			a.sourceMu.Lock()
			pins := a.sourcePins[wb]
			a.sourceMu.Unlock()
			expected := baseline
			if kind == "evict" {
				expected--
			}
			if pins != expected {
				t.Fatal("pin leak", pins, expected)
			}
		})
	}
}
