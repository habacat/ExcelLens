package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func filterCheckpointCSV(t *testing.T, records int) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "filter.csv")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := bufio.NewWriter(f)
	fmt.Fprintln(writer, "编号,金额,分组")
	for row := 1; row <= records; row++ {
		group := "keep"
		if row%7 == 0 {
			group = "drop"
		}
		fmt.Fprintf(writer, "000%09d,9007199254740993.%02d,%s\n", row, row%17, group)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func filterCheckpointStart(t *testing.T, a *App, req filterRequest) *FilterJob {
	t.Helper()
	wb := a.currentWorkbook()
	if req.Sheet == "" {
		req.Sheet = wb.Sheets[0].Name
	}
	req.Book, req.WorkbookKey, req.Recoverable = sourceBookID(wb), wb.Fingerprint, true
	var response struct{ JobID, TaskID string }
	if err := json.Unmarshal(checkpointCall(t, a, "/api/filter/start", req, 200).Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.filterJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil || job.TaskID != response.TaskID || response.TaskID == "" {
		t.Fatal("durable filter was not registered", response)
	}
	return job
}

func filterCheckpointWait(t *testing.T, job *FilterJob) {
	t.Helper()
	select {
	case <-job.done:
	case <-time.After(45 * time.Second):
		job.cancel()
		t.Fatal("durable filter did not terminate")
	}
}

func filterCheckpointResume(t *testing.T, a *App, m checkpoint.Manifest) *FilterJob {
	t.Helper()
	wb := a.currentWorkbook()
	var response struct{ JobID, Operation string }
	if err := json.Unmarshal(checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, 200).Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	a.mu.RLock()
	job := a.filterJobs[response.JobID]
	a.mu.RUnlock()
	if job == nil || response.Operation != "filter" || job.TaskID != m.ID {
		t.Fatal("filter task resumed as the wrong operation", response)
	}
	return job
}

func filterCheckpointPause(t *testing.T, a *App, req filterRequest) checkpoint.Manifest {
	t.Helper()
	job := filterCheckpointStart(t, a, req)
	deadline := time.Now().Add(30 * time.Second)
	for {
		m := job.checkpoint.lease.Snapshot()
		var cursor filterTaskCursor
		if err := json.Unmarshal(m.Cursor, &cursor); err != nil {
			t.Fatal(err)
		}
		if cursor.Next >= formats.CheckpointFilterRows && !cursor.FilterComplete {
			checkpointCall(t, a, "/api/filter/cancel", map[string]string{"id": job.ID}, 200)
			filterCheckpointWait(t, job)
			if job.Status != "cancelled" {
				t.Fatal("filter interruption became success", job.Status, job.Error)
			}
			return checkpointManifest(t, job.TaskID)
		}
		select {
		case <-job.done:
			t.Fatal("filter finished before the source-record interruption", job.Status, job.Error, cursor)
		default:
		}
		if time.Now().After(deadline) {
			job.cancel()
			t.Fatal("filter never confirmed a record checkpoint")
		}
		time.Sleep(time.Millisecond)
	}
}

func filterCheckpointPositions(t *testing.T, a *App, job *FilterJob) []int {
	t.Helper()
	filterCheckpointWait(t, job)
	if job.Status != "done" {
		t.Fatal("filter failed", job.Status, job.Stage, job.Error)
	}
	result := a.filterSnapshot(job.ResultID)
	if result == nil {
		t.Fatal("completed filter was not published")
	}
	defer result.close()
	positions := make([]int, 0, result.Rows.Len())
	for position := range result.Rows.Iter() {
		positions = append(positions, position)
	}
	return positions
}

func filterCheckpointExpected(records int, sorted bool) []int {
	positions := []int{0}
	for position := 1; position <= records; position++ {
		if position%7 != 0 {
			positions = append(positions, position)
		}
	}
	if sorted {
		sort.Slice(positions[1:], func(i, j int) bool {
			left, right := positions[i+1], positions[j+1]
			if left%17 != right%17 {
				return left%17 > right%17
			}
			return left < right
		})
	}
	return positions
}

func filterCheckpointRequest(sorted bool) filterRequest {
	req := filterRequest{HeaderRow: 1, Rules: []workbook.FilterRule{{Column: 3, Operator: "equals", Value: "keep"}}}
	if sorted {
		req.SortKeys = []workbook.SortKey{{Column: 2, Mode: "number", Descending: true}, {Column: 1, Mode: "natural"}}
	}
	return req
}

func TestFilterCheckpointHTTPReopenResumesActualSourceAndKeepsConsumerLeases(t *testing.T) {
	isolateTestStorage(t)
	file := filterCheckpointCSV(t, 60000)
	before := printScaleHash(t, file)
	a := checkpointOpen(t, file)
	m := filterCheckpointPause(t, a, filterCheckpointRequest(false))
	a.Close()
	b := checkpointOpen(t, file)
	var listing struct{ Items []taskCheckpointInfo }
	if err := json.Unmarshal(featureCall(t, b, "GET", "/api/tasks", "").Body.Bytes(), &listing); err != nil || len(listing.Items) != 1 || listing.Items[0].Driver != filterCheckpointDriver || listing.Items[0].Error != "" || len(b.filterJobs) != 0 {
		t.Fatal("listing started or lost a filter task", listing, err)
	}
	job := filterCheckpointResume(t, b, m)
	got := filterCheckpointPositions(t, b, job)
	want := filterCheckpointExpected(60000, false)
	if !reflect.DeepEqual(got, want) || job.Scanned != 60001 || job.Total != 60001 || printScaleHash(t, file) != before {
		t.Fatal("source order/count/bytes changed after recovery", len(got), len(want), job.Scanned)
	}
	final := checkpointManifest(t, m.ID)
	if !final.Complete || final.Records != int64(len(want)) {
		t.Fatal(final)
	}
	consumer := b.filterSnapshot(job.ResultID)
	defer consumer.close()
	checkpointCall(t, b, "/api/filter/clear", map[string]string{"id": job.ResultID}, 200)
	if consumer.Rows.Len() != len(want) || consumer.Rows.At(len(want)-1) != want[len(want)-1] {
		t.Fatal("clearing a recovered grid invalidated a borrowed row order")
	}
	finished := filterCheckpointResume(t, b, final)
	if !reflect.DeepEqual(filterCheckpointPositions(t, b, finished), want) || checkpointManifest(t, m.ID).Revision != final.Revision {
		t.Fatal("completed replay rescanned or rewrote the task")
	}
}

func TestFilterCheckpointExactSortedOutputAndCompletedReopen(t *testing.T) {
	isolateTestStorage(t)
	file := filterCheckpointCSV(t, 20000)
	a := checkpointOpen(t, file)
	req := filterCheckpointRequest(true)
	job := filterCheckpointStart(t, a, req)
	got := filterCheckpointPositions(t, a, job)
	want := filterCheckpointExpected(20000, true)
	if !reflect.DeepEqual(got, want) || len(got) <= 10000 {
		t.Fatal("large sorted stage changed exact ordering", len(got), len(want))
	}
	final := checkpointManifest(t, job.TaskID)
	if final.Records != int64(2*len(want)) || !final.Complete {
		t.Fatal("both complete stages were not retained", final)
	}
	a.Close()
	b := checkpointOpen(t, file)
	resumed := filterCheckpointResume(t, b, final)
	if !reflect.DeepEqual(filterCheckpointPositions(t, b, resumed), want) || checkpointManifest(t, final.ID).Revision != final.Revision {
		t.Fatal("reopening a completed sorted stage changed the result")
	}
}

// Seed complete stage records independently from the task runner. This makes
// sorting-prefix interruption/validation deterministic even on very fast disks.
func filterCheckpointSeed(t *testing.T, a *App, req filterRequest, sourcePositions, sortedPrefix []int, next int, filterComplete bool) checkpoint.Manifest {
	t.Helper()
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	def, err := filterTaskDefinition(wb, meta.Name, newFilterSpec(req.Rules, req.options()))
	if err != nil {
		t.Fatal(err)
	}
	store, err := openTaskCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	initial, _ := json.Marshal(filterTaskCursor{})
	lease, err := store.Create(context.Background(), def, "prepare", initial)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	stamp, err := wb.SourceIndexStamp(context.Background(), meta, idx)
	if err != nil {
		t.Fatal(err)
	}
	cursor := filterTaskCursor{Index: stamp, Rows: idx.RowCount(), Next: next, Matched: len(sourcePositions), FilterComplete: filterComplete}
	payload, err := encodeFilterPositions(sourcePositions)
	if err != nil {
		t.Fatal(err)
	}
	m := lease.Snapshot()
	stage := "filter"
	if filterComplete {
		stage = "sort"
	}
	data, _ := json.Marshal(cursor)
	m, err = lease.Advance(context.Background(), checkpoint.Advance{Revision: m.Revision, Stage: stage, Cursor: data, Records: int64(len(sourcePositions)), Bytes: int64(len(payload))}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(sortedPrefix) > 0 {
		cursor.OrderWritten = len(sortedPrefix)
		payload, err = encodeFilterPositions(sortedPrefix)
		if err != nil {
			t.Fatal(err)
		}
		data, _ = json.Marshal(cursor)
		m, err = lease.Advance(context.Background(), checkpoint.Advance{Revision: m.Revision, Stage: "sort", Cursor: data, Records: int64(len(sortedPrefix)), Bytes: int64(len(payload))}, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestFilterCheckpointSortRebuildConfirmsExistingPrefixAndRejectsFalsePermutation(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, 15000)
			a := checkpointOpen(t, file)
			want := filterCheckpointExpected(15000, true)
			prefix := append([]int{}, want[:4096]...)
			if wrong {
				prefix[2], prefix[3] = prefix[3], prefix[2]
			}
			m := filterCheckpointSeed(t, a, filterCheckpointRequest(true), filterCheckpointExpected(15000, false), prefix, 15001, true)
			a.Close()
			b := checkpointOpen(t, file)
			job := filterCheckpointResume(t, b, m)
			filterCheckpointWait(t, job)
			if wrong {
				if job.Status != "error" || job.Error == "" || checkpointManifest(t, m.ID).Revision != m.Revision {
					t.Fatal("incorrect committed sorted prefix was accepted", job.Status, job.Error)
				}
			} else if !reflect.DeepEqual(filterCheckpointPositions(t, b, job), want) || !checkpointManifest(t, m.ID).Complete {
				t.Fatal("sort did not rebuild from the complete filter stage")
			}
		})
	}
}

func TestFilterCheckpointRejectsInvalidPositionShapesAndPreservesOldResult(t *testing.T) {
	for _, malformed := range []string{"duplicate-filter", "missing-header", "out-of-range", "sorted-not-member", "duplicate-sorted"} {
		t.Run(malformed, func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, 100)
			a := checkpointOpen(t, file)
			req := filterCheckpointRequest(true)
			old := filterCheckpointStart(t, a, req)
			want := filterCheckpointPositions(t, a, old)
			positions := filterCheckpointExpected(100, false)
			var prefix []int
			switch malformed {
			case "duplicate-filter":
				positions[3] = positions[2]
			case "missing-header":
				positions = positions[1:]
			case "out-of-range":
				positions[len(positions)-1] = 101
			case "sorted-not-member":
				prefix = []int{0, 7}
			case "duplicate-sorted":
				prefix = []int{0, 1, 1}
			}
			m := filterCheckpointSeed(t, a, req, positions, prefix, 101, true)
			job := filterCheckpointResume(t, a, m)
			filterCheckpointWait(t, job)
			if job.Status != "error" || job.ResultID != "" || checkpointManifest(t, m.ID).Complete || !reflect.DeepEqual(filterCheckpointPositions(t, a, old), want) {
				t.Fatal("invalid row positions replaced a complete grid", malformed, job.Status, job.Error)
			}
		})
	}
}

