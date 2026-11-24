package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/editdoc"
	"excellens/internal/workbook"
)

func TestFullCacheResetClosesTabsAndPreservesFilesAndLayout(t *testing.T) {
	isolateTestStorage(t)
	source := filepath.Join(t.TempDir(), "费用.xlsx")
	writeMultiSheetWorkbook(t, source)
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	a := prepareFeatureApp(t, source)
	defer a.Close()
	id, err := a.OpenPathForLaunch(source)
	if err != nil {
		t.Fatal(err)
	}
	if ribbon, err := a.RibbonLayout(); err != nil || ribbon {
		t.Fatal(ribbon, err)
	}
	// A web change must be visible to the tray and to later processes.
	res := sessionCall(t, a, id, "POST", "/api/interface", `{"ribbon":true}`)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if ribbon, err := a.RibbonLayout(); !ribbon || err != nil {
		t.Fatal(ribbon, err)
	}
	upload := filepath.Join(workbook.UploadRoot(), "ExcelLens-upload-source.csv")
	if err := os.MkdirAll(filepath.Dir(upload), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(upload, []byte("001,12"), 0600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(workbook.CacheRoot(), strings.Repeat("a", 64), "row.idx")
	if err := os.MkdirAll(filepath.Dir(history), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(history, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := a.ClearAllCaches()
	if err != nil {
		t.Fatal(result, err)
	}
	entries, err := os.ReadDir(workbook.CacheRoot())
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	if a.currentWorkbook() != nil || a.cacheEpoch.Load() != 1 {
		t.Fatal("current workbook not reset")
	}
	if res := sessionCall(t, a, id, "GET", "/api/status", ""); !strings.Contains(res.Body.String(), `"opened":false`) {
		t.Fatal(res.Body.String())
	}
	req := localTestConnection(httptest.NewRequest("POST", "/api/open", nil))
	req.Header.Set("X-ExcelLens-Token", a.token)
	req.Header.Set("X-ExcelLens-Cache-Epoch", "0")
	stale := httptest.NewRecorder()
	a.Handler().ServeHTTP(stale, req)
	if stale.Code != 409 {
		t.Fatalf("a stale tab request reached its handler: %d", stale.Code)
	}
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if ribbon, err := b.RibbonLayout(); err != nil || !ribbon {
		t.Fatal(ribbon, err)
	}
	if err = b.SetRibbonLayout(false); err != nil {
		t.Fatal(err)
	}
	if res := sessionCall(t, b, "", "GET", "/api/interface", ""); !strings.Contains(res.Body.String(), `"ribbon":false`) {
		t.Fatal(res.Body.String())
	}
	if data, err := os.ReadFile(source); err != nil || !bytes.Equal(original, data) {
		t.Fatal("source changed", err)
	}
	if data, err := os.ReadFile(upload); err != nil || string(data) != "001,12" {
		t.Fatal("upload changed", err)
	}
	if err := a.OpenPath(source); err != nil {
		t.Fatal(err)
	}
	meta, _ := a.currentWorkbook().FindSheet("First")
	if _, err := a.currentWorkbook().BuildIndex(meta, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFullCacheResetWaitsForRequestsAndPreservesSourceInsideCache(t *testing.T) {
	isolateTestStorage(t)
	dir := filepath.Join(workbook.CacheRoot(), strings.Repeat("c", 64))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source.xlsx")
	writeMultiSheetWorkbook(t, source)
	unused := filepath.Join(dir, "unused.idx")
	if err := os.WriteFile(unused, []byte("index"), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(workbook.CacheRoot(), "personal", "original.csv")
	if err := os.MkdirAll(filepath.Dir(other), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("001"), 0600); err != nil {
		t.Fatal(err)
	}
	a := prepareFeatureApp(t, source)
	defer a.Close()
	guard := make(chan struct{})
	if _, err := a.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("history cleanup removed the current source", err)
	}
	release := make(chan struct{})
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		a.routeSessions(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(guard); <-release })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/test", nil))
	}()
	<-guard
	done := make(chan error, 1)
	go func() { _, err := a.ClearAllCaches(); done <- err }()
	select {
	case <-done:
		t.Fatal("cache cleared while a reader owned the request")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-requestDone
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatal("source inside cache was removed", err)
	}
	if _, err := os.Stat(unused); !os.IsNotExist(err) {
		t.Fatal("a protected source incorrectly retained its cache siblings", err)
	}
	if data, err := os.ReadFile(other); err != nil || string(data) != "001" {
		t.Fatal("non-cache directory was modified", err)
	}
}

func TestFullCacheResetReleasesMappedIndexAndKeepsConfirmedEdit(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "small.csv")
	if err := os.WriteFile(source, []byte("id,value\n001,42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.OpenPath(source); err != nil {
		t.Fatal(err)
	}
	state := decodeResponse[editState](t, editHTTP(t, a, "start", editRequest{Book: sourceBookID(a.currentWorkbook())}, 200))
	address := editdoc.Address{Sheet: state.Sheets[0].Name, Row: 2, Col: 1}
	doc := state.Document.Key
	editHTTP(t, a, "commit", editRequest{Document: doc, Operation: editdoc.Operation{Revision: 1, Request: strings.Repeat("1", 32), Action: "change", Changes: []editdoc.Change{{Address: address, Value: editdoc.Value{Kind: "text", Raw: "009"}}}}}, 200)
	large := filepath.Join(dir, "mapped.csv")
	if err := os.WriteFile(large, bytes.Repeat([]byte("001,1\n"), workbook.MaxResidentRowOffsets+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(large); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	if _, err := wb.BuildIndex(wb.Sheets[0], nil, nil); err != nil {
		t.Fatal(err)
	}
	if !wb.HasMappedIndexes() {
		t.Fatal("large source did not exercise mapped files")
	}
	if _, err := a.ClearAllCaches(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(workbook.CacheRoot()); err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
	if err := a.OpenPath(source); err != nil {
		t.Fatal(err)
	}
	cell := decodeResponse[struct{ Base, Value editdoc.Value }](t, editHTTP(t, a, "read", editRequest{Document: doc, Sheet: address.Sheet, Ref: "A2", Revision: 2}, 200))
	if cell.Value.Raw != "009" {
		t.Fatal(cell)
	}
}

func TestAssetCachingAndETag304(t *testing.T) {
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()

	// Static embedded assets carry ETag and immutable cache header, and honor If-None-Match.
	req := localTestConnection(httptest.NewRequest(http.MethodGet, "/assets/style.css", nil))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || res.Body.Len() == 0 {
		t.Fatalf("GET /assets/style.css status=%d len=%d", res.Code, res.Body.Len())
	}
	etag := res.Header().Get("ETag")
	if len(etag) != 66 || !strings.Contains(res.Header().Get("Cache-Control"), "no-cache") {
		t.Fatalf("unexpected asset headers: ETag=%q Cache-Control=%q", etag, res.Header().Get("Cache-Control"))
	}
	req304 := localTestConnection(httptest.NewRequest(http.MethodGet, "/assets/style.css", nil))
	req304.URL.RawQuery = "v=" + strings.Trim(etag, `"`)
	req304.Header.Set("If-None-Match", etag)
	res304 := httptest.NewRecorder()
	h.ServeHTTP(res304, req304)
	if res304.Code != http.StatusNotModified || res304.Body.Len() != 0 || !strings.Contains(res304.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("304 expected, got status=%d len=%d", res304.Code, res304.Body.Len())
	}

	// Dynamic index and API responses must remain no-store.
	for _, path := range []string{"/", "/api/status"} {
		r := localTestConnection(httptest.NewRequest(http.MethodGet, path, nil))
		r.Header.Set("X-ExcelLens-Token", a.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: status=%d Cache-Control=%q", path, w.Code, w.Header().Get("Cache-Control"))
		}
	}
}

func TestInterfaceLongPollWakesOnLayoutAndCacheReset(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h := a.Handler()

	// Retiring a child must leave the shared notification channel alive.
	child, err := a.newBrowserSession("")
	if err != nil {
		t.Fatal(err)
	}
	child.app.Close()
	if _, open := a.toolbar.waitChannel(); !open {
		t.Fatal("child shutdown disabled interface watches")
	}

	// Waiting request wakes immediately when layout changes.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := localTestConnection(httptest.NewRequest(http.MethodGet, "/api/interface?wait=1&ribbon=false&epoch=0", nil))
		r.Header.Set("X-ExcelLens-Token", a.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		done <- w
	}()
	time.Sleep(25 * time.Millisecond)
	if err := a.SetRibbonLayout(true); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-done:
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ribbon":true`) {
			t.Fatalf("unexpected layout wake response: %d %s", w.Code, w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interface long-poll did not wake on SetRibbonLayout")
	}

	// Waiting request must not block ClearAllCaches and must wake with the new epoch.
	doneReset := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := localTestConnection(httptest.NewRequest(http.MethodGet, "/api/interface?wait=1&ribbon=true&epoch=0", nil))
		r.Header.Set("X-ExcelLens-Token", a.token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		doneReset <- w
	}()
	time.Sleep(25 * time.Millisecond)
	if _, err := a.ClearAllCaches(); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-doneReset:
		if w.Code != http.StatusOK || w.Header().Get("X-ExcelLens-Cache-Epoch") != "1" || !strings.Contains(w.Body.String(), `"cacheEpoch":1`) {
			t.Fatalf("unexpected cache-reset wake response: %d hdr=%q body=%s", w.Code, w.Header().Get("X-ExcelLens-Cache-Epoch"), w.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interface long-poll did not wake on ClearAllCaches")
	}
}
