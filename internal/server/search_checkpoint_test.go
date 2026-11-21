package server

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
)

func checkpointCall(t *testing.T, a *App, path string, body any, code int) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
	r = localTestConnection(r)
	r.Header.Set("X-ExcelLens-Token", a.token)
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != code {
		t.Fatalf("%s: HTTP %d, want %d: %s", path, w.Code, code, w.Body.String())
	}
	return w
}

func checkpointStart(t *testing.T, a *App, query, mode, scope string) (*SearchJob, string) {
	t.Helper()
	wb := a.currentWorkbook()
	req := searchRequest{Sheet: wb.Sheets[0].Name, Query: query, Mode: mode, Scope: scope, Recoverable: true, WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb)}
	var response struct{ JobID, TaskID string }
	if err := json.Unmarshal(checkpointCall(t, a, "/api/search/start", req, 200).Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.searchJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil || response.TaskID == "" || job.TaskID != response.TaskID {
		t.Fatal("durable search was not registered", response)
	}
	return job, response.TaskID
}

func checkpointWait(t *testing.T, job *SearchJob) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(30 * time.Second):
		job.cancel()
		t.Fatal("search did not terminate")
	}
}

func checkpointManifest(t *testing.T, id string) checkpoint.Manifest {
	t.Helper()
	store, err := openTaskCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.ID == id && entry.Manifest != nil && entry.Error == "" {
			return *entry.Manifest
		}
	}
	t.Fatal("missing confirmed manifest", id, entries)
	return checkpoint.Manifest{}
}

func checkpointResume(t *testing.T, a *App, m checkpoint.Manifest) *SearchJob {
	t.Helper()
	wb := a.currentWorkbook()
	req := taskCheckpointRequest{ID: m.ID, Revision: m.Revision, WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb)}
	var response struct{ JobID string }
	if err := json.Unmarshal(checkpointCall(t, a, "/api/tasks/resume", req, 200).Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.searchJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil {
		t.Fatal("resume did not register search")
	}
	return job
}

func checkpointOpen(t *testing.T, path string) *App {
	t.Helper()
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	return a
}