func TestFilterCheckpointQueuedLeaseAcrossTabsCancellationAndStrictRequests(t *testing.T) {
	isolateTestStorage(t)
	file := filterCheckpointCSV(t, 20)
	a := checkpointOpen(t, file)
	a.querySem <- struct{}{}
	a.querySem <- struct{}{}
	defer func() { <-a.querySem; <-a.querySem }()
	job := filterCheckpointStart(t, a, filterCheckpointRequest(false))
	m := checkpointManifest(t, job.TaskID)
	child, err := a.sessionFor(randomID())
	if err != nil {
		t.Fatal(err)
	}
	if err := child.app.OpenPath(file); err != nil {
		t.Fatal(err)
	}
	wb := child.app.currentWorkbook()
	req := taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}
	checkpointCall(t, child.app, "/api/tasks/resume", req, 409)
	checkpointCall(t, child.app, "/api/tasks/discard", req, 409)
	checkpointCall(t, a, "/api/filter/cancel", map[string]string{"id": job.ID}, 200)
	filterCheckpointWait(t, job)
	resumed := filterCheckpointResume(t, child.app, m)
	resumed.cancel()
	filterCheckpointWait(t, resumed)
	if resumed.Status != "cancelled" {
		t.Fatal(resumed.Status, resumed.Error)
	}
	checkpointCall(t, child.app, "/api/tasks/discard", req, 200)
	bad := filterCheckpointRequest(false)
	bad.Recoverable, bad.Sheet = true, wb.Sheets[0].Name
	checkpointCall(t, child.app, "/api/filter/start", bad, 409)
	bad.Book, bad.WorkbookKey = sourceBookID(wb), wb.Fingerprint
	bad.Rules[0].Operator = "unknown"
	checkpointCall(t, child.app, "/api/filter/start", bad, 400)
	r := httptest.NewRequest("GET", "/api/tasks", nil)
	w := httptest.NewRecorder()
	a.handleTaskCheckpoints(w, r)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
}

