package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRelationZIPMatchesPlainCSVAfterOwnerClearWithBothTextProtectionOptions(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(map[bool]string{false: "protected", true: "raw"}[raw], func(t *testing.T) {
			a, _ := recordFixture(t, 12, "")
			wb := a.currentWorkbook()
			id := attachReconcileCSV(t, a, "公式😀.csv", "编号,内容\n001,\"=SUM(1,2)\"\nright,\"精确原文,\"\"引号\"\"\n😀\"\n")
			right := a.resolveReconcileBook(id)
			refs := [2]reconcileSourceRef{reconcileRef(t, a, sourceBookID(wb), "wb:"+wb.Sheets[0].Name, 1), reconcileRef(t, a, id, "wb:"+right.Sheets[0].Name, 1)}
			query := relationServerQuery()
			query.LeftColumns = []int{1, 2, 6}
			root := relationStart(t, a, relationStartRequest{Mode: "prepare", Left: &refs[0], Right: &refs[1], Query: &query})
			waitRelation(t, a, root, "done")
			release := holdRelationSlots(t, a.exportSem)
			var jobs []string
			for _, format := range []string{"csv", "zipcsv"} {
				response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": "100", "protectCSV": !raw, "format": format}))
				jobs = append(jobs, response.JobID)
			}
			clearRelation(t, a, root.ID)
			if response := reconcilePost(t, a, "/api/reconcile/source/remove", map[string]string{"book": id}); response.Code != 200 {
				t.Fatal(response.Code)
			}
			release()
			plain, compressed := awaitReconcileExport(t, a, jobs[0]), awaitReconcileExport(t, a, jobs[1])
			original, err := os.ReadFile(plain.FilePath)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := zip.OpenReader(compressed.FilePath)
			if err != nil || len(archive.File) != 1 {
				t.Fatal(err)
			}
			defer archive.Close()
			member, err := archive.File[0].Open()
			if err != nil {
				t.Fatal(err)
			}
			extracted, err := io.ReadAll(member)
			_ = member.Close()
			if err != nil || !bytes.Equal(original, extracted) || compressed.Format != "zipcsv" || filepath.Ext(compressed.FileName) != ".zip" {
				t.Fatal("ZIP changed complete CSV provenance, values, order or text protection", err)
			}
			if relationUsed(a) != 0 {
				t.Fatal("last archive borrow retained source budget", relationUsed(a))
			}
			response := analysisRequest(t, a, "GET", "/api/export/download?id="+compressed.ID+"&ticket="+compressed.Ticket, "")
			if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Disposition"), ".zip") {
				t.Fatal("ZIP did not use shared authenticated download", response.Code, response.Header())
			}
		})
	}
}

func TestRelationZIPInvalidFormatAndQueuedCancellationDoNotPublishFiles(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	root := relationPrepared(t, a, refs)
	for _, format := range []string{"xlsx", "zip", "ZIPCSV", "../csv"} {
		if response := reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": "100", "format": format}); response.Code != 400 {
			t.Fatal("unsupported relation export admitted", format, response.Code)
		}
	}
	release := holdRelationSlots(t, a.querySem)
	defer release()
	start := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": "left", "maximum": "100", "format": "zipcsv"}))
	if response := reconcilePost(t, a, "/api/export/cancel", map[string]string{"id": start.JobID}); response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	a.mu.RLock()
	job := a.exports[start.JobID]
	a.mu.RUnlock()
	<-job.done
	if job.Status != "cancelled" || job.FilePath != "" {
		t.Fatal("cancelled archive published a partial output", job.Status, job.FilePath)
	}
	matches, _ := filepath.Glob(filepath.Join(ExportDir(), ".export-*.tmp"))
	if len(matches) != 0 {
		t.Fatal("cancelled archive left temporary files", matches)
	}
}

func TestRelationZIPAllSevenModesIncludingEmptyResultsKeepIdenticalCSV(t *testing.T) {
	a, refs := relationSnapshotFixture(t)
	root := relationPrepared(t, a, refs)
	result := relationResult(t, a, root.ID)
	empty := false
	for _, mode := range []string{"left", "inner", "left_matched", "left_unmatched", "right_unmatched", "left_invalid", "right_invalid"} {
		want, err := result.Counts.Rows(mode)
		if err != nil {
			t.Fatal(err)
		}
		var original []byte
		for _, format := range []string{"csv", "zipcsv"} {
			response := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/relation/export", map[string]any{"jobId": root.ID, "mode": mode, "maximum": "100", "format": format}))
			job := awaitReconcileExport(t, a, response.JobID)
			var data []byte
			if format == "csv" {
				data, err = os.ReadFile(job.FilePath)
				original = data
			} else {
				archive, openErr := zip.OpenReader(job.FilePath)
				if openErr != nil || len(archive.File) != 1 {
					t.Fatal(mode, openErr)
				}
				member, openErr := archive.File[0].Open()
				if openErr != nil {
					t.Fatal(openErr)
				}
				data, err = io.ReadAll(member)
				_ = member.Close()
				_ = archive.Close()
			}
			if err != nil || !bytes.Equal(data, original) {
				t.Fatal("mode-specific CSV changed", mode, format, err)
			}
			rows, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))).ReadAll()
			if err != nil || int64(len(rows)) != want+1 {
				t.Fatal("missing records or header-only output", mode, len(rows), want, err)
			}
		}
		empty = empty || want == 0
	}
	if !empty {
		t.Fatal("fixture did not exercise a header-only complete export")
	}
}