func checkpointCSV(t *testing.T, rows int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resume.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, "名称,金额")
	for row := 1; row <= rows; row++ {
		name := fmt.Sprintf("ID%09d", row)
		if row == 1000 || row == rows-5 {
			name = "needleABC"
		}
		if row == rows-4 {
			name = "needleabc"
		}
		// Quoted physical newlines must remain a single logical source record.
		if row%997 == 0 {
			name = "\"two\nlines\""
		}
		fmt.Fprintf(w, "%s,9007199254740993.31\n", name)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func checkpointPause(t *testing.T, a *App, query string) checkpoint.Manifest {
	t.Helper()
	job, id := checkpointStart(t, a, query, "advanced", "current")
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := job.checkpoint.lease.Snapshot()
		var cursor searchTaskCursor
		if err := json.Unmarshal(m.Cursor, &cursor); err != nil {
			t.Fatal(err)
		}
		if m.Complete {
			t.Fatal("fixture finished before interruption", cursor)
		}
		if cursor.Next >= formats.CheckpointSearchRows {
			checkpointCall(t, a, "/api/search/cancel", map[string]string{"id": job.ID}, 200)
			checkpointWait(t, job)
			m = checkpointManifest(t, id)
			if m.Complete || job.Status != "cancelled" {
				t.Fatal("cancel published completion", m, job.Status, job.Error)
			}
			return m
		}
		select {
		case <-job.done:
			t.Fatal("search terminated before first checkpoint", job.Status, job.Error)
		default:
		}
		if time.Now().After(deadline) {
			job.cancel()
			t.Fatal("checkpoint did not advance")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSearchCheckpointHTTPReopenResumesCompleteRecordsAndKeepsExactHits(t *testing.T) {
	isolateTestStorage(t)
	file := checkpointCSV(t, 30000)
	before, _ := os.ReadFile(file)
	a := checkpointOpen(t, file)
	m := checkpointPause(t, a, `A~"needleABC"`)
	var saved searchTaskCursor
	if err := json.Unmarshal(m.Cursor, &saved); err != nil || saved.Count != 1 || saved.Next == 0 || saved.Next >= 30001 {
		t.Fatal(saved, err)
	}
	a.Close()
	b := checkpointOpen(t, file)
	wb := b.currentWorkbook()
	if len(b.searchJobs) != 0 {
		t.Fatal("opening automatically resumed work")
	}
	var listing struct{ Items []taskCheckpointInfo }
	if err := json.Unmarshal(featureCall(t, b, "GET", "/api/tasks", "").Body.Bytes(), &listing); err != nil || len(listing.Items) != 1 || listing.Items[0].Busy || listing.Items[0].Complete || len(b.searchJobs) != 0 {
		t.Fatal("listing resumed or lost the task", listing, err)
	}
	job := checkpointResume(t, b, m)
	checkpointWait(t, job)
	if job.Status != "done" || job.Truncated || len(job.Matches) != 2 || job.Matches[0].Row != 1001 || job.Matches[1].Row != 29996 || job.Scanned != 30001 {
		t.Fatal(job.Status, job.Error, job.Matches, job.Scanned)
	}
	idx := wb.GetCachedIndex(wb.Sheets[0].Path)
	want, truncated, err := wb.SearchSheetAdvanced(idx, wb.Sheets[0].Name, `A~"needleABC"`, 500, nil, nil)
	if err != nil || truncated || !reflect.DeepEqual(want, job.Matches) {
		t.Fatal("resumed results differ from ordinary source search", want, job.Matches, err)
	}
	final := checkpointManifest(t, m.ID)
	if !final.Complete || final.Records != 2 || final.Blocks != 2 {
		t.Fatal("existing hits were duplicated in the journal", final)
	}
	checkpointCall(t, b, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, WorkbookKey: wb.Fingerprint, Book: sourceBookID(wb)}, 409)
	// A finished task can be reopened without appending to the journal.
	finished := checkpointResume(t, b, final)
	checkpointWait(t, finished)
	if finished.Status != "done" || !reflect.DeepEqual(finished.Matches, want) || checkpointManifest(t, m.ID).Revision != final.Revision {
		t.Fatal("terminal replay changed the checkpoint", finished.Status, finished.Error)
	}
	after, _ := os.ReadFile(file)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("search recovery changed the source")
	}
}

func TestSearchCheckpointCappedPartialRowKeepsTerminalAndProgressOnReplay(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "wide.csv")
	if err := os.WriteFile(file, []byte(strings.Repeat("needle,", 500)+"needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := checkpointOpen(t, file)
	job, id := checkpointStart(t, a, "needle", "value", "current")
	checkpointWait(t, job)
	if job.Status != "done" || !job.Truncated || len(job.Matches) != 500 || job.Scanned != 1 {
		t.Fatal(job.Status, job.Error, job.Scanned, len(job.Matches))
	}
	m := checkpointManifest(t, id)
	var cursor searchTaskCursor
	if err := json.Unmarshal(m.Cursor, &cursor); err != nil || cursor.Next != 0 || !cursor.Truncated || !m.Complete {
		t.Fatal("partial source row was recorded as a whole record", cursor, err)
	}
	resumed := checkpointResume(t, a, m)
	checkpointWait(t, resumed)
	if resumed.Status != "done" || !resumed.Truncated || resumed.Scanned != 1 || !reflect.DeepEqual(job.Matches, resumed.Matches) || checkpointManifest(t, id).Revision != m.Revision {
		t.Fatal("terminal replay changed the capped state", resumed.Status, resumed.Error, resumed.Scanned)
	}
}

func TestSearchCheckpointAllSharedAdaptersPreserveLeadingZeroAndPreciseText(t *testing.T) {
	for _, tc := range []struct{ file, format, body, query, value string }{
		{"data.csv", "csv", "编号,金额\n001,9007199254740993.31\n002,-2.25\n", "001", "001"},
		{"data.tsv", "tsv", "编号\t金额\n001\t9007199254740993.31\n002\t-2.25\n", "9007199254740993.31", "9007199254740993.31"},
		{"data.txt", "txt", "编号\t金额\n001\t9007199254740993.31\n002\t-2.25\n", "001", "001"},
		{"html.xls", "xls-html", "<html><table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>9007199254740993.31</td></tr><tr><td>002</td><td>-2.25</td></tr></table></html>", "9007199254740993.31", "9007199254740993.31"},
		{"native.xls", "xls", "", "0000000002", "0000000002"},
		{"macro.xlsm", "xlsx", "", "2", "2"},
	} {
		t.Run(tc.format, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), tc.file)
			if tc.format == "xls" {
				printBIFFSource(t, file, 5)
			} else if tc.format == "xlsx" {
				writeMultiSheetWorkbook(t, file)
			} else if err := os.WriteFile(file, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a := checkpointOpen(t, file)
			if a.currentWorkbook().Format != tc.format {
				t.Fatal("incorrect actual adapter", a.currentWorkbook().Format)
			}
			job, id := checkpointStart(t, a, tc.query, "value", "current")
			checkpointWait(t, job)
			if job.Status != "done" || len(job.Matches) != 1 || job.Matches[0].Value != tc.value {
				t.Fatal("source text was reinterpreted", job.Status, job.Error, job.Matches)
			}
			m := checkpointManifest(t, id)
			a.Close()
			b := checkpointOpen(t, file)
			resumed := checkpointResume(t, b, m)
			checkpointWait(t, resumed)
			if resumed.Status != "done" || !reflect.DeepEqual(job.Matches, resumed.Matches) || printScaleHash(t, file) != before {
				t.Fatal("adapter replay changed original results/source", resumed.Status, resumed.Error, resumed.Matches)
			}
		})
	}
}

