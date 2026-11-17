package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalysisSelectionMatchesQuickStatsAfterSortingAndFreezing(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "selection.xlsx")
	writeClipboardTestWorkbook(t, file)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	meta, _ := wb.FindSheet("Data")
	if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
	a.filterResults["f"] = &FilterResult{SheetPath: meta.Path, Sorted: true, Rows: rowOrderFixture(t, wb.GetCachedIndex(meta.Path).RowCount(), 3, 0, 1, 2)}
	for _, tc := range []struct {
		start, end string
		v1, v2     int
		sum        string
		total      int64
	}{
		{"B4", "B2", 1, 2, "6", 2},
		{"B2", "B4", 2, 1, "6", 2},
		{"B2", "B1", 2, 1, "7", 3},
	} {
		fields := fmt.Sprintf(`"sheet":"Data","start":%q,"end":%q,"filterId":"f","visualStart":%d,"visualEnd":%d,"excludeThroughRow":1`, tc.start, tc.end, tc.v1, tc.v2)
		profile := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", "{"+fields+"}"))
		if profile.Sum != tc.sum || profile.Total != tc.total {
			t.Fatalf("profile lost visual selection: %+v", profile)
		}
		quickBody := fmt.Sprintf(`{"sheet":"Data","filterId":"f","excludeThroughRow":1,"ranges":[{"start":%q,"end":%q,"visualStart":%d,"visualEnd":%d}]}`, tc.start, tc.end, tc.v1, tc.v2)
		quick := decodeResponse[selectionStatResult](t, featureCall(t, a, "POST", "/api/selection/stats", quickBody))
		if quick.Sum != profile.Sum || quick.Total != profile.Total {
			t.Fatalf("profile/quick disagree: %+v / %+v", profile, quick)
		}
		var inspection struct {
			Matched int `json:"matched"`
		}
		response := featureCall(t, a, "POST", "/api/inspect", "{"+fields+`,"mode":"formulas"}`)
		if err := json.Unmarshal(response.Body.Bytes(), &inspection); err != nil || inspection.Matched != 0 {
			t.Fatalf("inspected unselected formula at B3: %s %v", response.Body.String(), err)
		}
	}
	// Typed addresses remain explicit source ranges intersected with the filter.
	manual := decodeResponse[profileReport](t, featureCall(t, a, "POST", "/api/profile", `{"sheet":"Data","start":"B2","end":"B4","filterId":"f"}`))
	if manual.Sum != "9" || manual.Total != 3 {
		t.Fatal("typed source range changed", manual)
	}
	for _, endpoint := range []string{"/api/profile", "/api/inspect"} {
		body := `{"sheet":"Data","start":"B2","end":"B4","visualStart":1,"visualEnd":2,"mode":"formulas"}`
		if endpoint == "/api/profile" {
			body = strings.ReplaceAll(body, `,"mode":"formulas"`, "")
		}
		r := httptest.NewRequest("POST", endpoint, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("X-ExcelLens-Token", a.token)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code == http.StatusOK {
			t.Fatal("accepted visual selection without a filter", endpoint)
		}
	}
}
