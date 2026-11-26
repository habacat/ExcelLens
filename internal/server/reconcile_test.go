package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"excellens/internal/analysis"
	"excellens/internal/reconcile"
)

func reconcilePost(t *testing.T, a *App, route string, value any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return analysisRequest(t, a, "POST", route, string(data))
}

func attachReconcileCSV(t *testing.T, a *App, name, data string) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, data)
	for key, value := range map[string]string{"reconcileSource": "1", "workbookKey": a.currentWorkbook().Fingerprint, "primaryBook": sourceBookID(a.currentWorkbook())} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/open", &body)
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", a.token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	out := httptest.NewRecorder()
	a.Handler().ServeHTTP(out, req)
	return decodeResponse[struct{ Book string }](t, out).Book
}

func reconcileRef(t *testing.T, a *App, book, source string, header int) reconcileSourceRef {
	t.Helper()
	ref := reconcileSourceRef{Book: book, Source: source, HeaderRow: header}
	info := decodeResponse[reconcile.SourceInfo](t, reconcilePost(t, a, "/api/reconcile/fields", ref))
	ref.Stamp = info.Stamp
	return ref
}

func startReconcile(t *testing.T, a *App, left, right reconcileSourceRef, query reconcile.Query) *reconcileTask {
	t.Helper()
	wb := a.currentWorkbook()
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/start", map[string]any{"workbookKey": wb.Fingerprint, "primaryBook": sourceBookID(wb), "left": left, "right": right, "query": query}))
	job := a.getReconcileTask(started.JobID)
	if job == nil {
		t.Fatal("task missing")
	}
	select {
	case <-job.done:
	case <-time.After(15 * time.Second):
		t.Fatal("reconciliation timed out")
	}
	return job
}

func awaitReconcileExport(t *testing.T, a *App, id string) *ExportJob {
	t.Helper()
	a.mu.RLock()
	job := a.exports[id]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("export missing")
	}
	select {
	case <-job.done:
	case <-time.After(15 * time.Second):
		t.Fatal("export timed out")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != "done" {
		t.Fatal("export failed", job.Status, job.Error)
	}
	return job
}

