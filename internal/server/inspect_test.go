package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectEndpointAndTokenGuard(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "inspection.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := prepareFeatureApp(t, file)
	defer a.Close()
	check := func(mode string, want int) {
		t.Helper()
		request := `{"sheet":"First","mode":"` + mode + `","start":"A1","end":"D4","limit":100}`
		res := featureCall(t, a, "POST", "/api/inspect", request)
		var data struct {
			Matched int `json:"matched"`
			Matches []struct {
				Ref string `json:"ref"`
			} `json:"matches"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if data.Matched != want {
			t.Fatalf("mode %s: got %d want %d", mode, data.Matched, want)
		}
	}
	check("formulas", 1)
	check("constants", 1)
	check("blanks", 14)
	request := httptest.NewRequest(http.MethodPost, "/api/inspect", strings.NewReader(`{"sheet":"First","mode":"formulas","start":"A1","end":"D4"}`))
	request = localTestConnection(request)
	res := httptest.NewRecorder()
	a.Handler().ServeHTTP(res, request)
	if res.Code != http.StatusForbidden {
		t.Fatalf("missing token returned %d", res.Code)
	}
}
