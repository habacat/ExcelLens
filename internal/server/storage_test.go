package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	featureCall(t, a, "POST", "/api/storage", `{"action":"migrate"}`)
	layout := decodeResponse[sheetLayout](t, featureCall(t, a, "GET", "/api/layout?sheet=First", ""))
	if layout.Rows["2"] != 44 {
		t.Fatal(layout)
	}
	featureCall(t, a, "POST", "/api/layout", `{"sheet":"First","kind":"row","index":2,"size":55}`)
	featureCall(t, a, "POST", "/api/storage", `{"action":"migrate"}`)
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
	featureCall(t, a, "POST", "/api/storage", string(request))
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

// Reopen the restored checkpoint through the actual edit API. Its original
// source stays at the same path while both application data roots change.
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
	featureCall(t, reopened, "POST", "/api/storage", string(req))
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