func TestReconcileCrossFileSnapshotQueuedCSVAndSourceProtection(t *testing.T) {
	isolateTestStorage(t)
	leftData := "编号,金额,备注\n001,9007199254740993.31,\"多\n行\"\n002,0.1,一致\nleft,2,=1+1\ndup,1,D1\ndup,2,D2\n,1,无键\n"
	rightData := "编号,金额,备注\n001,9007199254740993.30,右文字\n002,0.10,一致\nright,3,=2+2\ndup,3,D3\ndup,4,D4\ndup,5,D5\n,2,无键\n"
	path := filepath.Join(t.TempDir(), "left.csv")
	if err := os.WriteFile(path, []byte(leftData), 0600); err != nil {
		t.Fatal(err)
	}
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	primary := a.currentWorkbook()
	bookID := attachReconcileCSV(t, a, "right.csv", rightData)
	rightBook := a.resolveReconcileBook(bookID)
	if a.currentWorkbook() != primary || rightBook == nil {
		t.Fatal("attachment replaced the primary workbook")
	}
	left := reconcileRef(t, a, sourceBookID(primary), "wb:"+primary.Sheets[0].Name, 1)
	right := reconcileRef(t, a, bookID, "wb:"+rightBook.Sheets[0].Name, 1)
	query := reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}, Fields: []reconcile.Pair{{Left: 2, Right: 2, Mode: "number"}, {Left: 3, Right: 3}}}
	job := startReconcile(t, a, left, right, query)
	if job.Status != "done" {
		t.Fatal(job.Status, job.Error)
	}
	want := reconcile.Counts{Equal: 1, Different: 1, LeftOnly: 1, RightOnly: 1, Duplicate: 1, InvalidKeys: 2, LeftRecords: 6, RightRecords: 7}
	if job.Result.Counts != want {
		t.Fatal(job.Result.Counts)
	}
	navigation := decodeResponse[struct {
		SameBook bool
		URL      string
	}](t, reconcilePost(t, a, "/api/reconcile/navigate", map[string]any{"jobId": job.ID, "side": "right", "row": 2, "col": 2}))
	if navigation.SameBook || navigation.URL == "" {
		t.Fatal(navigation)
	}
	u, _ := url.Parse(navigation.URL)
	sessionID := u.Query().Get("session")
	a.sessionsMu.Lock()
	child := a.sessions[sessionID]
	delete(a.sessions, sessionID)
	a.sessionsMu.Unlock()
	if child == nil || child.app.currentWorkbook() != rightBook {
		t.Fatal("navigation did not share the compared workbook")
	}
	child.app.Close()
	// Live navigation must reject changed files; the completed result and full
	// export continue to read the immutable, pinned cache captured for this job.
	if err := os.WriteFile(rightBook.Path, []byte("changed,source\nnew,data\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out := reconcilePost(t, a, "/api/reconcile/navigate", map[string]any{"jobId": job.ID, "side": "right", "row": 2, "col": 2}); out.Code != 409 || !strings.Contains(out.Body.String(), "核对快照") {
		t.Fatal("changed source navigation accepted or snapshot status lost", out.Code, out.Body.String())
	}
	for i := 0; i < cap(a.exportSem); i++ {
		a.exportSem <- struct{}{}
	}
	defer func() {
		for len(a.exportSem) > 0 {
			<-a.exportSem
		}
	}()
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/export", map[string]any{"jobId": job.ID, "protectCSV": false}))
	decodeResponse[map[string]bool](t, reconcilePost(t, a, "/api/reconcile/clear", map[string]string{"id": job.ID}))
	decodeResponse[map[string]bool](t, reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": bookID}))
	if _, err := a.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ClearUnusedUploads(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rightBook.Path); err != nil {
		t.Fatal("queued export lost its pinned source", err)
	}
	if _, err := os.Stat(rightBook.CacheDir); err != nil {
		t.Fatal("queued export lost its source cache", err)
	}
	<-a.exportSem
	exported := awaitReconcileExport(t, a, started.JobID)
	data, err := os.ReadFile(exported.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\ufeff"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 22 {
		t.Fatal("CSV omitted rows or expanded a multi-to-multi group", len(records))
	}
	members, multiline, delta := 0, false, false
	for _, row := range records[1:] {
		if len(row) != 28 {
			t.Fatal("CSV shape", row)
		}
		if row[0] == "重复键记录" {
			members++
		}
		multiline = multiline || row[13] == "多\n行"
		delta = delta || row[26] == "0.01"
		if row[5] != "left.csv" || row[14] != "right.csv" {
			t.Fatal("source names drifted", row)
		}
	}
	if members != 10 || !multiline || !delta {
		t.Fatal("full CSV fidelity", members, multiline, delta)
	}
	if sha256.Sum256([]byte(leftData)) != func() [32]byte { data, _ := os.ReadFile(path); return sha256.Sum256(data) }() {
		t.Fatal("primary source changed")
	}
	if _, err := a.ClearUnusedUploads(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rightBook.Path); !os.IsNotExist(err) {
		t.Fatal("released uploaded copy remained protected", err)
	}
}

func TestReconcileSavedAnalysisSnapshotSurvivesEditsAndRejectsStaleNavigation(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "source.csv")
	_ = os.WriteFile(path, []byte("编号,金额\nA,2\nB,3\n"), 0600)
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	err := a.editAnalysis(wb, func(workspace *analysis.Workspace) error {
		workspace.Sheets = append(workspace.Sheets, analysis.Sheet{ID: "saved", Name: "核对编辑表", Columns: []string{"编号", "金额"}, Rows: [][]string{{"A", "2"}, {"B", "3"}}, Revision: 1})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	left := reconcileRef(t, a, sourceBookID(wb), "scratch:saved", 0)
	right := reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1)
	query := reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}, Fields: []reconcile.Pair{{Left: 2, Right: 2, Mode: "number"}}, IncludeEqual: true}
	job := startReconcile(t, a, left, right, query)
	if job.Status != "done" || job.Result.Counts.Equal != 2 {
		t.Fatal(job.Status, job.Error, job.Result.Counts)
	}
	if job.Result.Samples[0].Left.Number != 1 || job.Result.Samples[0].Right.Number != 2 {
		t.Fatal("source coordinates were compacted")
	}
	if err := a.editAnalysis(wb, func(workspace *analysis.Workspace) error {
		sheet := workspace.Sheet("saved")
		sheet.Rows[0][1] = "99"
		sheet.Revision++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if out := reconcilePost(t, a, "/api/reconcile/navigate", map[string]any{"jobId": job.ID, "side": "left", "row": 1, "col": 2}); out.Code != 409 {
		t.Fatal("stale editor navigation accepted", out.Code, out.Body.String())
	}
	stale := startReconcile(t, a, left, right, query)
	if stale.Status != "error" || !strings.Contains(stale.Error, "已修改") {
		t.Fatal("stale metadata silently rebound", stale.Status, stale.Error)
	}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/export", map[string]any{"jobId": job.ID, "protectCSV": false}))
	exported := awaitReconcileExport(t, a, started.JobID)
	data, _ := os.ReadFile(exported.FilePath)
	if strings.Contains(string(data), ",99,") || !strings.Contains(string(data), ",true,2,") {
		t.Fatal("export reloaded changed editor values", string(data))
	}
}

func TestReconcileCancellationAndWorkbookReplacementReleaseEveryPin(t *testing.T) {
	isolateTestStorage(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "one.csv")
	_ = os.WriteFile(path, []byte("ID,Amount\nA,1\n"), 0600)
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	ref := reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1)
	for i := 0; i < cap(a.querySem); i++ {
		a.querySem <- struct{}{}
	}
	request := map[string]any{"workbookKey": wb.Fingerprint, "primaryBook": sourceBookID(wb), "left": ref, "right": ref, "query": reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}}}
	started := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/start", request))
	job := a.getReconcileTask(started.JobID)
	decodeResponse[map[string]bool](t, reconcilePost(t, a, "/api/reconcile/cancel", map[string]string{"id": job.ID}))
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("queued cancellation blocked")
	}
	for len(a.querySem) > 0 {
		<-a.querySem
	}
	if job.Status != "cancelled" {
		t.Fatal(job.Status, job.Error)
	}
	newPath := filepath.Join(dir, "two.csv")
	_ = os.WriteFile(newPath, []byte("ID,Amount\nB,2\n"), 0600)
	if err := a.OpenPath(newPath); err != nil {
		t.Fatal(err)
	}
	pins, uploads := a.sourcePins.Len(), a.sourceUploads.Len()
	if pins != 0 || uploads != 0 || len(a.reconcileJobs) != 0 || a.cacheMu == nil {
		t.Fatal("replacement retained orphan source leases", pins, uploads)
	}
}

