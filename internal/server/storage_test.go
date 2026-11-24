package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/editdoc"

	"excellens/internal/localstore"
	"excellens/internal/workbook"
)

func TestDataManagerMigratesRealSettingsAndRestoresThem(t *testing.T) {
	isolateTestStorage(t)
	src := filepath.Join(t.TempDir(), "source.xlsx")
	writeMultiSheetWorkbook(t, src)
	a := prepareFeatureApp(t, src)
	defer a.Close()
	fp := a.currentWorkbook().Fingerprint
	roots, err := dataRoots()
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"layouts": `{"version":1,"sheets":{"First":{"rows":{"2":44}}}}`, "annotations": `{"version":1,"items":[{"id":"old","sheet":"First","ref":"A1","note":"保留的核查记录"}]}`}
	for kind, data := range files {
		dir := filepath.Join(roots.Local, kind)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fp+".json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	notes := decodeResponse[bookmarkStore](t, featureCall(t, a, "GET", "/api/bookmarks", ""))
	if len(notes.Items) != 1 || notes.Items[0].Note != "保留的核查记录" {
		t.Fatal(notes)
	}
	waitStorageTransfer(t, a, `{"action":"migrate"}`)
	layout := decodeResponse[sheetLayout](t, featureCall(t, a, "GET", "/api/layout?sheet=First", ""))
	if layout.Rows["2"] != 44 {
		t.Fatal(layout)
	}
	featureCall(t, a, "POST", "/api/layout", `{"sheet":"First","kind":"row","index":2,"size":55}`)
	waitStorageTransfer(t, a, `{"action":"migrate"}`)
	if b, _ := os.ReadFile(filepath.Join(roots.Local, "layouts", fp+".json")); string(b) != files["layouts"] {
		t.Fatal("old layout overwritten")
	}
	list := decodeResponse[struct{ Groups []localstore.Usage }](t, featureCall(t, a, "GET", "/api/storage", ""))
	if len(list.Groups) < 8 {
		t.Fatal(list)
	}
	for _, row := range list.Groups {
		if row.Error != "" {
			t.Fatalf("partial inventory %+v", row)
		}
	}
	upload := filepath.Join(workbook.UploadRoot(), "ExcelLens-upload-unused.csv")
	if err := os.MkdirAll(filepath.Dir(upload), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upload, []byte("a,b"), 0600); err != nil {
		t.Fatal(err)
	}
	backupParent := t.TempDir()
	request, _ := json.Marshal(map[string]string{"action": "backup", "directory": backupParent})
	waitStorageTransfer(t, a, string(request))
	featureCall(t, a, "POST", "/api/storage", `{"action":"clear-cache"}`)
	if _, err := os.Stat(upload); err != nil {
		t.Fatal("cache cleanup removed upload", err)
	}
	featureCall(t, a, "POST", "/api/storage", `{"action":"clear-uploads"}`)
	if _, err := os.Stat(upload); !os.IsNotExist(err) {
		t.Fatal("unused upload was not cleaned")
	}
	folders, err := os.ReadDir(backupParent)
	if err != nil || len(folders) != 1 {
		t.Fatal(folders, err)
	}
	target := localstore.Roots{Config: t.TempDir(), Local: t.TempDir()}
	if err := target.Restore(t.Context(), filepath.Join(backupParent, folders[0].Name())); err != nil {
		t.Fatal(err)
	}
	got, err := readLayouts(filepath.Join(target.Config, "layouts", fp+".json"))
	if err != nil || got.Sheets["First"].Rows["2"] != 55 {
		t.Fatal(got, err)
	}
	if b, err := os.ReadFile(filepath.Join(target.Local, "temp", filepath.Base(upload))); err != nil || string(b) != "a,b" {
		t.Fatal(string(b), err)
	}
}

