package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSavedReportBindsExplicitWorkbookAndPathIdentity(t *testing.T) {
	a, file := setupProApp(t)
	original := a.currentWorkbook()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	clone := filepath.Join(t.TempDir(), "identical-copy.xlsx")
	if err := os.WriteFile(clone, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(clone); err != nil {
		t.Fatal(err)
	}
	current := a.currentWorkbook()
	if current.Fingerprint != original.Fingerprint || sourceBookID(current) == sourceBookID(original) {
		t.Fatal("fixture did not retain content while changing source identity")
	}
	for _, identity := range []map[string]string{{"workbookKey": "different", "book": sourceBookID(current)}, {"workbookKey": current.Fingerprint, "book": sourceBookID(original)}} {
		body := map[string]any{"name": "报告", "query": map[string]any{"source": "wb:Invest", "aggregate": "count"}, "workbookKey": identity["workbookKey"], "book": identity["book"]}
		raw, _ := json.Marshal(body)
		response := analysisRequest(t, a, "POST", "/api/analysis/report", string(raw))
		if response.Code != 409 {
			t.Fatal("obsolete report saved into current workspace", response.Code, response.Body.String())
		}
	}
	workspace, err := a.loadAnalysis(current)
	if err != nil || len(workspace.Reports) != 0 {
		t.Fatal("rejected requests changed saved reports", workspace.Reports, err)
	}
	body, _ := json.Marshal(map[string]any{"name": "报告", "query": map[string]any{"source": "wb:Invest", "aggregate": "count"}, "workbookKey": current.Fingerprint, "book": sourceBookID(current)})
	if response := analysisRequest(t, a, "POST", "/api/analysis/report", string(body)); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
}