func TestReconcileRealSQLiteKeepsTypeLossAndPlaceholderUncertaintyVisible(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native readonly SQLite adapter is available on Windows")
	}
	isolateTestStorage(t)
	sample, err := os.ReadFile("../workbook/testdata/ledger.db")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "ledger.db")
	if err := os.WriteFile(file, sample, 0600); err != nil {
		t.Fatal(err)
	}
	a, _ := New()
	defer a.Close()
	if err := a.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	var sheet string
	for _, meta := range wb.Sheets {
		if strings.HasPrefix(meta.Name, "财务") {
			sheet = meta.Name
		}
	}
	if sheet == "" {
		t.Fatal("finance fixture missing")
	}
	ref := reconcileRef(t, a, sourceBookID(wb), "wb:"+sheet, 1)
	unknown := startReconcile(t, a, ref, ref, reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1}}})
	if unknown.Status != "done" || unknown.Result.Counts.InvalidKeys != 4 || !strings.Contains(unknown.Result.Left.Notice, "未保留原始类型") {
		t.Fatal("SQLite cached text silently promoted to original source type", unknown.Status, unknown.Error, unknown.Result)
	}
	explicit := startReconcile(t, a, ref, ref, reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 1, Mode: "text"}}, Fields: []reconcile.Pair{{Left: 2, Right: 2, Mode: "number"}, {Left: 3, Right: 3, Mode: "text"}, {Left: 4, Right: 4, Mode: "text"}}})
	if explicit.Status != "done" || explicit.Result.Counts.Review == 0 || explicit.Result.Counts.Equal+explicit.Result.Counts.Review != 2 {
		t.Fatal("SQLite NULL/BLOB cache labels treated as reliable text", explicit.Status, explicit.Error, explicit.Result)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(sample, after) {
		t.Fatal("native SQLite reconciliation modified its source", err)
	}
}
