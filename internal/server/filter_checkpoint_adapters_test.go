package server

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/checkpoint"
	"excellens/internal/workbook"
)

func filterCheckpointCompareAdapter(t *testing.T, file string) {
	t.Helper()
	before := printScaleHash(t, file)
	a := checkpointOpen(t, file)
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := filterRequest{HeaderRow: 1, Rules: []workbook.FilterRule{{Column: 1, Operator: "notBlank"}}, SortKeys: []workbook.SortKey{{Column: 2, Mode: "number", Descending: true}, {Column: 1, Mode: "text"}}}
	ordinary, err := wb.FilterRowOrder(context.Background(), idx, req.Rules, req.options(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var want []int
	for position := range ordinary.Iter() {
		want = append(want, position)
	}
	_ = ordinary.Close()
	job := filterCheckpointStart(t, a, req)
	if got := filterCheckpointPositions(t, a, job); !reflect.DeepEqual(got, want) {
		t.Fatal("adapter durable filter/sort changed established semantics", wb.Format, got, want)
	}
	final := checkpointManifest(t, job.TaskID)
	a.Close()
	b := checkpointOpen(t, file)
	resumed := filterCheckpointResume(t, b, final)
	if got := filterCheckpointPositions(t, b, resumed); !reflect.DeepEqual(got, want) || printScaleHash(t, file) != before {
		t.Fatal("adapter reopen changed row order or original bytes", wb.Format, got, want)
	}
}

func TestFilterCheckpointRealTextHTMLOOXMLAndBIFFAdapters(t *testing.T) {
	for _, test := range []struct{ file, body string }{
		{"data.csv", "编号,金额\n001,9007199254740993.31\n002,9007199254740993.30\n"},
		{"data.tsv", "编号\t金额\n001\t9007199254740993.31\n002\t9007199254740993.30\n"},
		{"data.txt", "编号\t金额\n001\t9007199254740993.31\n002\t9007199254740993.30\n"},
		{"html.xls", "<table><tr><td>编号</td><td>金额</td></tr><tr><td>001</td><td>9007199254740993.31</td></tr><tr><td>002</td><td>9007199254740993.30</td></tr></table>"},
		{"native.xls", ""}, {"source.xlsx", ""}, {"source.xlsm", ""},
	} {
		t.Run(test.file, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), test.file)
			if test.file == "native.xls" {
				printBIFFSource(t, file, 5)
			} else if test.body == "" {
				writeMultiSheetWorkbook(t, file)
			} else if err := os.WriteFile(file, []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			filterCheckpointCompareAdapter(t, file)
		})
	}
}

func TestFilterCheckpointRejectsChangedSourceCachePayloadAndUnknownSemantics(t *testing.T) {
	for _, change := range []string{"path", "source", "cache", "missing-cache", "payload", "semantics", "cursor"} {
		t.Run(change, func(t *testing.T) {
			isolateTestStorage(t)
			file := filterCheckpointCSV(t, 60000)
			a := checkpointOpen(t, file)
			m := filterCheckpointPause(t, a, filterCheckpointRequest(true))
			wb := a.currentWorkbook()
			idx := wb.GetCachedIndex(wb.Sheets[0].Path)
			if change == "path" || change == "source" {
				body, _ := os.ReadFile(file)
				path := file
				if change == "path" {
					path = filepath.Join(t.TempDir(), "same-bytes.csv")
				} else {
					body = []byte(strings.Replace(string(body), "keep", "KEEP", 1))
				}
				info, _ := os.Stat(file)
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
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
					lease, err := store.Create(context.Background(), def, "prepare", []byte(`{}`))
					if err != nil {
						t.Fatal(err)
					}
					m = lease.Snapshot()
					_ = lease.Close()
				} else {
					lease, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash)
					if err != nil {
						t.Fatal(err)
					}
					m, err = lease.Advance(context.Background(), checkpoint.Advance{Revision: m.Revision, Stage: "filter", Cursor: []byte(`{"index":"bad","rows":1,"next":-1,"matched":0,"filterComplete":false,"orderWritten":0}`)}, nil)
					_ = lease.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				status := 409
				if change == "cursor" {
					status = 422
				}
				checkpointCall(t, a, "/api/tasks/resume", taskCheckpointRequest{ID: m.ID, Revision: m.Revision, Book: sourceBookID(wb), WorkbookKey: wb.Fingerprint}, status)
				return
			}
			if change == "cache" {
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
			job := filterCheckpointResume(t, a, m)
			filterCheckpointWait(t, job)
			if job.Status != "error" || job.Error == "" || checkpointManifest(t, m.ID).Complete {
				t.Fatal("changed source cache became successful recovery", change, job.Status, job.Error)
			}
		})
	}
}
