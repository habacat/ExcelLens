package server

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"testing"
	"time"
)

// A referenced sheet need not have been opened by the user before its exact
// cached value and original formula can be inspected. This is a regression
// test for the formula-bar lazy index -> exact-cell request sequence.
func TestFormulaInspectorLazyCrossSheetReference(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "references.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()

	res := featureCall(t, app, "POST", "/api/index/start", `{"sheet":"Second"}`)
	var start struct {
		Ready bool   `json:"ready"`
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if !start.Ready {
		if start.JobID == "" {
			t.Fatal("missing index job")
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			status := featureCall(t, app, "GET", "/api/index/status?id="+url.QueryEscape(start.JobID), "")
			var task struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(status.Body.Bytes(), &task); err != nil {
				t.Fatal(err)
			}
			if task.Status == "done" {
				break
			}
			if task.Status == "error" || task.Status == "cancelled" {
				t.Fatalf("index failed: %+v", task)
			}
			if time.Now().After(deadline) {
				t.Fatal("index exceeded deadline")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	response := featureCall(t, app, "GET", "/api/cell?sheet=Second&ref=A1", "")
	var result struct {
		Sheet string `json:"sheet"`
		Cell  struct {
			Ref     string `json:"ref"`
			Display string `json:"display"`
			Formula string `json:"formula"`
		} `json:"cell"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Sheet != "Second" || result.Cell.Ref != "A1" {
		t.Fatalf("cross-sheet lookup returned wrong cell: %+v", result)
	}
}