func TestSearchCheckpointLargeHitFailsWithoutFalseCompletionAndRequestsAreBounded(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "large-cell.csv")
	if err := os.WriteFile(file, []byte(strings.Repeat("x", 3<<20)+"needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a := checkpointOpen(t, file)
	job, id := checkpointStart(t, a, "needle", "value", "current")
	checkpointWait(t, job)
	if job.Status != "error" || !strings.Contains(job.Error, "容量") && !strings.Contains(job.Error, "上限") || checkpointManifest(t, id).Complete {
		t.Fatal("oversized durable hits became success", job.Status, job.Error)
	}
	wb := a.currentWorkbook()
	request := searchRequest{Sheet: wb.Sheets[0].Name, Query: "needle", Mode: "value", Recoverable: true}
	checkpointCall(t, a, "/api/search/start", request, 409)
	request.Book, request.WorkbookKey = sourceBookID(wb), wb.Fingerprint
	request.Query = strings.Repeat("x", 257)
	checkpointCall(t, a, "/api/search/start", request, 400)
	request.Query = "needle"
	checkpointCall(t, a, "/api/search/start", map[string]any{"sheet": request.Sheet, "query": "needle", "unexpected": "field"}, 400)
	checkpointCall(t, a, "/api/tasks/resume", map[string]any{"id": id, "revision": "1", "book": strings.Repeat("x", formats.CheckpointRequestBytes)}, 400)
}

func TestSearchCheckpointWorkbookOrderFormulaReferenceAndCaseRules(t *testing.T) {
	isolateTestStorage(t)
	file := filepath.Join(t.TempDir(), "native.xlsx")
	writeMultiSheetWorkbook(t, file)
	a := checkpointOpen(t, file)
	for _, test := range []struct {
		query, mode string
		hits        int
	}{{"A1", "formula", 2}, {"'First'!A1", "reference", 2}, {"2", "value", 3}, {`A=2`, "advanced", 1}} {
		job, id := checkpointStart(t, a, test.query, test.mode, "all")
		checkpointWait(t, job)
		if job.Status != "done" || len(job.Matches) != test.hits || !checkpointManifest(t, id).Complete {
			t.Fatal(test, job.Status, job.Error, job.Matches)
		}
		if len(job.Matches) > 1 && job.Matches[0].Sheet != "First" {
			t.Fatal("results left workbook source order", job.Matches)
		}
	}
	wb := a.currentWorkbook()
	upper, _ := searchTaskDefinition(wb, searchRequest{Sheet: "First", Query: " A1 ", Mode: "formula"})
	lower, _ := searchTaskDefinition(wb, searchRequest{Sheet: "First", Query: "a1", Mode: "formula"})
	u, _ := checkpoint.DefinitionHash(upper)
	l, _ := checkpoint.DefinitionHash(lower)
	if u != l {
		t.Fatal("ordinary case folding has different durable semantics")
	}
	upper, _ = searchTaskDefinition(wb, searchRequest{Sheet: "First", Query: `A~"ABC"`, Mode: "advanced"})
	lower, _ = searchTaskDefinition(wb, searchRequest{Sheet: "First", Query: `A~"abc"`, Mode: "advanced"})
	u, _ = checkpoint.DefinitionHash(upper)
	l, _ = checkpoint.DefinitionHash(lower)
	if u == l {
		t.Fatal("case-sensitive regular expressions share durable binding")
	}
}

func TestSearchCheckpointQueuedCancellationLeaseAcrossTabsAndStaleDiscard(t *testing.T) {
	isolateTestStorage(t)
	file := checkpointCSV(t, 10)
	a := checkpointOpen(t, file)
	a.querySem <- struct{}{}
	a.querySem <- struct{}{}
	defer func() { <-a.querySem; <-a.querySem }()
	job, id := checkpointStart(t, a, "needle", "value", "current")
	m := checkpointManifest(t, id)
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := child.app.currentWorkbook()
	request := taskCheckpointRequest{ID: id, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}
	checkpointCall(t, child.app, "/api/tasks/resume", request, 409)
	checkpointCall(t, child.app, "/api/tasks/discard", request, 409)
	checkpointCall(t, a, "/api/search/cancel", map[string]string{"id": job.ID}, 200)
	checkpointWait(t, job)
	if job.Status != "cancelled" || len(a.rootApp().sourcePins) != 0 {
		t.Fatal("queued cancellation leaked its source or lease", job.Status)
	}
	resumed := checkpointResume(t, child.app, m)
	resumed.cancel()
	checkpointWait(t, resumed)
	request.Revision++
	checkpointCall(t, child.app, "/api/tasks/discard", request, 409)
	request.Revision = 0
	checkpointCall(t, child.app, "/api/tasks/discard", request, 409)
	request.Revision = m.Revision
	checkpointCall(t, child.app, "/api/tasks/discard", request, 200)
	if entries := featureCall(t, a, "GET", "/api/tasks", "").Body.String(); !strings.Contains(entries, `"items":[]`) {
		t.Fatal(entries)
	}
}

func TestSearchCheckpointRejectsSameBytesAtOtherPathSourceRewriteAndChangedCache(t *testing.T) {
	for _, change := range []string{"path", "source", "cache", "missing-cache", "payload", "semantics", "cursor"} {
		t.Run(change, func(t *testing.T) {
			isolateTestStorage(t)
			file := checkpointCSV(t, 30000)
			a := checkpointOpen(t, file)
			m := checkpointPause(t, a, `A:"needle"`)
			wb := a.currentWorkbook()
			idx := wb.GetCachedIndex(wb.Sheets[0].Path)
			if change == "path" || change == "source" {
				data, _ := os.ReadFile(file)
				path := file
				if change == "path" {
					path = filepath.Join(t.TempDir(), "copy.csv")
				} else {
					data = []byte(strings.Replace(string(data), "needleABC", "NEEDLEABC", 1))
				}
				info, _ := os.Stat(file)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				if change == "source" {
					if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				}
				if err := a.OpenPath(path); err != nil {
					t.Fatal(err)
				}
				wb = a.currentWorkbook()
				checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, 409)
				return
			}
			if change == "payload" {
				base, _ := os.UserConfigDir()
				path := filepath.Join(base, "ExcelLens", "task-checkpoints", m.ID, "blocks")
				data, _ := os.ReadFile(path)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, 422)
				return
			}
			if change == "semantics" || change == "cursor" {
				store, err := openTaskCheckpoints()
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				if change == "semantics" {
					def := m.Definition
					def.Semantics++
					lease, err := store.Create(context.Background(), def, "prepare", json.RawMessage(`{}`))
					if err != nil {
						t.Fatal(err)
					}
					other := lease.Snapshot()
					_ = lease.Close()
					checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: other.ID, Revision: other.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, 409)
					return
				}
				lease, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash)
				if err != nil {
					t.Fatal(err)
				}
				var cursor searchTaskCursor
				_ = json.Unmarshal(m.Cursor, &cursor)
				cursor.Next = -1
				data, _ := json.Marshal(cursor)
				m, err = lease.Advance(context.Background(), checkpoint.Advance{Revision: m.Revision, Stage: m.Stage, Cursor: data}, nil)
				if err != nil {
					t.Fatal(err)
				}
				_ = lease.Close()
			} else if change == "cache" {
				info, _ := os.Stat(idx.XMLPath)
				data, _ := os.ReadFile(idx.XMLPath)
				data[len(data)-1] ^= 1
				if err := os.WriteFile(idx.XMLPath, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(idx.XMLPath, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(idx.XMLPath); err != nil {
				t.Fatal(err)
			}
			job := checkpointResume(t, a, m)
			checkpointWait(t, job)
			if job.Status != "error" || job.Error == "" || checkpointManifest(t, m.ID).Complete {
				t.Fatal("changed or invalid recovery published completion", change, job.Status, job.Error)
			}
		})
	}
}
