//go:build windows

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCellExplanationWindowsSQLiteAndGB18030(t *testing.T) {
	for _, name := range []string{"ledger.db", "accounting-gbk.txt"} {
		t.Run(name, func(t *testing.T) {
			isolateTestStorage(t)
			data, err := os.ReadFile(filepath.Join("..", "workbook", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), name)
			if err = os.WriteFile(file, data, 0600); err != nil {
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
			req := cellExplanationSource(t, a, meta.Name, "A2", rowViewRequest{})
			id := cellExplanationOK(t, a, req)
			if id.Numeric.Eligible {
				t.Fatal("identifier/text counted", id)
			}
			if name == "ledger.db" {
				req.Ref = "B2"
				amount := cellExplanationOK(t, a, req)
				if !amount.Numeric.Eligible || amount.Numeric.Exact != "100000000000.123456" {
					t.Fatal(amount)
				}
				for _, ref := range []string{"C2", "D2"} {
					req.Ref = ref
					r := cellExplanationOK(t, a, req)
					if r.Numeric.Eligible || r.Cell.Raw == "" {
						t.Fatal("NULL/BLOB state lost", r)
					}
				}
			} else if id.Cell.Raw != "现金" {
				t.Fatal(id.Cell)
			}
			if before != printScaleHash(t, file) {
				t.Fatal("source changed")
			}
		})
	}
}
