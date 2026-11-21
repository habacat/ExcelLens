package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/workbook"
)

func taskOpenFixture(t *testing.T) (*App, checkpoint.Manifest) {
	t.Helper()
	isolateTestStorage(t)
	a := checkpointOpen(t, checkpointCSV(t, 20))
	j, id := checkpointStart(t, a, "needle", "value", "current")
	checkpointWait(t, j)
	return a, checkpointManifest(t, id)
}

func TestTaskOpenValidatesBeforeReplacingAndDoesNotResume(t *testing.T) {
	a, m := taskOpenFixture(t)
	a.Close()
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var listed struct{ Items []taskCheckpointInfo }
	w := featureCall(t, b, "GET", "/api/tasks", "")
	if json.Unmarshal(w.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 || b.currentWorkbook() != nil || len(b.indexJobs) != 0 {
		t.Fatal("listing opened source", w.Body.String())
	}
	w = checkpointCall(t, b, "/api/tasks/open", taskCheckpointRequest{ID: m.ID, Revision: m.Revision}, 200)
	if b.currentWorkbook() == nil || len(b.searchJobs) != 0 || len(b.indexJobs) != 0 || len(b.filterJobs) != 0 {
		t.Fatal("opening ran a task", w.Body.String())
	}
	j := checkpointResume(t, b, m)
	checkpointWait(t, j)
	if j.Status != "done" || len(j.Matches) != 2 {
		t.Fatal("opened task could not resume", j.Status, j.Error, j.Matches)
	}
}

func TestTaskOpenFailureLeavesCurrentWorkbookAndResults(t *testing.T) {
	a, m := taskOpenFixture(t)
	current := a.currentWorkbook()
	other := checkpointOpen(t, filterCheckpointCSV(t, 3)).currentWorkbook()
	a.replaceWorkbook(other)
	req := taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(other), WorkbookKey: other.Fingerprint}
	stale := req
	stale.Revision++
	checkpointCall(t, a, "/api/tasks/open", stale, 409)
	wrong := req
	wrong.Book = "another-intent"
	checkpointCall(t, a, "/api/tasks/open", wrong, 409)
	if err := os.WriteFile(current.Path, []byte("changed,source\n1,2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	checkpointCall(t, a, "/api/tasks/open", req, 409)
	if a.currentWorkbook() != other {
		t.Fatal("failed verification replaced current workbook")
	}
	if err := os.Remove(current.Path); err != nil {
		t.Fatal(err)
	}
	checkpointCall(t, a, "/api/tasks/open", req, 422)
	if a.currentWorkbook() != other {
		t.Fatal("missing source replaced current workbook")
	}
}

func TestTaskOpenRejectsLateCandidateAndCancelledRequest(t *testing.T) {
	a, m := taskOpenFixture(t)
	old := a.currentWorkbook()
	req := taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(old), WorkbookKey: old.Fingerprint}
	data, _ := json.Marshal(req)
	r := httptest.NewRequest("POST", "/api/tasks/open", strings.NewReader(string(data)))
	r.Header.Set("X-ExcelLens-Token", a.token)
	r = localTestConnection(r)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r.WithContext(ctx))
	if w.Code == 200 || a.currentWorkbook() != old {
		t.Fatal("cancelled open changed source", w.Code)
	}
	// Hold the shared cache lock so candidate opening cannot complete. Switch
	// the current workbook after this request has captured its expected source.
	a.cacheMu.Lock()
	locked := true
	defer func() {
		if locked {
			a.cacheMu.Unlock()
		}
	}()
	w = httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest("POST", "/api/tasks/open", strings.NewReader(string(data)))
		r.Header.Set("X-ExcelLens-Token", a.token)
		r = localTestConnection(r)
		a.Handler().ServeHTTP(w, r)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(a.rootApp().recoverySem) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("open did not enter admission")
		}
		time.Sleep(time.Millisecond)
	}
	other := &workbook.Workbook{Path: filepath.Join(t.TempDir(), "new.csv"), Fingerprint: "new-source"}
	a.replaceWorkbook(other)
	a.cacheMu.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("late candidate did not finish")
	}
	if w.Code != 409 || a.currentWorkbook() != other {
		t.Fatal("late candidate replaced newer source", w.Code, w.Body.String())
	}
}

func TestTaskOpenRestoresUploadedHTMLNameAndFilterEnvelope(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "ExcelLens-upload-random.xls")
	if err := os.WriteFile(file, []byte(`<html><body><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>12.30</td></tr></table></body></html>`), 0600); err != nil {
		t.Fatal(err)
	}
	a := checkpointOpen(t, file)
	a.currentWorkbook().SetDisplayName("原始账簿.xls")
	j := filterCheckpointStart(t, a, filterRequest{Rules: []workbook.FilterRule{{Column: 1, Operator: "notBlank"}}, HeaderRow: 1, SortKeys: []workbook.SortKey{{Column: 2, Mode: "number", Descending: true}}})
	filterCheckpointWait(t, j)
	m := checkpointManifest(t, j.TaskID)
	a.Close()
	b, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	w := checkpointCall(t, b, "/api/tasks/open", taskCheckpointRequest{ID: m.ID, Revision: m.Revision}, 200)
	var opened struct{ FileName string }
	if json.Unmarshal(w.Body.Bytes(), &opened) != nil || opened.FileName != "原始账簿.xls" || b.currentWorkbook().Sheets[0].Name != "原始账簿" {
		t.Fatal("lost uploaded name", w.Body.String())
	}
	var resumed struct {
		Operation string
		Filter    filterSpec
	}
	w = checkpointCall(t, b, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(b.currentWorkbook()), WorkbookKey: b.currentWorkbook().Fingerprint}, 200)
	if json.Unmarshal(w.Body.Bytes(), &resumed) != nil || resumed.Operation != "filter" || resumed.Filter.HeaderRow != 1 || len(resumed.Filter.SortKeys) != 1 || len(resumed.Filter.Rules) != 1 {
		t.Fatal("missing committed filter rules", w.Body.String())
	}
}
