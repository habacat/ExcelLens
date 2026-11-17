//go:build windows

package server

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSearchCheckpointWindowsSQLiteAndGB18030UseExistingReaders(t *testing.T) {
	for _, tc := range []struct{ name, format, query, value string }{
		{"ledger.db", "sqlite", "0000000123456789", "0000000123456789"},
		{"accounting-gbk.txt", "txt", "现金", "现金"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateTestStorage(t)
			data, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a := checkpointOpen(t, file)
			if a.currentWorkbook().Format != tc.format {
				t.Fatal(a.currentWorkbook().Format)
			}
			job, id := checkpointStart(t, a, tc.query, "value", "all")
			checkpointWait(t, job)
			if job.Status != "done" || len(job.Matches) != 1 || job.Matches[0].Value != tc.value {
				t.Fatal(job.Status, job.Error, job.Matches)
			}
			m := checkpointManifest(t, id)
			a.Close()
			b := checkpointOpen(t, file)
			resumed := checkpointResume(t, b, m)
			checkpointWait(t, resumed)
			if resumed.Status != "done" || !reflect.DeepEqual(resumed.Matches, job.Matches) || printScaleHash(t, file) != before {
				t.Fatal("source/adapter changed on durable replay", resumed.Status, resumed.Error, resumed.Matches)
			}
		})
	}
}
