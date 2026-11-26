package server

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/analysis"
	"excellens/internal/workbook"
)

func TestDatabaseSnapshotManagementChecksReferencesAtConfirmation(t *testing.T) {
	isolateTestStorage(t)
	wb := createDatabaseSnapshotFixture(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	key := wb.Fingerprint
	preflight := map[string]any{"key": key}
	confirm := map[string]any{"key": key, "confirm": true}
	const removal = databaseSnapshotsAPI + "/removal"
	inspect := func() string { return checkpointCall(t, app, removal, preflight, 200).Body.String() }
	blocked := func(reason string) {
		t.Helper()
		out := checkpointCall(t, app, removal, confirm, 409)
		if !strings.Contains(out.Body.String(), reason) {
			t.Fatal(out.Body.String())
		}
		if _, err := os.Stat(wb.Path); err != nil {
			t.Fatal(err)
		}
	}
	policy := snapshotPolicy{1 << 30, 90}
	checkpointCall(t, app, databaseSnapshotsAPI, map[string]any{"action": "policy", "policy": policy}, 200)
	list := decodeResponse[struct {
		Items  []workbook.DatabaseSnapshotSummary
		Policy snapshotPolicy
	}](t, featureCall(t, app, "GET", databaseSnapshotsAPI, ""))
	if len(list.Items) != 1 || list.Items[0].Source.Host != "report.example.test" || list.Items[0].Tables[0] != "audit.claims" || list.Policy != policy {
		t.Fatal(list)
	}
	manifest, err := os.ReadFile(wb.Path)
	if err != nil {
		t.Fatal(err)
	}
	if list.Items[0].Bytes != int64(len(manifest))+wb.DatabaseSnapshotSize() {
		t.Fatal("wrong directory usage", list.Items[0])
	}
	res := checkpointCall(t, app, databaseSnapshotsAPI, map[string]string{"action": "export", "key": key}, 200)
	if !bytes.Equal(res.Body.Bytes(), manifest) || fmt.Sprintf("%x", sha256.Sum256(res.Body.Bytes())) != key ||
		!strings.Contains(res.Header().Get("Content-Disposition"), key+".json") {
		t.Fatal("export changed manifest bytes")
	}
	if strings.Contains(inspect(), "打开的工作簿") {
		t.Fatal("closed snapshot is active")
	}
	// Register a source after the user saw the removal preview.
	checkpointCall(t, app, databaseSnapshotsAPI, map[string]string{"action": "open", "key": key}, 200)
	blocked("打开的工作簿")
	job, taskID := checkpointStart(t, app, "000123", "value", "current")
	checkpointWait(t, job)
	recipe := saveTestRecipe(t, app, analysis.Query{Source: "wb:claims", HeaderRow: 1, Rows: []string{"C1"}, Value: "C2", Aggregate: "sum", Precision: 2})
	app.replaceWorkbook(nil)
	blocked("审计任务")
	blocked("分析方案")
	checkpointCall(t, app, "/api/tasks/discard", taskCheckpointRequest{ID: taskID, Revision: checkpointManifest(t, taskID).Revision}, 200)
	checkpointCall(t, app, "/api/recipes", map[string]any{"action": "delete", "id": recipe.ID, "expectedRevision": 1}, 200)
	path, _ := recipesPath()
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked("无法完整核对引用")
	if err := os.WriteFile(path, good, 0600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(filepath.Dir(wb.Path), "keep.txt")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked("其他文件")
	if err := os.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ExportDir(), 0700); err != nil {
		t.Fatal(err)
	}
	result := filepath.Join(ExportDir(), "审计台账_双表核对_20261126.csv")
	if err := os.WriteFile(result, []byte("记录类别,左来源指纹,右来源指纹\n差异,"+key+",other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	blocked("导出文件")
	if err := os.Remove(result); err != nil {
		t.Fatal(err)
	}
	checkpointCall(t, app, removal, confirm, 200)
	if _, err := os.Stat(wb.Path); !os.IsNotExist(err) {
		t.Fatal("explicit deletion did not remove the capture", err)
	}
	items, err := workbook.ListDatabaseSnapshots(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
}