func TestFilterCheckpointEmptyMatchesHeadersOnlyAndSortOnlyRemainComplete(t *testing.T) {
	for _, test := range []struct {
		name             string
		header           int
		sortOnly, sorted bool
		count            int
	}{
		{"no-matches", 0, false, false, 0}, {"empty-sorted", 0, false, true, 0},
		{"header-only", 1, false, true, 1}, {"all-headers", 1000, false, true, 38},
		{"sort-only", 1, true, true, 38},
	} {
		t.Run(test.name, func(t *testing.T) {
			isolateTestStorage(t)
			a := checkpointOpen(t, filterCheckpointCSV(t, 37))
			req := filterRequest{HeaderRow: test.header, Rules: []workbook.FilterRule{{Column: 1, Operator: "equals", Value: "not-present"}}}
			if test.sorted {
				req.SortKeys = []workbook.SortKey{{Column: 1, Mode: "natural"}}
			}
			if test.sortOnly {
				req.Rules = nil
			}
			job := filterCheckpointStart(t, a, req)
			got := filterCheckpointPositions(t, a, job)
			if len(got) != test.count {
				t.Fatal(test, got)
			}
			for i, position := range got {
				if position != i {
					t.Fatal("retained source headers/order changed", got)
				}
			}
			m := checkpointManifest(t, job.TaskID)
			if !m.Complete {
				t.Fatal(m)
			}
			resumed := filterCheckpointResume(t, a, m)
			if !reflect.DeepEqual(filterCheckpointPositions(t, a, resumed), got) || checkpointManifest(t, m.ID).Revision != m.Revision {
				t.Fatal("terminal zero/header stage was rewritten")
			}
		})
	}
}
