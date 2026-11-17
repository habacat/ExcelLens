package server

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/workbook"
)

func TestExportCheckpointMillionActualReversedCSVAndExactOutputReadback(t *testing.T) {
	if os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_SCALE") != "1" {
		t.Skip("set EXCELLENS_EXPORT_SNAPSHOT_SCALE=1 for actual million-row frozen input and output readback")
	}
	isolateTestStorage(t)
	const count = 1_000_000
	file := filterCheckpointCSV(t, count)
	before := printScaleHash(t, file)
	a := checkpointOpen(t, file)
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	order, err := workbook.BuildRowOrder(context.Background(), filepath.Join(wb.CacheDir, "queries"), idx.RowCount(), func(ctx context.Context, add func(int) error) error {
		for position := idx.RowCount() - 1; position >= 0; position-- {
			if err := add(position); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a.filterResults["million-frozen"] = &FilterResult{ID: "million-frozen", SheetPath: meta.Path, Sorted: true, Rows: order}
	req := exportRequest{Scope: "filtered", Sheet: meta.Name, FilterID: "million-frozen", Format: "csv", Mode: "values"}
	selections, release := exportCheckpointPrepareOwned(t, a, req)
	store, lease := exportCheckpointCreate(t, a, req, selections)
	started := time.Now()
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	m := lease.Snapshot()
	if m.Complete || m.Stage != "ready" || m.Records != count+1 {
		t.Fatal("million input did not confirm its complete frozen order", m)
	}
	_ = lease.Close()
	release()
	a.Close()
	b := checkpointOpen(t, file)
	lease, err = store.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	out, err := readExportCheckpointSelections(context.Background(), b.currentWorkbook(), lease)
	if err != nil {
		t.Fatal(err)
	}
	defer out.close()
	seen := 0
	for ref := range out.selections[0].Rows.Rows() {
		if ref.Number != count+1-seen || ref.Position != count-seen {
			t.Fatal("million snapshot lost exact source positions", seen, ref)
		}
		seen++
	}
	if seen != count+1 {
		t.Fatal("incomplete million coordinate stream", seen)
	}
	path := filepath.Join(t.TempDir(), "actual-frozen-output.csv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = excelExport.Write(context.Background(), f, b.currentWorkbook(), out.selections, req.options(), nil)
	err = errors.Join(err, f.Close())
	if err != nil {
		t.Fatal(err)
	}
	f, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	reader := csv.NewReader(bufio.NewReaderSize(f, 1<<20))
	for source := count; source > 0; source-- {
		record, err := reader.Read()
		group := "keep"
		if source%7 == 0 {
			group = "drop"
		}
		if err != nil || len(record) != 3 || record[0] != fmt.Sprintf("000%09d", source) || record[1] != fmt.Sprintf("9007199254740993.%02d", source%17) || record[2] != group {
			t.Fatal("exact million output differs from integer/source oracle", source, record, err)
		}
	}
	header, err := reader.Read()
	if err != nil || len(header) != 3 || header[0] != "编号" || header[1] != "金额" || header[2] != "分组" {
		t.Fatal("reverse order lost its final original header", header, err)
	}
	if _, err := reader.Read(); err != io.EOF || printScaleHash(t, file) != before || lease.Snapshot().Revision != m.Revision {
		t.Fatal("output tail, source or input revision changed", err)
	}
	report := map[string]any{"result": "PASS", "actualDataRows": count, "sourceRowsIncludingHeader": count + 1, "verifiedSourcePositions": seen, "verifiedOutputRecords": count + 1, "journalInputRecords": m.Records, "journalBytes": m.DataBytes, "journalBlocks": m.Blocks, "sourceSHA256": before, "sourceUnchanged": true, "observedDurationMs": time.Since(started).Milliseconds(), "boundary": "frozen input reopened and exported through existing writer; output resumption, download UI and peak memory not claimed"}
	data, _ := json.MarshalIndent(report, "", "  ")
	if target := os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_SCALE_REPORT"); target != "" {
		if err := os.WriteFile(target, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(data))
}
