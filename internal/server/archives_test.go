package server

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"excellens/internal/workbook"
)

func zipArchiveFixture(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for name, content := range entries {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func rawArchiveCall(app *App, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", app.token)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	return response
}

func uploadArchiveFixture(t *testing.T, app *App, name string, content []byte) map[string]any {
	t.Helper()
	var buffer bytes.Buffer
	mw := multipart.NewWriter(&buffer)
	w, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/open", &buffer)
	req = localTestConnection(req)
	req.Header.Set("X-ExcelLens-Token", app.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res := httptest.NewRecorder()
	app.Handler().ServeHTTP(res, req)
	if res.Code != 200 {
		t.Fatalf("archive upload %d: %s", res.Code, res.Body.String())
	}
	return decodeResponse[map[string]any](t, res)
}

func awaitArchiveFixture(t *testing.T, app *App, archive string, member int, partial ...bool) map[string]any {
	t.Helper()
	allowPartial := len(partial) > 0 && partial[0]
	started := decodeResponse[struct {
		JobID string `json:"jobId"`
	}](t, featureCall(t, app, "POST", "/api/archive/open", fmt.Sprintf(`{"archive":%q,"member":%d,"allowPartial":%t}`, archive, member, allowPartial)))
	app.archiveMu.Lock()
	job := app.archiveTask
	app.archiveMu.Unlock()
	if job == nil || job.id != started.JobID {
		t.Fatal("archive job not created")
	}
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("archive job timed out")
	}
	response := decodeResponse[struct {
		Status, Error string
		Result        map[string]any
	}](t, featureCall(t, app, "GET", "/api/archive/job?id="+job.id, ""))
	if response.Status != "done" {
		t.Fatalf("archive job %s: %s", response.Status, response.Error)
	}
	return response.Result
}

func TestRARWorkbookOpen(t *testing.T) {
	path, err := filepath.Abs("../archives/testdata/rar5-solid.rar")
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"local", "upload"} {
		t.Run(route, func(t *testing.T) {
			isolateTestStorage(t)
			app, err := New()
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()

			if route == "upload" {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if uploaded := uploadArchiveFixture(t, app, "资料.RAR", raw); uploaded["archiveOnly"] != true {
					t.Fatal(uploaded)
				}
			} else if err := app.OpenPath(path); err != nil {
				t.Fatal(err)
			}

			summary := app.archiveSummary()
			if summary.Format != "RAR" || app.currentWorkbook() != nil {
				t.Fatal(summary)
			}
			id := summary.ID
			list := featureCall(t, app, "GET", "/api/archive/list?id="+id+"&q="+url.QueryEscape("明细"), "")
			items := decodeResponse[struct {
				Items []struct {
					ID   int
					Name string
				}
			}](t, list).Items
			if len(items) != 1 || items[0].Name != "02-明细.csv" {
				t.Fatal(items)
			}
			if result := awaitArchiveFixture(t, app, id, items[0].ID); result["opened"] != true {
				t.Fatal(result)
			}
			wb := app.currentWorkbook()
			if wb == nil || wb.SourceInfo.ArchiveMember != items[0].Name {
				t.Fatal("workbook did not retain RAR provenance")
			}
			idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
			if err != nil || idx.MaxRow != 385 || idx.MaxCol != 2 {
				t.Fatal(idx, err)
			}
			if id := cellValueServer(t, wb, idx, 385, 1); id != "000789" {
				t.Fatalf("source identifier = %q, want leading zeros", id)
			}
			if amount := cellValueServer(t, wb, idx, 385, 2); amount != "6.75" {
				t.Fatalf("source amount = %q", amount)
			}
		})
	}
}

func TestTruncatedTARRecoveryNoticeIndexAndRecent(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	header := "凭证来源@批名@凭证编号@凭证名@日期@凭证类别@过帐人@创建人@审批人@原币@原币借@原币贷@本位币@本位借@本位贷@segment1@seg1_desc"
	record := "系统@批@000000000000000001@凭@01-01-25@记账@人@人@审批@CNY@7.2800001@@CNY@7.2800001@@0001@缺省"
	fragment := []byte(header + record + "\n" + record + "\n" + record + "\n" + "末尾@片段")
	var buffer bytes.Buffer
	tw := tar.NewWriter(&buffer)
	if err := tw.WriteHeader(&tar.Header{Name: "data/ledger.txt", Mode: 0600, Size: int64(len(fragment) + 10000)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(fragment); err != nil {
		t.Fatal(err)
	}
	uploaded := uploadArchiveFixture(t, app, "cut.tar", buffer.Bytes())
	if uploaded["archiveOnly"] != true {
		t.Fatal(uploaded)
	}
	summary := app.archiveSummary()
	id := summary.ID
	if !strings.Contains(summary.Notice, "不完整") {
		t.Fatal(summary)
	}
	list := featureCall(t, app, "GET", "/api/archive/list?id="+id, "")
	if !strings.Contains(list.Body.String(), `"incomplete":true`) || !strings.Contains(list.Body.String(), `"declaredSize":`) {
		t.Fatal(list.Body.String())
	}
	denied := rawArchiveCall(app, "POST", "/api/archive/open", fmt.Sprintf(`{"archive":%q,"member":0}`, id))
	if denied.Code != http.StatusConflict || app.currentWorkbook() != nil {
		t.Fatal("normal open accepted partial data", denied.Body.String())
	}
	result := awaitArchiveFixture(t, app, id, 0, true)
	if result["opened"] != true {
		t.Fatal(result)
	}
	wb := app.currentWorkbook()
	if !wb.SourceInfo.Incomplete || !strings.Contains(wb.SourceInfo.ArchiveNotice, "10000 字节") || !strings.Contains(wb.DisplayName, "不完整片段") {
		t.Fatal(wb.SourceInfo, wb.DisplayName)
	}
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil || idx.MaxRow != 4 || idx.MaxCol != 17 {
		t.Fatal(idx, err)
	}
	if got := cellValueServer(t, wb, idx, 4, 3); got != "000000000000000001" {
		t.Fatal(got)
	}
	if got := cellValueServer(t, wb, idx, 4, 14); got != "7.2800001" {
		t.Fatal(got)
	}
	if raw, _ := os.ReadFile(wb.Path); !bytes.Equal(raw, fragment) {
		t.Fatal("raw fragment not preserved")
	}
	recents := readRecents()
	if len(recents.Items) != 1 || !recents.Items[0].ArchivePartial {
		t.Fatal("fragment provenance missing", recents)
	}
	app.Close()
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	opened := featureCall(t, other, "POST", "/api/recent", `{"action":"open","id":"`+recents.Items[0].ID+`"}`)
	if opened.Code != http.StatusOK || !other.currentWorkbook().SourceInfo.Incomplete || other.currentWorkbook().SourceInfo.ArchiveNotice == "" {
		t.Fatal("recent lost partial notice", opened.Body.String())
	}
	status := featureCall(t, other, "GET", "/api/status", "")
	if !strings.Contains(status.Body.String(), `"incomplete":true`) || !strings.Contains(status.Body.String(), `"archiveNotice":`) {
		t.Fatal(status.Body.String())
	}
}

func TestArchiveUploadMemberIndexStatsExportRecentAndCleanup(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	xlsxPath := filepath.Join(t.TempDir(), "source.xlsx")
	writeMultiSheetWorkbook(t, xlsxPath)
	xlsx, err := os.ReadFile(xlsxPath)
	if err != nil {
		t.Fatal(err)
	}
	content := zipArchiveFixture(t, map[string][]byte{"data/ledger.txt": []byte("账号@金额@贷方\n000123456789012345@1.234567@\n0002@2@\n0003@-0.000001@\n"), "book/source.xlsx": xlsx, "ignore.bin": []byte("untouched")})
	uploaded := uploadArchiveFixture(t, app, "账簿.zip", content)
	if uploaded["archiveOnly"] != true || app.currentWorkbook() != nil {
		t.Fatal("archive should first show its directory")
	}
	view := app.archiveSummary()
	id := view.ID
	listed := decodeResponse[struct {
		Total int
		Items []struct {
			ID   int
			Name string
		}
	}](t, featureCall(t, app, "GET", "/api/archive/list?id="+id+"&q=ledger&limit=1", ""))
	if listed.Total != 1 || len(listed.Items) != 1 || listed.Items[0].Name != "data/ledger.txt" {
		t.Fatalf("filtered directory %+v", listed)
	}
	result := awaitArchiveFixture(t, app, id, listed.Items[0].ID)
	if result["opened"] != true {
		t.Fatal(result)
	}
	wb := app.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if idx.MaxRow != 4 || idx.MaxCol != 3 || cellValueServer(t, wb, idx, 2, 1) != "000123456789012345" {
		t.Fatal("archive member data/positions changed")
	}
	stats := decodeResponse[selectionStatResult](t, analysisRequest(t, app, "POST", "/api/selection/stats", `{"sheet":"数据","ranges":[{"start":"B2","end":"B4"}]}`))
	if stats.Sum != "3.234566" || stats.Number != 3 {
		t.Fatalf("financial stats %+v", stats)
	}
	app.archiveMu.Lock()
	originalArchive := app.archiveView.file.Path
	app.archiveMu.Unlock()
	if _, err := app.ClearUnusedUploads(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(originalArchive); err != nil {
		t.Fatal("cleanup deleted current archive", err)
	}
	if _, err := app.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := wb.ReadRow(idx, 3, 1, 3); err != nil {
		t.Fatal("cleanup invalidated member/index", err)
	}
	recents := readRecents()
	if len(recents.Items) != 1 || recents.Items[0].ArchivePath != originalArchive {
		t.Fatalf("archive provenance missing: %+v", recents.Items)
	}
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	opened := featureCall(t, other, "POST", "/api/recent", `{"action":"open","id":"`+recents.Items[0].ID+`"}`)
	if opened.Code != 200 || other.currentWorkbook() == nil || other.currentWorkbook().SourceInfo.ArchiveName != "账簿.zip" {
		t.Fatal("recent member reopen", opened.Body.String())
	}
	list := decodeResponse[struct {
		Items []struct {
			ID   int
			Name string
		}
	}](t, featureCall(t, app, "GET", "/api/archive/list?id="+id+"&q=xlsx", ""))
	awaitArchiveFixture(t, app, id, list.Items[0].ID)
	if app.currentWorkbook().Format != "xlsx" || len(app.currentWorkbook().Sheets) != 2 {
		t.Fatal("XLSX member did not use original engine")
	}
}

func TestArchiveNativeLaunchNestedAndSessionIsolation(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	inner := zipArchiveFixture(t, map[string][]byte{"one.csv": []byte("ID,amount\n0001,9.999999\n")})
	outer := zipArchiveFixture(t, map[string][]byte{"nested/inner.zip": inner, "other.txt": []byte("A@B\n1@2\n")})
	path := filepath.Join(t.TempDir(), "outer.zip")
	if err := os.WriteFile(path, outer, 0600); err != nil {
		t.Fatal(err)
	}
	sessionID, err := app.OpenPathForLaunch(path)
	if err != nil {
		t.Fatal(err)
	}
	app.sessionsMu.Lock()
	session := app.sessions[sessionID].app
	app.sessionsMu.Unlock()
	status := sessionCall(t, app, sessionID, "GET", "/api/status", "")
	if !strings.Contains(status.Body.String(), "outer.zip") {
		t.Fatal("shell launch did not preserve archive", status.Body.String())
	}
	id := session.archiveSummary().ID
	denied := rawArchiveCall(app, "GET", "/api/archive/list?id="+id, "")
	if denied.Code != 404 {
		t.Fatal("archive leaked across sessions")
	}
	listed := decodeResponse[struct{ Items []struct{ ID int } }](t, featureCall(t, session, "GET", "/api/archive/list?id="+id+"&q=inner", ""))
	result := awaitArchiveFixture(t, session, id, listed.Items[0].ID)
	if result["archiveOnly"] != true {
		t.Fatal("nested archive not browsable", result)
	}
	newID := session.archiveSummary().ID
	result = awaitArchiveFixture(t, session, newID, 0)
	if result["opened"] != true || session.currentWorkbook().SourceInfo.ArchiveMember != "one.csv" {
		t.Fatal(result)
	}
	index, err := session.currentWorkbook().BuildIndex(session.currentWorkbook().Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cellValueServer(t, session.currentWorkbook(), index, 2, 2); got != "9.999999" {
		t.Fatal(got)
	}
	recents := readRecents()
	if len(recents.Items) != 1 || len(recents.Items[0].ArchiveChain) != 1 || recents.Items[0].ArchivePath != path {
		t.Fatalf("nested provenance %+v", recents.Items)
	}
	app.Close()
	reopened, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	response := featureCall(t, reopened, "POST", "/api/recent", `{"action":"open","id":"`+recents.Items[0].ID+`"}`)
	if response.Code != 200 || reopened.currentWorkbook() == nil || reopened.currentWorkbook().SourceInfo.ArchiveMember != "one.csv" {
		t.Fatal("nested recent could not rebuild cleared caches", response.Body.String())
	}
	// A renamed member in a saved chain must not displace the directory the user is browsing.
	previousView, previousBook := reopened.archiveSummary().ID, reopened.currentWorkbook()
	missing := recents.Items[0]
	missing.ArchiveMember = "renamed.csv"
	if _, err := reopened.reopenArchiveRecent(context.Background(), missing); err == nil {
		t.Fatal("missing nested member reopened")
	}
	if reopened.archiveSummary().ID != previousView || reopened.currentWorkbook() != previousBook {
		t.Fatal("failed recent open displaced the active directory or workbook")
	}
}

func TestArchiveCachePinsProtectActiveAndReleaseInactiveMembers(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	path := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(path, zipArchiveFixture(t, map[string][]byte{"one.csv": []byte("ID,amount\n0001,2\n")}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	id := app.archiveSummary().ID
	awaitArchiveFixture(t, app, id, 0)
	app.archiveMu.Lock()
	dir := app.archiveView.file.CacheDir()
	app.archiveMu.Unlock()
	if _, err := app.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("active archive cache removed", err)
	}
	plain := filepath.Join(t.TempDir(), "plain.csv")
	_ = os.WriteFile(plain, []byte("ID,amount\n0002,3\n"), 0600)
	if err := app.OpenPath(plain); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ClearUnusedCaches(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("inactive archive cache was not reclaimed", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("original source archive was removed", err)
	}
}

type blockedArchiveResponse struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
	once             sync.Once
}

func (r *blockedArchiveResponse) Write(b []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return r.ResponseRecorder.Write(b)
}

func TestArchiveDirectoryNetworkWriteDoesNotHoldSessionState(t *testing.T) {
	isolateTestStorage(t)
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	path := filepath.Join(t.TempDir(), "one.zip")
	_ = os.WriteFile(path, zipArchiveFixture(t, map[string][]byte{"one.txt": []byte("A@B\n1@2\n")}), 0600)
	if err := app.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	id := app.archiveSummary().ID
	writer := &blockedArchiveResponse{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		app.handleArchiveList(writer, httptest.NewRequest("GET", "/api/archive/list?id="+id, nil))
		close(done)
	}()
	<-writer.started
	stateDone := make(chan struct{})
	go func() { _ = app.archiveSummary(); close(stateDone) }()
	blocked := false
	select {
	case <-stateDone:
	case <-time.After(time.Second):
		blocked = true
	}
	close(writer.release)
	<-done
	if blocked {
		t.Fatal("slow HTTP client blocked archive state/cancellation")
	}
	awaitArchiveFixture(t, app, id, 0)
	wb := app.currentWorkbook()
	app.closeArchive()
	app.recordRecent(wb)
	if recent := readRecents(); len(recent.Items) != 1 || recent.Items[0].ArchivePath != path {
		t.Fatalf("closing view lost immutable provenance: %+v", recent.Items)
	}
}

func TestArchiveRejectsUnauthorizedParametersAndStaticAssets(t *testing.T) {
	app, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, endpoint := range []string{"/api/archive/list?id=missing", "/api/archive/job?id=missing"} {
		if rawArchiveCall(app, "GET", endpoint, "").Code != 404 {
			t.Fatal(endpoint)
		}
	}
	if rawArchiveCall(app, "POST", "/api/archive/open", `{"archive":"missing","member":-1}`).Code != 404 {
		t.Fatal("bad archive member accepted")
	}
	if rawArchiveCall(app, "POST", "/api/archive/open", `{"path":"C:\\unselected.zip"}`).Code != 404 {
		t.Fatal("client-selected local path accepted")
	}
	req := httptest.NewRequest(http.MethodGet, "/assets/app.js", nil)
	req = localTestConnection(req)
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "/api/archive/open") {
		t.Fatal("archive UI not bundled")
	}
	for _, extension := range []string{".zip", ".tar", ".gz", ".tgz", ".7z", ".bz2", ".xz"} {
		if !workbook.SupportedExtensions(extension) {
			t.Fatal(extension)
		}
	}
}
