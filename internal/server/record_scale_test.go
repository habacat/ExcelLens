package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestRecordActualMillionCSVAndCompleteBIFFCapacity(t *testing.T) {
	if os.Getenv("EXCELLENS_RECORD_SCALE") != "1" {
		t.Skip("set EXCELLENS_RECORD_SCALE=1 for actual million-row sources")
	}
	var evidence []map[string]any
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			count := 1_000_000
			file := filepath.Join(t.TempDir(), "record-source."+kind)
			if kind == "csv" {
				f, err := os.Create(file)
				if err != nil {
					t.Fatal(err)
				}
				writer := bufio.NewWriterSize(f, 1<<20)
				if _, err = writer.WriteString("编号,金额,源行\n"); err != nil {
					t.Fatal(err)
				}
				for row := 2; row <= count; row++ {
					if _, err = fmt.Fprintf(writer, "%012d,9007199254740993.31,%d\n", row, row); err != nil {
						t.Fatal(err)
					}
				}
				if err = writer.Flush(); err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				count = formats.BIFFRows
				printBIFFSource(t, file, count)
			}
			before := printScaleHash(t, file)
			a, meta := recordOpen(t, file)
			wb := a.currentWorkbook()
			idx := wb.GetCachedIndex(meta.Path)
			if idx.RowCount() != count || idx.MaxRow != count || wb.Format != kind {
				t.Fatal("fixture did not exercise real capacity", kind, idx.RowCount(), idx.MaxRow, wb.Format)
			}
			req := recordSource(t, a, meta.Name, rowViewRequest{})
			req.PinnedThroughRow = 1
			var pages []map[string]any
			middle := count / 2
			if kind == "xls" {
				middle = (middle/997)*997 + 1 // Immediately after a hidden source row.
			}
			for _, number := range []int{2, middle, count} {
				req.Row = number
				started := time.Now()
				r := recordOK(t, a, req)
				elapsed := time.Since(started)
				identifier, amount := fmt.Sprintf("%012d", number), "9007199254740993.31"
				if kind == "xls" {
					identifier = fmt.Sprintf("%010d", number)
					amount = strconv.FormatFloat(float64(number)+.5, 'f', 1, 64)
				}
				if r.Row != number || r.Fields[0].Cell.Ref != "A"+strconv.Itoa(number) || r.Fields[0].Cell.Raw != identifier || r.Fields[1].Cell.Raw != amount || !r.RowIndexed {
					t.Fatal("capacity source value/coordinate changed", kind, number, r)
				}
				if kind == "csv" && r.Fields[2].Cell.Raw != strconv.Itoa(number) {
					t.Fatal("CSV source row lost", r)
				}
				position, total, previous, next := number, count, number-1, number+1
				if kind == "xls" {
					position, total = number-number/997, count-count/997
					if previous%997 == 0 {
						previous--
					}
					if next%997 == 0 {
						next++
					}
				}
				if next > count {
					next = 0
				}
				if r.Position != position || r.TotalRecords != total || r.PreviousRow != previous || r.NextRow != next {
					t.Fatal("physical source visibility/navigation diverged", kind, number, r.Position, r.TotalRecords, r.PreviousRow, r.NextRow)
				}
				pages = append(pages, map[string]any{"row": number, "fields": len(r.Fields), "position": r.Position, "totalVisible": r.TotalRecords, "elapsedMs": float64(elapsed.Microseconds()) / 1000})
			}
			cacheFaultVerified := false
			if kind == "csv" {
				// Demonstrate that unrelated records are not decoded. A bounded
				// read-ahead may contain these bytes but must not parse them.
				badNumber := count / 3
				offset := idx.RowAt(idx.FindRowOffset(badNumber))
				cache, err := os.OpenFile(idx.XMLPath, os.O_RDWR, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = cache.WriteAt(bytes.Repeat([]byte{0xff}, min(16, int(offset.End-offset.Start))), offset.Start); err != nil {
					t.Fatal(err)
				}
				if err = cache.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err = wb.ReadRowsByNumbersWithOptionsContext(context.Background(), idx, []int{badNumber}, 1, 3, workbook.RowReadOptions{}); err == nil {
					t.Fatal("negative-control cache fault did not affect its own record")
				}
				req.Row = 2
				r := recordOK(t, a, req)
				if r.Fields[0].Cell.Raw != "000000000002" {
					t.Fatal("unrelated damaged cache record affected targeted record", r)
				}
				cacheFaultVerified = true
			}
			if printScaleHash(t, file) != before {
				t.Fatal("source bytes changed")
			}
			info, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			evidence = append(evidence, map[string]any{"format": kind, "physicalRecords": count, "indexedRecords": idx.RowCount(), "sourceMaxRow": idx.MaxRow, "sourceBytes": info.Size(), "recordPages": pages, "unrelatedCacheFaultVerified": cacheFaultVerified, "sourceSHA256": before, "sourceUnchanged": true})
		})
	}
	if t.Failed() {
		return
	}
	if target := os.Getenv("EXCELLENS_RECORD_SCALE_REPORT"); target != "" {
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(target)
		if err != nil {
			t.Fatal(err)
		}
		err = json.NewEncoder(file).Encode(map[string]any{"result": "PASS", "version": buildinfo.Version, "sources": evidence, "desktopManualAcceptance": false, "limitations": "Generated physical sources; three actual HTTP-handler record pages per source, not every row/field. Timings are local warm request observations, not comparative performance or browser memory bounds."})
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatal(err, closeErr)
		}
	}
}
