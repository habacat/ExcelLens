package server

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSearchCheckpointProcessHelper(t *testing.T) {
	if os.Getenv("EXCELLENS_SEARCH_CHECKPOINT_CHILD") != "1" {
		t.Skip("only the owned subprocess runs this helper")
	}
	configureTestStorage(os.Getenv("EXCELLENS_SEARCH_CHECKPOINT_ROOT"), os.Setenv)
	a := checkpointOpen(t, os.Getenv("EXCELLENS_SEARCH_CHECKPOINT_SOURCE"))
	job, id := checkpointStart(t, a, `A:"needle"`, "advanced", "current")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m := job.checkpoint.lease.Snapshot()
		var cursor searchTaskCursor
		if err := json.Unmarshal(m.Cursor, &cursor); err != nil {
			t.Fatal(err)
		}
		if m.Complete {
			t.Fatal("child completed before parent could interrupt")
		}
		if cursor.Next >= 4096 {
			data, _ := json.Marshal(map[string]string{"id": id})
			if err := os.WriteFile(os.Getenv("EXCELLENS_SEARCH_CHECKPOINT_READY"), data, 0600); err != nil {
				t.Fatal(err)
			}
			// The parent terminates only this child. No graceful App.Close runs.
			time.Sleep(time.Hour)
			t.Fatal("parent failed to terminate its child")
		}
		select {
		case <-job.done:
			t.Fatal("child search failed", job.Status, job.Error)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("child search did not confirm progress")
}

func TestSearchCheckpointRealProcessTerminationReleasesLeaseAndResumes(t *testing.T) {
	root := isolateTestStorage(t)
	file := checkpointCSV(t, 120000)
	before := printScaleHash(t, file)
	ready := filepath.Join(t.TempDir(), "ready.json")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSearchCheckpointProcessHelper$", "-test.v")
	cmd.Env = append(os.Environ(), "EXCELLENS_SEARCH_CHECKPOINT_CHILD=1", "EXCELLENS_SEARCH_CHECKPOINT_ROOT="+root, "EXCELLENS_SEARCH_CHECKPOINT_SOURCE="+file, "EXCELLENS_SEARCH_CHECKPOINT_READY="+ready)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	var response struct{ ID string }
	deadline := time.Now().Add(30 * time.Second)
	for {
		if data, err := os.ReadFile(ready); err == nil && json.Unmarshal(data, &response) == nil && response.ID != "" {
			break
		}
		select {
		case err := <-done:
			waited = true
			t.Fatal("child exited before durable progress", err, output.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-done
			waited = true
			t.Fatal("child did not confirm a checkpoint", output.String())
		}
		time.Sleep(time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("child unexpectedly exited normally")
	}
	waited = true
	m := checkpointManifest(t, response.ID)
	var cursor searchTaskCursor
	if err := json.Unmarshal(m.Cursor, &cursor); err != nil || m.Complete || cursor.Next < 4096 || cursor.Next >= 120001 || cursor.Count != 1 {
		t.Fatal("process did not leave a partial confirmed prefix", cursor, m.Complete, err)
	}
	a := checkpointOpen(t, file)
	job := checkpointResume(t, a, m)
	checkpointWait(t, job)
	if job.Status != "done" || job.Truncated || len(job.Matches) != 3 || job.Matches[0].Row != 1001 || job.Matches[1].Row != 119996 || job.Matches[2].Row != 119997 || printScaleHash(t, file) != before {
		t.Fatal("crash recovery changed results or source", job.Status, job.Error, job.Matches)
	}
	final := checkpointManifest(t, m.ID)
	if !final.Complete || final.Records != 3 {
		t.Fatal(final)
	}
	t.Logf("terminated owned process; confirmed prefix %d records; resumed to %d records, 3 exact hits", cursor.Next, job.Scanned)
}

func TestSearchCheckpointMillionActualCSV(t *testing.T) {
	if os.Getenv("EXCELLENS_SEARCH_RECOVERY_SCALE") != "1" {
		t.Skip("set EXCELLENS_SEARCH_RECOVERY_SCALE=1 for actual million-row recovery")
	}
	isolateTestStorage(t)
	file := checkpointCSV(t, 1000000)
	before := printScaleHash(t, file)
	a := checkpointOpen(t, file)
	m := checkpointPause(t, a, `A:"needle"`)
	a.Close()
	b := checkpointOpen(t, file)
	job := checkpointResume(t, b, m)
	checkpointWait(t, job)
	if job.Status != "done" || job.Truncated || job.Scanned != 1000001 || len(job.Matches) != 3 || job.Matches[0].Row != 1001 || job.Matches[1].Row != 999996 || job.Matches[2].Row != 999997 || printScaleHash(t, file) != before {
		t.Fatal("actual million-row recovery lost source records", job.Status, job.Error, job.Scanned, job.Matches)
	}
	final := checkpointManifest(t, m.ID)
	var cursor searchTaskCursor
	_ = json.Unmarshal(m.Cursor, &cursor)
	if !final.Complete || final.Records != 3 || final.Blocks != 2 || final.DataBytes >= 4096 {
		t.Fatal("journal copied the full source or duplicated hits", final)
	}
	if path := os.Getenv("EXCELLENS_SEARCH_RECOVERY_SCALE_REPORT"); path != "" {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		report := map[string]any{"result": "PASS", "format": "csv", "actualDataRows": 1000000, "actualSourceBytes": info.Size(), "confirmedBeforeReopen": cursor.Next, "scannedIncludingHeader": job.Scanned, "exactHits": job.Matches, "journalRecords": final.Records, "journalBlocks": final.Blocks, "journalBytes": final.DataBytes, "sourceUnchanged": true, "boundary": "actual source search recovery after App reopen; separate real subprocess regression; no performance or desktop acceptance claim"}
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("actual 1,000,000 data rows; resumed after %d source records; journal %d bytes", cursor.Next, final.DataBytes)
}
