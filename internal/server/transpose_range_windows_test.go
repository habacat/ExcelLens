//go:build windows

package server

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransposeRangeWindowsSQLiteAndGB18030(t *testing.T) {
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
			a, meta := recordOpen(t, file)
			if name == "ledger.db" {
				for _, candidate := range a.currentWorkbook().Sheets {
					if strings.HasPrefix(candidate.Name, "财务") {
						meta = candidate
						break
					}
				}
			}
			wb := a.currentWorkbook()
			if _, err := wb.BuildIndex(meta, nil, nil); err != nil {
				t.Fatal(err)
			}
			params := url.Values{"sheet": {meta.Name}, "start": {"A1"}, "end": {"B2"}, "format": {"json"}}
			p := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?"+params.Encode(), ""))
			if p.Book != sourceBookID(wb) || p.Sheet != meta.Name || len(p.Rows) != 2 || len(p.Rows[1].Cells) != 2 {
				t.Fatal("source/row binding lost", p)
			}
			if printScaleHash(t, file) != printScaleHash(t, filepath.Join("..", "workbook", "testdata", name)) {
				t.Fatal("source changed")
			}
		})
	}
}
