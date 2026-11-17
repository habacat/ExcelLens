package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	excelExport "excellens/internal/export"
)

type exportCrashWriter struct {
	out  io.Writer
	park func()
}

func (w *exportCrashWriter) Write(data []byte) (int, error) {
	// Even a tiny CSV part has a real in-flight write. Stop before returning
	// to its producer; larger writes also leave an incomplete body payload.
	n, err := w.out.Write(data[:min(len(data), 256)])
	if err == nil && n > 0 {
		w.park()
	}
	return n, err
}

type exportCrashJournal struct {
	excelExport.ResumeJournal
	mode  string
	after uint64
	park  func()
}

func (j exportCrashJournal) Commit(ctx context.Context, part excelExport.ResumePart, produce func(io.Writer) (excelExport.ResumeState, error)) error {
	err := j.ResumeJournal.Commit(ctx, part, func(out io.Writer) (excelExport.ResumeState, error) {
		if j.mode == "bytes" {
			out = &exportCrashWriter{out: out, park: j.park}
		}
		return produce(out)
	})
	if err == nil && j.mode == "part" && j.State().Commits == j.after {
		j.park()
	}
	return err
}

func TestExportResumeProcessHelper(t *testing.T) {
	if os.Getenv("EXCELLENS_EXPORT_RESUME_CHILD") != "1" {
		t.Skip("owned subprocess helper only")
	}
	configureTestStorage(os.Getenv("EXCELLENS_EXPORT_RESUME_ROOT"), os.Setenv)
	a := checkpointOpen(t, os.Getenv("EXCELLENS_EXPORT_RESUME_SOURCE"))
	wb := a.currentWorkbook()
	req := exportRequest{Scope: "sheet", Sheet: wb.Sheets[0].Name, Format: os.Getenv("EXCELLENS_EXPORT_RESUME_FORMAT"), Mode: "values", Styles: true, BOM: true, ProtectCSV: true}
	if req.Format == "xlsx" || req.Format == "zipcsv" {
		req.Scope = "all"
	}
	selections := exportCheckpointPrepare(t, a, req)
	_, lease := exportCheckpointCreate(t, a, req, selections)
	if err := captureExportCheckpoint(context.Background(), wb, lease, selections); err != nil {
		t.Fatal(err)
	}
	manifest := lease.Snapshot()
	journal, err := newExportCheckpointJournal(context.Background(), wb, lease, selections)
	if err != nil {
		t.Fatal(err)
	}
	park := func() {
		data, _ := json.Marshal(map[string]string{"id": manifest.ID})
		if err := os.WriteFile(os.Getenv("EXCELLENS_EXPORT_RESUME_READY"), data, 0600); err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		t.Fatal("parent failed to terminate its live writer")
	}
	after := uint64(1)
	if req.Format == "html" {
		after = 2
	}
	err = excelExport.Resume(context.Background(), wb, selections, req.options(), exportCrashJournal{ResumeJournal: journal, mode: os.Getenv("EXCELLENS_EXPORT_RESUME_PHASE"), after: after, park: park}, nil)
	t.Fatal("writer completed before requested process termination", err)
}

func TestExportResumeRealProcessTerminationAfterRecordsAndInsideUnconfirmedParts(t *testing.T) {
	for _, test := range []struct{ format, phase string }{
		{"csv", "part"}, {"tsv", "part"}, {"jsonl", "part"}, {"html", "part"},
		{"xlsx", "part"}, {"zipcsv", "part"}, {"csv", "bytes"}, {"xlsx", "bytes"}, {"zipcsv", "bytes"},
	} {
		t.Run(test.format+"-"+test.phase, func(t *testing.T) {
			root := isolateTestStorage(t)
			file := ""
			if test.format == "xlsx" || test.format == "zipcsv" {
				file = exportResumeStyledWorkbook(t)
			} else {
				file = filterCheckpointCSV(t, 12000)
			}
			before := printScaleHash(t, file)
			a := checkpointOpen(t, file)
			wb := a.currentWorkbook()
			req := exportRequest{Scope: "sheet", Sheet: wb.Sheets[0].Name, Format: test.format, Mode: "values", Styles: true, BOM: true, ProtectCSV: true}
			if test.format == "xlsx" || test.format == "zipcsv" {
				req.Scope = "all"
			}
			want := exportCheckpointOutput(t, wb, exportCheckpointPrepare(t, a, req), req.options())
			a.Close()
			ready := filepath.Join(t.TempDir(), "ready.json")
			cmd := exec.Command(os.Args[0], "-test.run=^TestExportResumeProcessHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "EXCELLENS_EXPORT_RESUME_CHILD=1", "EXCELLENS_EXPORT_RESUME_ROOT="+root, "EXCELLENS_EXPORT_RESUME_SOURCE="+file, "EXCELLENS_EXPORT_RESUME_READY="+ready, "EXCELLENS_EXPORT_RESUME_FORMAT="+test.format, "EXCELLENS_EXPORT_RESUME_PHASE="+test.phase)
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
					t.Fatal("live writer ended before the requested phase", err, output.String())
				default:
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					<-done
					waited = true
					t.Fatal("writer readiness timeout", output.String())
				}
				time.Sleep(time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("writer exited gracefully")
			}
			waited = true
			m := checkpointManifest(t, response.ID)
			var cursor exportTaskCursor
			if json.Unmarshal(m.Cursor, &cursor) != nil || m.Complete || !cursor.Snapshot || test.phase == "part" && cursor.Output == nil || test.phase == "bytes" && cursor.Output != nil {
				t.Fatal("unexpected acknowledged phase after process termination", m)
			}
			if test.phase == "bytes" {
				base, _ := os.UserConfigDir()
				path := filepath.Join(base, "ExcelLens", "task-checkpoints", m.ID, "blocks")
				info, err := os.Stat(path)
				if err != nil || info.Size() <= m.DataBytes {
					t.Fatal("test did not leave a physical unconfirmed output tail", info, err)
				}
				store, err := openTaskCheckpoints()
				if err != nil {
					t.Fatal(err)
				}
				lease, err := store.Acquire(context.Background(), m.ID, m.DefinitionHash)
				if err != nil {
					t.Fatal(err)
				}
				info, err = os.Stat(path)
				if err != nil || info.Size() != m.DataBytes || lease.Snapshot().Revision != m.Revision {
					t.Fatal("unconfirmed tail was not removed at its exact confirmed boundary", info, err)
				}
				_ = lease.Close()
				_ = store.Close()
			}
			b := checkpointOpen(t, file)
			got := exportResumeDownload(t, b, exportResumeJob(t, b, m))
			compareResumedOutput(t, test.format, got, want, m.Created)
			if printScaleHash(t, file) != before || !checkpointManifest(t, m.ID).Complete {
				t.Fatal("recovered output modified its source or never completed")
			}
			t.Logf("killed live %s writer at %s; confirmed records=%d blocks=%d; complete HTTP download independently matched", test.format, test.phase, m.Records, m.Blocks)
		})
	}
}
