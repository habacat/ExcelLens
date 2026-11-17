package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestTransposeRangeActualMillionAndCompleteBIFF(t *testing.T) {
	if os.Getenv("EXCELLENS_TRANSPOSE_SCALE") != "1" {
		t.Skip("set EXCELLENS_TRANSPOSE_SCALE=1 for actual source scale")
	}
	var results []map[string]any
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "scale."+kind)
			rows := 65536
			if kind == "xls" {
				printBIFFSource(t, file, rows)
			} else {
				rows = 1000001
				f, err := os.Create(file)
				if err != nil {
					t.Fatal(err)
				}
				w := bufio.NewWriterSize(f, 256<<10)
				fmt.Fprintln(w, "编号,金额")
				for n := 1; n < rows; n++ {
					fmt.Fprintf(w, "%09d,9007199254740993.31\n", n)
				}
				if err := w.Flush(); err != nil {
					t.Fatal(err)
				}
				if err := f.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			wb := a.currentWorkbook()
			first := rows - 199
			params := url.Values{"sheet": {meta.Name}, "start": {"A" + strconv.Itoa(first)}, "end": {"B" + strconv.Itoa(rows)}, "format": {"json"}, "book": {sourceBookID(wb)}, "workbookKey": {wb.Fingerprint}}
			p := decodeResponse[clipboardPayload](t, analysisRequest(t, a, "GET", "/api/range?"+params.Encode(), ""))
			if len(p.Rows) != 200 || p.Book != sourceBookID(wb) {
				t.Fatal("wrong bounded page", len(p.Rows))
			}
			for at, row := range p.Rows {
				n := first + at
				if row.Row != n || len(row.Cells) != 2 || row.Cells[0].Ref != "A"+strconv.Itoa(n) || row.Cells[1].Ref != "B"+strconv.Itoa(n) {
					t.Fatal("coordinates changed", row)
				}
				if kind == "csv" && (row.Cells[0].Raw != fmt.Sprintf("%09d", n-1) || row.Cells[1].Raw != "9007199254740993.31") {
					t.Fatal("values rounded", row)
				}
				if kind == "xls" && (row.Cells[0].Raw != fmt.Sprintf("%010d", n) || row.Cells[1].Raw != strconv.Itoa(n)+".5") {
					t.Fatal("native values changed", row)
				}
			}
			if printScaleHash(t, file) != before {
				t.Fatal("source changed")
			}
			results = append(results, map[string]any{"format": kind, "sourceRows": rows, "selectedRows": len(p.Rows), "first": first, "last": rows, "sourceSHA256": before, "sourceUnchanged": true})
		})
	}
	if report := os.Getenv("EXCELLENS_TRANSPOSE_REPORT"); report != "" && !t.Failed() {
		data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "scope": "Actual million CSV and complete BIFF source, 400 bounded cells each; no peak memory or general speed claim", "cases": results}, "", "  ")
		if err := os.WriteFile(report, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
