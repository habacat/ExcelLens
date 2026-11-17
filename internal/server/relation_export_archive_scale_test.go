package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
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

	"excellens/internal/workbook"
)

func verifyRelationArchiveScaleCSV(t *testing.T, in io.Reader, kind string, sourceRecords int64) (string, int64) {
	t.Helper()
	h := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(in, h), 64<<10)
	if prefix, err := reader.Peek(3); err == nil && bytes.Equal(prefix, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = reader.Discard(3)
	}
	csvReader := csv.NewReader(reader)
	csvReader.ReuseRecord = true
	header, err := csvReader.Read()
	if err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]int)
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
		t.Fatal("ordinary source columns missing")
	}
	var count int64
	for {
		row, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal("complete CSV or ZIP CRC failed", err)
		}
		count++
		leftRow := max(int(count), 2)
		identity, value, last := 1_000_002-leftRow, "9007199254740993.31", 1
		if kind == "xls" {
			identity, value, last = leftRow, strconv.Itoa(leftRow)+".5", 65_536
		}
		if row[0] != strconv.FormatInt(count, 10) || row[columns["左源行"]] != strconv.Itoa(leftRow) || row[leftID] != fmt.Sprintf("%010d", identity) || row[amount] != value || row[columns["左源列(JSON)"]] != "[1,2]" {
			t.Fatal("source value, type order or coordinate differs", count)
		}
		if kind == "xls" && row[columns["左读取类型(JSON)"]] != `["inlineStr","n"]` {
			t.Fatal("BIFF source types changed", count)
		}
		switch {
		case count <= 2:
			if row[columns["右源行"]] != strconv.Itoa(int(count)*2) || row[3] != "2" {
				t.Fatal("duplicate right records were dropped/reordered", count)
			}
		case identity == last:
			if row[columns["右源行"]] != "3" || row[3] != "1" {
				t.Fatal("last match was lost", count)
			}
		default:
			if row[columns["右记录存在"]] != "false" || row[3] != "0" || row[columns["右读取类型(JSON)"]] != "" {
				t.Fatal("missing side inherited state from a previous row", count)
			}
		}
	}
	if count != sourceRecords+1 {
		t.Fatal("incomplete output", count, sourceRecords+1)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), count
}

func TestRelationExportArchiveActualMillionCSVAndFullBIFF(t *testing.T) {
	if os.Getenv("EXCELLENS_RELATION_EXPORT_SCALE") != "1" {
		t.Skip("optional actual million/BIFF complete CSV and archive comparison")
	}
	var evidence []map[string]any
	for _, kind := range []string{"csv", "xls"} {
		t.Run(kind, func(t *testing.T) {
			isolateTestStorage(t)
			file := filepath.Join(t.TempDir(), "source."+kind)
			first, last, records := 1_000_000, 1, int64(1_000_000)
			if kind == "csv" {
				writeRelationReverseCSV(t, file, 1_000_000)
			} else {
				printBIFFSource(t, file, 65_536)
				first, last, records = 2, 65_536, 65_535
			}
			a, meta := recordOpen(t, file)
			wb := a.currentWorkbook()
			otherID := attachReconcileCSV(t, a, "lookup.csv", fmt.Sprintf("编号,内容\n%010d,first\n%010d,last\n%010d,first again\nmissing,unmatched\n", first, last, first))
			other := a.resolveReconcileBook(otherID)
			before, otherBefore := printScaleHash(t, file), printScaleHash(t, other.Path)
			refs := [2]reconcileSourceRef{reconcileRef(t, a, sourceBookID(wb), "wb:"+meta.Name, 1), reconcileRef(t, a, otherID, "wb:"+other.Sheets[0].Name, 1)}
			query := relationServerQuery()
			started := time.Now()
			root := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query})
			select {
			case <-root.done:
			case <-time.After(90 * time.Second):
				t.Fatal("complete preflight timed out")
			}
			waitRelation(t, a, root, "done")
			queryDir := filepath.Join(wb.CacheDir, "queries")
			if kind == "csv" && !workbook.QueryFilesInUse(queryDir) {
				t.Fatal("actual million reverse source did not exercise mapped order")
			}
			release := holdRelationSlots(t, a.exportSem)
			var jobs []*ExportJob
			for _, format := range []string{"csv", "zipcsv"} {
				response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": strconv.FormatInt(records+1, 10), "format": format, "protectCSV": false}))
				a.mu.RLock()
				jobs = append(jobs, a.exportJobs[response.JobID])
				a.mu.RUnlock()
			}
			clearRelation(t, a, root.ID)
			if relationUsed(a) <= 0 {
				t.Fatal("queued outputs lost their source ownership")
			}
			release()
			var rawBytes, archiveBytes int64
			var rawHash string
			for _, job := range jobs {
				select {
				case <-job.done:
				case <-time.After(180 * time.Second):
					t.Fatal("actual complete export timed out")
				}
				if job.Status != "done" {
					t.Fatal(job.Status, job.Error)
				}
				if job.Format == "csv" {
					f, err := os.Open(job.FilePath)
					if err != nil {
						t.Fatal(err)
					}
					rawHash, _ = verifyRelationArchiveScaleCSV(t, f, kind, records)
					_ = f.Close()
					rawBytes = job.Size
				} else {
					archive, err := zip.OpenReader(job.FilePath)
					if err != nil || len(archive.File) != 1 || archive.File[0].UncompressedSize64 != uint64(rawBytes) {
						t.Fatal("archive member is not the complete CSV", err)
					}
					member, err := archive.File[0].Open()
					if err != nil {
						t.Fatal(err)
					}
					digest, _ := verifyRelationArchiveScaleCSV(t, member, kind, records)
					_ = member.Close()
					_ = archive.Close()
					if digest != rawHash {
						t.Fatal("archive CSV is not byte-identical to standalone output")
					}
					archiveBytes = job.Size
				}
			}
			if relationUsed(a) != 0 || workbook.QueryFilesInUse(queryDir) || printScaleHash(t, file) != before || printScaleHash(t, other.Path) != otherBefore {
				t.Fatal("source mutation or retained budget/query files")
			}
			if archiveBytes >= rawBytes/4 {
				t.Fatal("repeated provenance did not compress as expected in this fixture", rawBytes, archiveBytes)
			}
			evidence = append(evidence, map[string]any{"sourceFormat": kind, "sourceRecords": records, "completeResultRows": records + 1, "csvBytes": rawBytes, "zipBytes": archiveBytes, "logicalCSV_SHA256": rawHash, "allRowsAndSourceCoordinatesCheckedTwice": true, "zipCRCAndExactBytesChecked": true, "lastBorrowReturnedBudgetAndFiles": true, "sourceHashesUnchanged": true, "combinedPreflightExportAndVerificationMs": time.Since(started).Milliseconds()})
		})
	}
	if receipt := os.Getenv("EXCELLENS_RELATION_EXPORT_RECEIPT"); receipt != "" {
		data, _ := json.MarshalIndent(map[string]any{"result": "PASS", "cases": evidence, "desktopManualAcceptance": false, "officeWpsAcceptance": false}, "", "  ")
		if err := os.WriteFile(receipt, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
