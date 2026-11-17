//go:build windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileDistributionWindowsSQLiteAndGB18030(t *testing.T) {
	for _, name := range []string{"ledger.db", "accounting-gbk.txt"} {
		t.Run(name, func(t *testing.T) {
			isolateTestStorage(t)
			data, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), name)
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			if name == "ledger.db" {
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
			req := profileSource(t, a, meta.Name, "A2", "A2", rowViewRequest{})
			r := profileOK(t, a, req)
			assertProfileDistribution(t, r, nil)
			if name == "ledger.db" {
				req.Start, req.End = "B2", "B2"
				assertProfileDistribution(t, profileOK(t, a, req), []string{"100000000000.123456"})
				req.Start, req.End = "C2", "D2"
				r = profileOK(t, a, req)
				assertProfileDistribution(t, r, nil)
				if r.Text != 2 {
					t.Fatal("NULL/BLOB state changed", r)
				}
			} else if len(r.TopValues) != 1 || r.TopValues[0].Value != "现金" {
				t.Fatal(r)
			}
			if before != printScaleHash(t, file) {
				t.Fatal("source changed")
			}
		})
	}
}
