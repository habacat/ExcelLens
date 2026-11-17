package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	excelExport "excellens/internal/export"
	"excellens/internal/formats"
	"excellens/internal/workbook"
)

func TestExportCheckpointProcessHelper(t *testing.T) {
	if os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_CHILD") != "1" {
		t.Skip("owned subprocess helper only")
	}
	configureTestStorage(os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_ROOT"), os.Setenv)
	a := checkpointOpen(t, os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_SOURCE"))
	wb := a.currentWorkbook()
	meta := wb.Sheets[0]
	idx, err := wb.BuildIndex(meta, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	phase := os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_PHASE")
	var taskID string
	park := func() {
		data, _ := json.Marshal(map[string]string{"id": taskID})
		if err := os.WriteFile(os.Getenv("EXCELLENS_EXPORT_SNAPSHOT_READY"), data, 0600); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin) // parent holds pipe, then kills us
		t.Fatal("parent failed to kill its owned helper")
	}
	capturing := false
	rows := exportCheckpointTestRows{index: idx, count: idx.RowCount(), refs: func(yield func(workbook.SourceRowReference) bool) {
		for ordinal := 0; ordinal < idx.RowCount(); ordinal++ {
			if capturing && phase == "capture" && ordinal == formats.CheckpointOrderRows {
				park()
			}
			position := idx.RowCount() - ordinal - 1
			if !yield(workbook.SourceRowReference{Number: idx.RowAt(position).Number, Position: position}) {
				return
			}
		}
	}}
	selections := []excelExport.Selection{{Sheet: meta, Index: idx, FirstRow: 1, LastRow: idx.MaxRow, FirstCol: 1, LastCol: idx.MaxCol, Filtered: true, Compact: true, Rows: rows}}
	req := exportRequest{Scope: "filtered", Sheet: meta.Name, Format: "csv", Mode: "values"}
	_, lease := exportCheckpointCreate(t, a, req, selections)
	taskID = lease.Snapshot().ID
	capturing = true
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	park()
}

func TestExportCheckpointRealProcessTerminationDuringCaptureAndAfterReady(t *testing.T) {
	for _, phase := range []string{"capture", "ready"} {
		t.Run(phase, func(t *testing.T) {
			root := isolateTestStorage(t)
			file := filterCheckpointCSV(t, 70000)
			before := printScaleHash(t, file)
			ready := filepath.Join(t.TempDir(), "ready.json")
			cmd := exec.Command(os.Args[0], "-test.run=^TestExportCheckpointProcessHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "EXCELLENS_EXPORT_SNAPSHOT_CHILD=1", "EXCELLENS_EXPORT_SNAPSHOT_ROOT="+root, "EXCELLENS_EXPORT_SNAPSHOT_SOURCE="+file, "EXCELLENS_EXPORT_SNAPSHOT_READY="+ready, "EXCELLENS_EXPORT_SNAPSHOT_PHASE="+phase)
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					<-done
				}
			}()
			var response struct{ ID string }
			deadline := time.Now().Add(45 * time.Second)
			for {
				if data, err := os.ReadFile(ready); err == nil && json.Unmarshal(data, &response) == nil && response.ID != "" {
					break
				}
				select {
				case err := <-done:
					waited = true
					t.Fatal("snapshot child ended before readiness", err, output.String())
				default:
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					<-done
					waited = true
					t.Fatal("snapshot child timed out", output.String())
				}
				time.Sleep(time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("snapshot helper exited normally")
			}
			waited = true
			m := checkpointManifest(t, response.ID)
			if m.Complete || m.Stage != phase {
				t.Fatal("input stage became a completed export", m)
			}
			a := checkpointOpen(t, file)
			store, err := openTaskCheckpoints()
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			lease, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash)
			if err != nil {
				t.Fatal("crashed process left its OS lease busy", err)
			}
			defer lease.Close()
			out, err := readExportCheckpointSelections(context.Background(), a.currentWorkbook(), lease)
			if phase == "capture" {
				if out != nil || !errors.Is(err, errExportCaptureIncomplete) || m.Records != formats.CheckpointOrderRows {
					t.Fatal("partial view input was inferred", out, err, m.Records)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer out.close()
				count := 0
				for ref := range out.selections[0].Rows.Rows() {
					if ref.Number != 70001-count || ref.Position != 70000-count {
						t.Fatal("confirmed input changed its exact source position", count, ref)
					}
					count++
				}
				if count != 70001 {
					t.Fatal(count)
				}
			}
			if printScaleHash(t, file) != before || lease.Snapshot().Revision != m.Revision {
				t.Fatal("input recovery changed source or confirmed stage")
			}
			t.Logf("killed owned subprocess at %s; durable input records=%d; source unchanged", phase, m.Records)
		})
	}
}
