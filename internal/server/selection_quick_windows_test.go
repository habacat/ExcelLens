//go:build windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectionQuickWindowsSQLiteAndGB18030Adapters(t *testing.T) {
	for _, fixture := range []string{"ledger.db", "accounting-gbk.txt"} {
		t.Run(fixture, func(t *testing.T) {
			isolateTestStorage(t)
			original, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", fixture))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), fixture)
			if err := os.WriteFile(file, original, 0600); err != nil {
				t.Fatal(err)
			}
			a, meta := recordOpen(t, file)
			if fixture == "ledger.db" {
				for _, candidate := range a.currentWorkbook().Sheets {
					if strings.HasPrefix(candidate.Name, "财务") {
						meta = candidate
						break
					}
				}
				if _, err := a.currentWorkbook().BuildIndex(meta, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			req := quickRequest(t, a, meta.Name, rowViewRequest{}, "A2", "B2")
			r := quickOK(t, a, req)
			if r.Rows != 1 || r.Columns[0].Counts.Other != 1 || r.Columns[1].Counts.Number != 1 {
				t.Fatal("source type/identifier semantics changed", r)
			}
			if fixture == "ledger.db" && r.Columns[1].Summary.Sum != "100000000000.123456" {
				t.Fatal("SQLite decimal changed", r.Columns[1])
			}
			after, err := os.ReadFile(file)
			if err != nil || string(after) != string(original) {
				t.Fatal("source changed", err)
			}
		})
	}
}
