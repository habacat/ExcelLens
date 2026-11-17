package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/workbook"
)

func TestBookmarksStayOutsideCacheAndFollowWorkbookContent(t *testing.T) {
	isolateTestStorage(t)
	original := filepath.Join(t.TempDir(), "source.xlsx")
	renamed := filepath.Join(t.TempDir(), "completely-different-name.xlsx")
	writeMultiSheetWorkbook(t, original)
	originalContent, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(renamed, originalContent, 0600); err != nil {
		t.Fatal(err)
	}
	app := prepareFeatureApp(t, original)
	var saved bookmarkStore
	result := featureCall(t, app, "POST", "/api/bookmarks", `{"action":"save","sheet":"Second","ref":"C1","tag":"异常","note":"跨Sheet引用有待复核","value":"2","formula":"='First'!$A$1+1"}`)
	if err := json.Unmarshal(result.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Items) != 1 || saved.Items[0].Tag != "异常" {
		t.Fatalf("save returned unexpected bookmarks: %+v", saved)
	}
	savedID := saved.Items[0].ID
	sourceFingerprint := app.currentWorkbook().Fingerprint
	app.Close()
	// Cached row/search indexes may be deleted without losing audit notes.
	cache := workbook.CacheRoot()
	if err := os.RemoveAll(cache); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workbook.DataRoot(), "annotations", sourceFingerprint+".json")); err != nil {
		t.Fatal(err)
	}
	app2 := prepareFeatureApp(t, renamed)
	defer app2.Close()
	if app2.currentWorkbook().Fingerprint != sourceFingerprint {
		t.Fatal("renamed identical file fingerprint changed")
	}
	var restored bookmarkStore
	if err := json.Unmarshal(featureCall(t, app2, "GET", "/api/bookmarks", "").Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if len(restored.Items) != 1 || restored.Items[0].ID != savedID || restored.Items[0].Note != "跨Sheet引用有待复核" {
		t.Fatalf("notes not reused across renamed copy: %+v", restored)
	}
	featureCall(t, app2, "POST", "/api/bookmarks", `{"action":"save","id":"`+savedID+`","sheet":"Second","ref":"C1","tag":"已核实","note":"复核已完成"}`)
	var updated bookmarkStore
	_ = json.Unmarshal(featureCall(t, app2, "GET", "/api/bookmarks", "").Body.Bytes(), &updated)
	if len(updated.Items) != 1 || updated.Items[0].Tag != "已核实" || updated.Items[0].Note != "复核已完成" {
		t.Fatalf("update failed: %+v", updated)
	}
	featureCall(t, app2, "POST", "/api/bookmarks", `{"action":"delete","id":"`+savedID+`"}`)
	var removed bookmarkStore
	_ = json.Unmarshal(featureCall(t, app2, "GET", "/api/bookmarks", "").Body.Bytes(), &removed)
	if len(removed.Items) != 0 {
		t.Fatalf("delete failed: %+v", removed)
	}
	after, err := os.ReadFile(original)
	if err != nil || !bytes.Equal(after, originalContent) {
		t.Fatal("source workbook was modified")
	}
}

func TestBookmarksRejectUnknownSheetOrExcessiveNotes(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "f.xlsx")
	writeMultiSheetWorkbook(t, file)
	app := prepareFeatureApp(t, file)
	defer app.Close()
	for _, body := range []string{
		`{"action":"save","sheet":"NotExist","ref":"A1","note":"x"}`,
		`{"action":"save","sheet":"First","ref":"A0","note":"x"}`,
		`{"action":"save","sheet":"First","ref":"A1","note":"` + strings.Repeat("a", 801) + `"}`,
	} {
		req := httptest.NewRequest("POST", "/api/bookmarks", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("X-ExcelLens-Token", app.token)
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, req)
		if response.Code != 400 {
			t.Fatalf("validation did not reject input %d", response.Code)
		}
	}
}