// Exercise the old edit key and cursor after both data roots and the original
// paths change. A preview must not authorize using the candidate.
func TestStoragePageRestoresConfirmedEditAndProtectsTaskUpload(t *testing.T) {
	isolateTestStorage(t)
	first, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	path := filepath.Join(t.TempDir(), "audit.csv")
	if err := os.WriteFile(path, []byte("编号,金额\n001,42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := first.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	state := decodeResponse[editState](t, editHTTP(t, first, "start", editRequest{Book: sourceBookID(first.currentWorkbook())}, 200))
	ref := editdoc.Address{Sheet: state.Sheets[0].Name, Row: 2, Col: 1}
	doc := state.Document.Key
	op := editdoc.Operation{Revision: 1, Request: strings.Repeat("1", 32), Action: "change", Changes: []editdoc.Change{{Address: ref, Value: editdoc.Value{Kind: "text", Raw: "009"}}}}
	editHTTP(t, first, "commit", editRequest{Document: doc, Operation: op}, 200)

	uploads := workbook.UploadRoot()
	if err := os.MkdirAll(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(uploads, "ExcelLens-upload-test.csv")
	if err := os.WriteFile(source, []byte("name,amount\nneedle,17\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := first.OpenPath(source); err != nil {
		t.Fatal(err)
	}
	job, taskID := checkpointStart(t, first, "needle", "value", "current")
	checkpointWait(t, job)
	if job.Status != "done" {
		t.Fatal(job.Status, job.Error)
	}
	first.replaceWorkbook(nil)
	if _, err := first.ClearUnusedUploads(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("saved task lost its upload", err)
	}
	roots, err := dataRoots()
	if err != nil {
		t.Fatal(err)
	}
	folder, err := roots.Backup(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first.Close()

	isolateTestStorage(t)
	reopened, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	req, _ := json.Marshal(map[string]string{"action": "restore", "directory": folder})
	waitStorageTransfer(t, reopened, string(req))
	newBase := filepath.Join(t.TempDir(), "底稿.csv")
	if err := os.Rename(path, newBase); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	newUpload := filepath.Join(workbook.UploadRoot(), filepath.Base(source))
	if _, err := reopened.ClearUnusedUploads(); err != nil {
		t.Fatal(err)
	}
	listed := decodeResponse[struct{ Items []relocationItem }](t, featureCall(t, reopened, "GET", "/api/storage/sources", ""))
	if len(listed.Items) != 2 {
		t.Fatal(listed.Items)
	}
	for _, item := range listed.Items {
		candidate := newBase
		if cachePathKey(item.Original) == cachePathKey(source) {
			candidate = newUpload
			if cachePathKey(item.Candidate) != cachePathKey(newUpload) {
				t.Fatal(item)
			}
		}
		wrong := newBase
		if wrong == candidate {
			wrong = newUpload
		}
		if _, err := reopened.verifyLocation(t.Context(), item.Book, item.Path, wrong, true); err == nil {
			t.Fatal("different bytes accepted")
		}
		request := map[string]any{"book": item.Book, "previous": item.Path, "path": candidate}
		post := func() storageJob {
			body, _ := json.Marshal(request)
			job := startStorageAt(t, reopened, "/api/storage/sources", string(body))
			return waitForStorage(t, reopened, job)
		}
		if preview := post(); cachePathKey(preview.Candidate) != cachePathKey(candidate) {
			t.Fatal(preview)
		}
		entries, err := relocationSources(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, saved := range entries {
			if saved.Book == item.Book && (len(saved.Moves) != 0 || saved.Path != item.Path) {
				t.Fatal("preview modified source path", saved)
			}
		}
		request["confirm"] = true
		if confirmed := post(); confirmed.Message != "已更新来源位置" {
			t.Fatal(confirmed)
		}
	}
	manifest := checkpointManifest(t, taskID)
	checkpointCall(t, reopened, "/api/tasks/open", taskCheckpointRequest{ID: taskID, Revision: manifest.Revision}, 200)
	if cachePathKey(reopened.currentWorkbook().Path) != cachePathKey(newUpload) {
		t.Fatal("opened wrong upload")
	}
	resumed := checkpointResume(t, reopened, manifest)
	checkpointWait(t, resumed)
	if resumed.Status != "done" || len(resumed.Matches) != 1 {
		t.Fatal(resumed.Status, resumed.Error, resumed.Matches)
	}
	if m := checkpointManifest(t, taskID); m.ID != taskID || !m.Complete {
		t.Fatal("restored checkpoint", m)
	}
	cell := decodeResponse[struct{ Base, Value editdoc.Value }](t, editHTTP(t, reopened, "read", editRequest{Document: doc, Revision: 2, Sheet: ref.Sheet, Ref: "A2"}, 200))
	if cell.Base.Raw != "001" || cell.Value.Raw != "009" {
		t.Fatal(cell)
	}
}

func TestCacheActionsAndBackupAdoptUnvisitedLegacyFreeze(t *testing.T) {
	for _, action := range []string{"current", "history", "all", "backup"} {
		t.Run(action, func(t *testing.T) {
			isolateTestStorage(t)
			source := filepath.Join(t.TempDir(), "layout.xlsx")
			writeMultiSheetWorkbook(t, source)
			app := prepareFeatureApp(t, source)
			defer app.Close()
			wb := app.currentWorkbook()
			if err := os.WriteFile(filepath.Join(wb.CacheDir, "view.json"), []byte(`{"version":1,"sheets":{"First":{"rows":2,"cols":1}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			roots, err := dataRoots()
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "current":
				featureCall(t, app, "POST", "/api/cache/clear", "")
			case "history":
				app.replaceWorkbook(nil)
				_, err = app.ClearUnusedCaches()
			case "all":
				_, err = app.ClearAllCaches()
			case "backup":
				var folder string
				folder, err = roots.Backup(t.Context(), t.TempDir())
				if err == nil {
					roots = localstore.Roots{Config: t.TempDir(), Local: t.TempDir()}
					err = roots.Restore(t.Context(), folder)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := readViews(filepath.Join(roots.Config, "frozen-panes", wb.Fingerprint+".json"))
			if err != nil || view.Sheets["First"] != (freezeSettings{Rows: 2, Cols: 1}) {
				t.Fatal(view, err)
			}
		})
	}
}

func startStorageTransfer(t *testing.T, a *App, body string) storageJob {
	t.Helper()
	return startStorageAt(t, a, "/api/storage", body)
}
func startStorageAt(t *testing.T, a *App, path, body string) storageJob {
	t.Helper()
	req := localTestConnection(httptest.NewRequest("POST", path, strings.NewReader(body)))
	req.Header.Set("X-ExcelLens-Token", a.token)
	res := httptest.NewRecorder()
	a.Handler().ServeHTTP(res, req)
	if res.Code != 202 {
		t.Fatal(res.Code, res.Body.String())
	}
	var job storageJob
	if err := json.Unmarshal(res.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	return job
}
func waitStorageTransfer(t *testing.T, a *App, body string) storageJob {
	t.Helper()
	return waitForStorage(t, a, startStorageTransfer(t, a, body))
}
func waitForStorage(t *testing.T, a *App, job storageJob) storageJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job = decodeResponse[storageJob](t, featureCall(t, a, "GET", "/api/storage/jobs?id="+job.ID, ""))
		if job.State == "done" {
			return job
		}
		if job.State != "running" {
			t.Fatal(job)
		}
		time.Sleep(time.Millisecond * 10)
	}
	t.Fatal("transfer timeout")
	return job
}

func TestLegacyFilterAndFrozenExportMigrateAcrossProfiles(t *testing.T) {
	isolateTestStorage(t)
	source := filterCheckpointCSV(t, 32)
	a := checkpointOpen(t, source)
	wb := a.currentWorkbook()
	old := filterCheckpointSeed(t, a, filterCheckpointRequest(false), []int{0, 1}, nil, 2, false)
	req := exportRequest{Scope: "sheet", Sheet: wb.Sheets[0].Name, Format: "csv", Mode: "values"}
	selections, free := exportCheckpointPrepareOwned(t, a, req)
	expected := exportCheckpointOutput(t, wb, selections, req.options())
	store, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(t.Context(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	export := lease.Snapshot()
	lease.Close()
	store.Close()
	free()
	parent := t.TempDir()
	payload, _ := json.Marshal(map[string]string{"action": "backup", "directory": parent})
	if completed := waitStorageTransfer(t, a, string(payload)); strings.Contains(completed.Message, "缺少") {
		t.Fatal(completed)
	}
	entries, _ := os.ReadDir(parent)
	a.Close()
	isolateTestStorage(t)
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	folder := filepath.Join(parent, entries[0].Name())
	payload, _ = json.Marshal(map[string]string{"action": "restore", "directory": folder})
	waitStorageTransfer(t, b, string(payload))
	next := filepath.Join(t.TempDir(), "moved.csv")
	if err = os.Rename(source, next); err != nil {
		t.Fatal(err)
	}
	orig := old.Definition.Sources[0]
	if _, err = b.verifyLocation(t.Context(), orig.Book, orig.Path, next, true); err != nil {
		t.Fatal(err)
	}
	checkpointCall(t, b, "/api/tasks/open", taskCheckpointRequest{ID: export.ID, Revision: export.Revision}, 200)
	if got := exportResumeDownload(t, b, exportResumeJob(t, b, export)); !bytes.Equal(got, expected) {
		t.Fatal("relocated export differed")
	}
	job := filterCheckpointResume(t, b, old)
	filterCheckpointWait(t, job)
	if job.Status != "done" {
		t.Fatal("legacy filter", job.Status, job.Error)
	}
	result := b.filterSnapshot(job.ResultID)
	if result == nil {
		t.Fatal("missing legacy order")
	}
	defer result.close()
	want := filterCheckpointExpected(32, false)
	if result.Rows.Len() != len(want) {
		t.Fatal(result.Rows.Len(), len(want))
	}
	for i, row := range want {
		if result.Rows.At(i) != row {
			t.Fatal(i, row)
		}
	}
}

func TestStorageCancellationRetryAndPrivateHistory(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	password := "test-only-private-password"
	body, _ := json.Marshal(map[string]string{"action": "backup", "directory": t.TempDir(), "password": password})
	// Queue a real operation behind maintenance and cancel before publication.
	var job storageJob
	var url string
	func() {
		a.cacheResetMu.Lock()
		defer a.cacheResetMu.Unlock()
		job = startStorageTransfer(t, a, string(body))
		url = "/api/storage/jobs?id=" + job.ID
		response := sessionCall(t, a, "", "POST", url, "{}")
		if response.Code != 200 {
			t.Fatal(response.Body)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		item := decodeResponse[storageJob](t, featureCall(t, a, "GET", url, ""))
		if item.State == "cancelled" {
			break
		}
		time.Sleep(time.Millisecond * 5)
	}
	a.Close()
	again, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if item := decodeResponse[storageJob](t, featureCall(t, again, "GET", url, "")); item.State != "cancelled" {
		t.Fatal(item)
	}
	featureCall(t, again, "POST", url, "{}")
	roots, _ := dataRoots()
	b, err := os.ReadFile(filepath.Join(roots.Config, "transfer-history.json"))
	if err != nil || bytes.Contains(b, []byte(password)) {
		t.Fatal("transfer history stored the password or failed to load", err)
	}
}
