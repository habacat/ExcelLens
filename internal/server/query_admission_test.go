package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"excellens/internal/sourceview"
)

func TestQueuedQueriesLeaveCacheMaintenanceAvailable(t *testing.T) {
	check := func(t *testing.T, a *App, handler http.HandlerFunc, endpoint string, body any) {
		t.Helper()
		method := http.MethodGet
		var payload []byte
		if body != nil {
			method = http.MethodPost
			var err error
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		synctest.Test(t, func(t *testing.T) {
			previous := a.querySem
			a.querySem = make(chan struct{}, 1)
			defer func() { a.querySem = previous }()
			a.querySem <- struct{}{}
			request := httptest.NewRequest(method, endpoint, bytes.NewReader(payload))
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler(response, request)
			}()
			synctest.Wait()
			select {
			case <-done:
				t.Errorf("request did not wait for its query permit: %d %s", response.Code, response.Body)
			default:
			}
			// A writer must be able to run even while the shared query pool
			// is full. Otherwise its pending lock can block the permit owners.
			if a.cacheMu.TryLock() {
				a.cacheMu.Unlock()
			} else {
				t.Error("queued query holds the cache read lock")
			}
			<-a.querySem
			<-done
			if response.Code != http.StatusOK || response.Body.Len() == 0 {
				t.Errorf("released query returned %d: %s", response.Code, response.Body)
			}
		})
	}

	a, _ := recordFixture(t, 12, "")
	rec := recordSource(t, a, "First", rowViewRequest{})
	coords := analysisSelectionCoordinates{ViewID: rec.ViewID}
	for _, tc := range []struct {
		name, endpoint string
		handler        http.HandlerFunc
		body           any
	}{
		{"record", "/api/record", a.handleRecord, rec},
		{"profile", "/api/profile", a.handleProfile, profileRequest{Sheet: rec.Sheet, Start: "A2", End: "B2", analysisSelectionCoordinates: coords}},
		{"cell explanation", "/api/cell/explain", a.handleCellExplanation, cellExplanationRequest{
			WorkbookKey: rec.WorkbookKey, Book: rec.Book, Sheet: rec.Sheet, ViewID: rec.ViewID, Ref: "A2",
		}},
		{"cold navigation", "/api/navigate?sheet=First&ref=A1&direction=down", a.handleNavigate, nil},
		{"analysis restore", "/api/analysis/source/restore", a.handleAnalysisSourceRestore, map[string]any{
			"workbookKey": rec.WorkbookKey, "book": rec.Book, "source": "wb:First",
			"sourceScope": sourceview.Scope{Version: sourceview.ScopeVersion, Fingerprint: rec.WorkbookKey,
				Mode: "filtered", Filter: &sourceview.FilterSpec{HeaderRow: 1, SortColumn: 2}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) { check(t, a, tc.handler, tc.endpoint, tc.body) })
	}

	t.Run("print page", func(t *testing.T) {
		printApp, _ := printServerFixture(t, nil)
		prepareDataRuleSheet(t, printApp)
		job := printStart(t, printApp, printOptions())
		printWait(t, printApp, job)
		check(t, printApp, printApp.handlePrintPage, "/api/printing/page?id="+job.ID+"&page=0", nil)
	})
}
