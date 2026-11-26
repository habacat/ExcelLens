package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/localstore"
	"excellens/internal/workbook"
)

const databaseSnapshotsAPI = "/api/database/snapshots"

func createDatabaseSnapshotFixture(t *testing.T) *workbook.Workbook {
	t.Helper()
	ctx := context.Background()
	d, err := workbook.StartDatabaseSnapshot("PostgreSQL · ledger", workbook.DatabaseSnapshotSource{
		Engine: "postgres", Protocol: "native", Host: "report.example.test", Port: 5432, Database: "ledger", TLS: "verify",
	}, []workbook.DatabaseSnapshotTable{{Schema: "audit", Name: "claims", Kind: "table"}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Discard()
	err = d.WriteTable(ctx, func(begin func(workbook.DatabaseTableDefinition) error, row func([]workbook.DatabaseCell) error) error {
		if err := begin(workbook.DatabaseTableDefinition{Query: `SELECT * FROM "audit"."claims"`, Ordering: "unspecified",
			Columns: []workbook.DatabaseColumn{{Name: "claim_no", Type: "TEXT"}, {Name: "amount", Type: "NUMERIC"}}}); err != nil {
			return err
		}
		return row([]workbook.DatabaseCell{{Kind: 't', Raw: "000123"}, {Kind: 'n', Raw: "9007199254740993.3100"}})
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := workbook.ListDatabaseSnapshots(ctx); err != nil || len(list) != 0 {
		t.Fatalf("unpublished snapshot was discoverable: %v %v", list, err)
	}
	roots, err := dataRoots()
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range roots.Inventory(ctx) {
		if group.Name == "数据库快照" && (group.Files != 0 || group.Error != "") {
			t.Fatalf("incomplete snapshot counted as evidence: %#v", group)
		}
	}
	wb, err := d.Commit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return wb
}

func TestDatabaseSnapshotSurvivesCacheClearsAndEncryptedMigration(t *testing.T) {
	isolateTestStorage(t)
	ctx := context.Background()
	wb := createDatabaseSnapshotFixture(t)
	if !wb.SourceInfo.DatabaseSnapshot.Retained || strings.HasPrefix(wb.Path, workbook.CacheRoot()+string(os.PathSeparator)) {
		t.Fatalf("snapshot did not become managed source: %q", wb.Path)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	start := decodeResponse[struct{ WorkbookKey string }](t, reconcilePost(t, app, databaseSnapshotsAPI, map[string]string{"action": "open", "key": wb.Fingerprint}))
	if start.WorkbookKey != wb.Fingerprint {
		t.Fatal(start)
	}
	assertDatabaseSnapshotCacheClear(t, app)
	if _, err := app.ClearAllCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	proof := decodeResponse[struct {
		Verified bool
		Evidence struct {
			Version     int
			Source      workbook.DatabaseSnapshotSource
			Consistency string
			Tables      []struct {
				Evidence struct {
					Records     int
					Rows, Index struct{ SHA256 string }
					Columns     []workbook.DatabaseColumn
				}
			}
		}
	}](t, reconcilePost(t, app, databaseSnapshotsAPI, map[string]string{"action": "verify", "key": wb.Fingerprint}))
	if !proof.Verified || proof.Evidence.Version != 2 || proof.Evidence.Consistency != "per-table" ||
		proof.Evidence.Source.Host != "report.example.test" || len(proof.Evidence.Tables) != 1 {
		t.Fatalf("incomplete provenance: %#v", proof)
	}
	d := proof.Evidence.Tables[0].Evidence
	if d.Records != 1 || len(d.Columns) != 2 || d.Columns[1].Type != "NUMERIC" || d.Rows.SHA256 == "" || d.Index.SHA256 == "" {
		t.Fatalf("incomplete table evidence: %#v", d)
	}
	app.Close()
	roots, err := dataRoots()
	if err != nil {
		t.Fatal(err)
	}
	options := localstore.TransferOptions{Password: "fixture-encryption-passphrase"}
	backup, err := roots.BackupWith(ctx, t.TempDir(), options)
	if err != nil {
		t.Fatal(err)
	}
	isolateTestStorage(t)
	restored, err := dataRoots()
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreWith(ctx, backup, options); err != nil {
		t.Fatal(err)
	}
	app, err = New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	recent := decodeResponse[recentFile](t, featureCall(t, app, http.MethodGet, "/api/recent", ""))
	if len(recent.Items) != 1 || !recent.Items[0].Available {
		t.Fatalf("restored recent snapshot is unavailable: %#v", recent.Items)
	}
	featureCall(t, app, http.MethodPost, "/api/recent", `{"action":"open","id":"`+recent.Items[0].ID+`"}`)
	after := app.currentWorkbook()
	if after.Path == wb.Path || after.Fingerprint != wb.Fingerprint {
		t.Fatal("restored source was not resolved in the new data root")
	}
	idx := after.GetCachedIndex(after.Sheets[0].Path)
	if idx == nil || cellValueServer(t, after, idx, 2, 1) != "000123" || cellValueServer(t, after, idx, 2, 2) != "9007199254740993.3100" {
		t.Fatal("restored values changed")
	}
	for _, part := range []struct {
		path, expected string
	}{{idx.XMLPath, d.Rows.SHA256}, {idx.IndexPath, d.Index.SHA256}} {
		raw, err := os.ReadFile(part.path)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != part.expected {
			t.Fatal("the exported digest does not match the independent file digest", err)
		}
		st, err := os.Stat(part.path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0222 != 0 {
			t.Fatal("restored evidence is writable", part.path)
		}
		if err := os.Chmod(part.path, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := after.CachedIndexForRead(ctx, after.Sheets[0].Path); err != nil {
			t.Fatal("metadata-only change rejected", err)
		}
		// Modify a byte without changing file length, then move mtime back.
		f, err := os.OpenFile(part.path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		ch := raw[len(raw)-1] ^ 1
		_, err = f.WriteAt([]byte{ch}, int64(len(raw)-1))
		closeErr := f.Close()
		if err != nil || closeErr != nil || os.Chtimes(part.path, st.ModTime(), st.ModTime()) != nil {
			t.Fatal(err, closeErr)
		}
		grid := analysisRequest(t, app, "GET", "/api/sheet?sheet=claims&row=2&cols=2", "")
		if grid.Code != http.StatusConflict || !strings.Contains(grid.Body.String(), "摘要不匹配") {
			t.Fatalf("grid accepted changed part: %d %s", grid.Code, grid.Body.String())
		}
		for _, action := range []string{"open", "verify", "export"} {
			res := reconcilePost(t, app, databaseSnapshotsAPI, map[string]string{"action": action, "key": wb.Fingerprint})
			if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "摘要不匹配") || app.currentWorkbook() != after {
				t.Fatalf("%s accepted a same-size rewrite: %d %s", action, res.Code, res.Body.String())
			}
		}
		if err := after.CheckSourceUnchanged(); err == nil {
			t.Fatal("an explicit source binding accepted altered snapshot data")
		}
		if err := os.WriteFile(part.path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOldDatabaseSnapshotRemainsProtectedAndCanBeSealed(t *testing.T) {
	isolateTestStorage(t)
	newSource := createDatabaseSnapshotFixture(t)
	manifest, err := json.Marshal(map[string]any{"version": 1, "nonce": "legacy-fixture", "label": "旧快照",
		"taken": time.Now().UTC(), "tables": []map[string]string{{"schema": "audit", "name": "claims", "kind": "table"}}})
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("%x", sha256.Sum256(manifest))
	legacy := workbook.LegacyDatabaseSnapshotPath(key)
	legacyDir := filepath.Dir(legacy)
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	idx := newSource.GetCachedIndex(newSource.Sheets[0].Path)
	for _, part := range []string{idx.XMLPath, idx.IndexPath} {
		data, err := os.ReadFile(part)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacyDir, filepath.Base(part)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(legacy, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, clean := range []func() (string, error){app.ClearUnusedCaches, app.ClearAllCaches} {
		if _, err := clean(); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(manifest, body) {
		t.Fatalf("ordinary cache cleanup removed or changed a closed legacy source: %v", err)
	}
	featureCall(t, app, http.MethodPost, databaseSnapshotsAPI, `{"action":"open","key":"`+key+`"}`)
	assertDatabaseSnapshotCacheClear(t, app)
	first := decodeResponse[struct{ Key string }](t, reconcilePost(t, app, databaseSnapshotsAPI, map[string]string{"action": "retain", "key": key}))
	next := decodeResponse[struct{ Key string }](t, reconcilePost(t, app, databaseSnapshotsAPI, map[string]string{"action": "retain", "key": key}))
	if first.Key == key || first.Key != next.Key {
		t.Fatalf("resaving did not preserve one independently addressable seal: %q %q", first.Key, next.Key)
	}
	saved, err := workbook.OpenWorkbook(workbook.DatabaseSnapshotPath(first.Key))
	if err != nil || !saved.SourceInfo.DatabaseSnapshot.Legacy || !saved.SourceInfo.DatabaseSnapshot.Retained {
		t.Fatalf("legacy provenance lost: %v", err)
	}
	if manifestAfter, err := os.ReadFile(legacy); err != nil || !bytes.Equal(manifestAfter, manifest) {
		t.Fatal("sealing changed the original snapshot", err)
	}
}

func assertDatabaseSnapshotCacheClear(t *testing.T, app *App) {
	t.Helper()
	wb := app.currentWorkbook()
	sheet := wb.Sheets[0]
	const query = "000123"
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, app, "/api/search/start", map[string]string{
		"sheet": sheet.Name, "query": query, "mode": "value", "scope": "current",
	}))
	app.mu.RLock()
	job := app.searchJobs[started.JobID]
	app.mu.RUnlock()
	if job == nil {
		t.Fatal("search did not start")
	}
	checkpointWait(t, job)
	if job.Status != "done" || len(job.Matches) != 1 {
		t.Fatalf("snapshot search did not find the source record: %s (%d hits)", job.Status, len(job.Matches))
	}
	cache := searchCachePath(wb, sheet.Name, query, "value", "current")
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("search cache was not created", err)
	}
	sourceEntries, err := os.ReadDir(filepath.Dir(wb.Path))
	if err != nil {
		t.Fatal(err)
	}
	featureCall(t, app, http.MethodPost, "/api/cache/clear", "")
	if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("per-workbook cleanup did not clear the derived search cache", err)
	}
	idx := wb.GetCachedIndex(sheet.Path)
	if idx == nil || cellValueServer(t, wb, idx, 2, 1) != query {
		t.Fatal("per-workbook cache cleanup removed the source rows or index")
	}
	if len(sourceEntries) != 3 {
		t.Fatal("the query cache was stored alongside the three source files", sourceEntries)
	}
}
