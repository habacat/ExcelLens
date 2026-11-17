package server

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/workbook"
)

func writeRelationReverseCSV(t *testing.T, path string, count int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 64<<10)
	_, err = fmt.Fprintln(w, "编号,金额")
	for i := count; i >= 1 && err == nil; i-- {
		_, err = fmt.Fprintf(w, "%010d,9007199254740993.31\n", i)
	}
	if next := w.Flush(); err == nil {
		err = next
	}
	if next := f.Close(); err == nil {
		err = next
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestRelationTasksActualReverseMillionAndFullBIFFCompleteCSVAfterOwnerClear(t *testing.T) {
	if os.Getenv("EXCELLENS_RELATION_SCALE") != "1" {
		t.Skip("set EXCELLENS_RELATION_SCALE=1 for actual relation task/export source checks")
	}
	var evidence []map[string]any
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			path := filepath.Join(t.TempDir(), "source."+kind)
			first, last, records := 1_000_000, 1, int64(1_000_000)
			if kind == "csv" {
				writeRelationReverseCSV(t, path, 1_000_000)
			} else {
				printBIFFSource(t, path, 65_536)
				first, last, records = 2, 65_536, 65_535
			}
			a, meta := recordOpen(t, path)
			wb := a.currentWorkbook()
			otherID := attachReconcileCSV(t, a, "lookup.csv", fmt.Sprintf("编号,内容\n%010d,first\n%010d,last\n%010d,first again\nmissing,unmatched\n", first, last, first))
			other := a.resolveReconcileBook(otherID)
			before, otherBefore := printScaleHash(t, path), printScaleHash(t, other.Path)
			refs := [2]reconcileSourceRef{reconcileRef(t, a, sourceBookID(wb), "wb:"+meta.Name, 1), reconcileRef(t, a, otherID, "wb:"+other.Sheets[0].Name, 1)}
			query := relationServerQuery()
			started := time.Now()
			root := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query})
			select {
			case <-root.done:
			case <-time.After(90 * time.Second):
				t.Fatal("actual relation preflight timed out")
			}
			waitRelation(t, a, root, "done")
			result := relationResult(t, a, root.ID)
			if result.Counts.LeftRecords != records || result.Counts.LeftJoinRows != records+1 || len(result.Preview.Rows) != 100 || !result.Preview.Truncated || result.Preview.Rows[0].Left.Number != 2 || result.Preview.Rows[1].Left.Number != 2 {
				t.Fatal("task scale counts/preview/source order changed", result.Counts)
			}
			retained := relationUsed(a)
			if retained <= 0 || retained > 2<<20 {
				t.Fatal("file-backed task retained a whole table or unbounded descriptors", retained)
			}
			queryDir := filepath.Join(wb.CacheDir, "queries")
			if kind == "csv" && !workbook.QueryFilesInUse(queryDir) {
				t.Fatal("reverse million did not exercise a retained mapped original-order plan")
			}
			release := holdRelationSlots(t, a.exportSem)
			response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": strconv.FormatInt(records+1, 10), "protectCSV": false}))
			a.mu.RLock()
			export := a.exportJobs[response.JobID]
			a.mu.RUnlock()
			clearRelation(t, a, root.ID)
			if relationUsed(a) <= 0 || kind == "csv" && !workbook.QueryFilesInUse(queryDir) {
				t.Fatal("clearing task reclaimed the queued export's mapped source")
			}
			release()
			select {
			case <-export.done:
			case <-time.After(180 * time.Second):
				t.Fatal("actual complete CSV export timed out")
			}
			export.mu.RLock()
			status, message, output, outputBytes := export.Status, export.Error, export.FilePath, export.Size
			export.mu.RUnlock()
			if status != "done" {
				t.Fatal("actual CSV export failed", status, message)
			}
			f, err := os.Open(output)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			reader := bufio.NewReaderSize(f, 64<<10)
			if prefix, err := reader.Peek(3); err == nil && bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
				_, _ = reader.Discard(3)
			}
			csvReader := csv.NewReader(reader)
			header, err := csvReader.Read()
			if err != nil {
				t.Fatal(err)
			}
			columns := map[string]int{}
			leftID, amount := -1, -1
			for i, name := range header {
				columns[name] = i
				if strings.HasPrefix(name, "左 A ") {
					leftID = i
				}
				if strings.HasPrefix(name, "左 B ") {
					amount = i
				}
			}
			if leftID < 0 || amount < 0 {
				t.Fatal("ordinary source value columns missing")
			}
			var count int64
			for {
				row, err := csvReader.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				count++
				leftRow := max(int(count), 2)
				identity, value := 1_000_002-leftRow, "9007199254740993.31"
				if kind == "xls" {
					identity, value = leftRow, strconv.Itoa(leftRow)+".5"
				}
				if row[0] != strconv.FormatInt(count, 10) || row[columns["左源行"]] != strconv.Itoa(leftRow) || row[leftID] != fmt.Sprintf("%010d", identity) || row[amount] != value {
					t.Fatal("complete task export changed original order/coordinate/raw value", count, row[0], row[columns["左源行"]], row[leftID], row[amount])
				}
				if kind == "xls" && row[columns["左读取类型(JSON)"]] != `["inlineStr","n"]` {
					t.Fatal("BIFF types changed", count)
				}
				if count <= 2 {
					if row[columns["右源行"]] != strconv.Itoa(int(count)*2) || row[3] != "2" {
						t.Fatal("duplicate right records were dropped or reordered", count)
					}
				} else if identity == last {
					if row[columns["右源行"]] != "3" || row[3] != "1" {
						t.Fatal("last source match changed", count)
					}
				} else if row[columns["右记录存在"]] != "false" || row[3] != "0" {
					t.Fatal("unmatched row was presented as matched", count)
				}
			}
			if count != records+1 || relationUsed(a) != 0 || workbook.QueryFilesInUse(queryDir) {
				t.Fatal("incomplete output or retained final source", count, relationUsed(a))
			}
			if printScaleHash(t, path) != before || printScaleHash(t, other.Path) != otherBefore {
				t.Fatal("task/export modified a source")
			}
			entry := map[string]any{"format": kind, "sourceRecords": records, "completeCSVRecords": count, "outputBytes": outputBytes, "retainedGoBytesAtPreflight": retained, "mappedMillionOrderExercised": kind == "csv", "allCoordinatesRawValuesOrderAndMatchCountsChecked": true, "sourceHashesUnchanged": true, "lastBorrowReturnedBudgetAndFiles": true, "elapsedMs": time.Since(started).Milliseconds()}
			evidence = append(evidence, entry)
			t.Logf("actual task/export %s: source=%d; complete CSV=%d rows/%d bytes; every original value/coordinate/order/match count checked; retained=%d Go bytes; final budget/query files released", kind, records, count, outputBytes, retained)
		})
	}
	if path := os.Getenv("EXCELLENS_RELATION_TASKS_RECEIPT"); path != "" {
		data, err := json.MarshalIndent(map[string]any{"version": buildinfo.Version, "result": "PASS", "cases": evidence}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
