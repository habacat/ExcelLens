package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/reconcile"
)

func TestReconcileRecordBundlesCapturedInputsAndCompleteFilteredEvidence(t *testing.T) {
	isolateTestStorage(t)
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	leftData := "编号,金额\n001,9007199254740993.31\ndup,3\ndup,4\n"
	rightData := "金额,编号\n9007199254740993.30,001\n4,dup\n"
	path := filepath.Join(t.TempDir(), "左表.csv")
	if err := os.WriteFile(path, []byte(leftData), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	leftBook := a.currentWorkbook()
	rightID := attachReconcileCSV(t, a, "右表.csv", rightData)
	left := reconcileRef(t, a, sourceBookID(leftBook), "wb:"+leftBook.Sheets[0].Name, 1)
	right := reconcileRef(t, a, rightID, "wb:"+a.resolveReconcileBook(rightID).Sheets[0].Name, 1)
	query := reconcile.Query{Keys: []reconcile.Pair{{Left: 1, Right: 2}}, Fields: []reconcile.Pair{{Left: 2, Right: 1, Mode: "number"}}}
	run := startReconcile(t, a, left, right, query)
	if run.Record == nil || run.RecordError != "" || run.Record.Counts.Different != 1 || run.Record.Counts.Duplicate != 1 {
		t.Fatal("input record missing or counts wrong", run.Status, run.RecordError)
	}
	// A later file revision must not acquire the digest or coordinates of this run.
	if err := os.WriteFile(path, []byte("编号,金额\n新批,1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := func(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }
	for _, status := range []string{"different", "duplicate"} {
		start := decodeResponse[struct{ JobID string }](t, reconcilePost(t, a, "/api/reconcile/export", map[string]any{
			"jobId": run.ID, "format": "zipcsv", "status": status, "protectCSV": true,
		}))
		export := awaitReconcileExport(t, a, start.JobID)
		for _, input := range run.Record.Inputs {
			found, err := exportedSnapshotReference(export.FilePath, input.Fingerprint)
			if err != nil || !found {
				t.Fatal("closed reconciliation bundle lost a source reference", err)
			}
		}
		z, err := zip.OpenReader(export.FilePath)
		if err != nil {
			t.Fatal(err)
		}
		parts := map[string][]byte{}
		for _, part := range z.File {
			reader, err := part.Open()
			if err != nil {
				t.Fatal(err)
			}
			parts[part.Name], err = io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		z.Close()
		var record reconcileRecord
		if err := json.Unmarshal(parts["核查记录.json"], &record); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(parts[record.Detail.File]), "\ufeff"))).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(parts) != 2 || record.Detail.SHA256 != hash(parts[record.Detail.File]) || record.Detail.Rows != int64(len(rows)-1) ||
			record.Detail.Status != status || !record.Detail.ProtectCSV || record.RuleVersion != reconcile.CurrentRuleVersion || record.Scope != "all-data-rows" ||
			record.Inputs[0].FileSHA256 != hash([]byte(leftData)) || record.Inputs[1].FileSHA256 != hash([]byte(rightData)) ||
			!record.Inputs[0].Fields[0].UniqueHeader || record.Query.Keys[0] != query.Keys[0] || record.Query.Fields[0] != query.Fields[0] ||
			record.Counts != run.Result.Counts || len(record.ExceptionGroupsSHA256) != 64 {
			t.Fatal("inconsistent record or evidence digest", status)
		}
		if status == "different" && (len(rows) != 2 || rows[1][10] != "B2" || rows[1][19] != "A2" || rows[1][26] != "0.01") {
			t.Fatal("precise amount or source coordinates lost", rows)
		}
		if status == "duplicate" && (len(rows) != 5 || rows[1][0] != "重复键记录" || rows[4][0] != "主键汇总") {
			t.Fatal("duplicate members missing", rows)
		}
	}
}
