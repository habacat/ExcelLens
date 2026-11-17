package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestFilterCheckpointProcessHelper(t *testing.T) {
	if os.Getenv("EXCELLENS_FILTER_CHECKPOINT_CHILD") != "1" {
		t.Skip("owned subprocess helper only")
	}
	configureTestStorage(os.Getenv("EXCELLENS_FILTER_CHECKPOINT_ROOT"), os.Setenv)
	a := checkpointOpen(t, os.Getenv("EXCELLENS_FILTER_CHECKPOINT_SOURCE"))
	job := filterCheckpointStart(t, a, filterCheckpointRequest(true))
	phase := os.Getenv("EXCELLENS_FILTER_CHECKPOINT_PHASE")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m := job.checkpoint.lease.Snapshot()
		var cursor filterTaskCursor
		if err := json.Unmarshal(m.Cursor, &cursor); err != nil {
			t.Fatal(err)
		}
		ready := phase == "filter" && cursor.Next >= 4096 && !cursor.FilterComplete || phase == "sort" && cursor.FilterComplete && !m.Complete
		if ready {
			data, _ := json.Marshal(map[string]string{"id": job.TaskID})
			if err := os.WriteFile(os.Getenv("EXCELLENS_FILTER_CHECKPOINT_READY"), data, 0600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(time.Hour) // parent kills this process without App.Close
			t.Fatal("parent failed to terminate its subprocess")
		}
		select {
		case <-job.done:
			t.Fatal("task ended before the requested interruption", phase, job.Status, job.Error, cursor)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("requested complete stage was not reached", phase)
}

func TestFilterCheckpointRealProcessTerminationDuringFilterAndSort(t *testing.T) {
	for _, phase := range []string{"filter", "sort"} {
		t.Run(phase, func(t *testing.T) {
			root := isolateTestStorage(t)
			file := filterCheckpointCSV(t, 200000)
			before := printScaleHash(t, file)
			ready := filepath.Join(t.TempDir(), "ready.json")
			cmd := exec.Command(os.Args[0], "-test.run=^TestFilterCheckpointProcessHelper$", "-test.v")
			cmd.Env = append(os.Environ(), "EXCELLENS_FILTER_CHECKPOINT_CHILD=1", "EXCELLENS_FILTER_CHECKPOINT_ROOT="+root, "EXCELLENS_FILTER_CHECKPOINT_SOURCE="+file, "EXCELLENS_FILTER_CHECKPOINT_READY="+ready, "EXCELLENS_FILTER_CHECKPOINT_PHASE="+phase)
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
			deadline := time.Now().Add(30 * time.Second)
			for {
				if data, err := os.ReadFile(ready); err == nil && json.Unmarshal(data, &response) == nil && response.ID != "" {
					break
				}
				select {
				case err := <-done:
					waited = true
					t.Fatal("subprocess ended before readiness", err, output.String())
				default:
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					<-done
					waited = true
					t.Fatal("subprocess readiness timeout", output.String())
				}
				time.Sleep(time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("subprocess exited normally")
			}
			waited = true
			m := checkpointManifest(t, response.ID)
			var cursor filterTaskCursor
			if err := json.Unmarshal(m.Cursor, &cursor); err != nil || m.Complete || phase == "filter" && (cursor.Next < 4096 || cursor.FilterComplete) || phase == "sort" && !cursor.FilterComplete {
				t.Fatal("termination did not leave the requested stage", cursor, err)
			}
			a := checkpointOpen(t, file)
			job := filterCheckpointResume(t, a, m)
			got := filterCheckpointPositions(t, a, job)
			want := filterCheckpointExpected(200000, true)
			if len(got) != len(want) {
				t.Fatal(len(got), len(want))
			}
			for i, position := range want {
				if got[i] != position {
					t.Fatal("crash recovery changed exact source order", i, got[i], position)
				}
			}
			if printScaleHash(t, file) != before || !checkpointManifest(t, m.ID).Complete {
				t.Fatal("source changed or task did not complete")
			}
			t.Logf("killed owned subprocess in %s; next=%d matched=%d sortedPrefix=%d; exact final rows=%d", phase, cursor.Next, cursor.Matched, cursor.OrderWritten, len(want))
		})
	}
}

func TestFilterCheckpointMillionActualCSVAndExternalSort(t *testing.T) {
	if os.Getenv("EXCELLENS_FILTER_RECOVERY_SCALE") != "1" {
		t.Skip("set EXCELLENS_FILTER_RECOVERY_SCALE=1 for actual million-row recovery and sorting")
	}
	isolateTestStorage(t)
	file := filterCheckpointCSV(t, 1000000)
	before := printScaleHash(t, file)
	a := checkpointOpen(t, file)
	m := filterCheckpointPause(t, a, filterCheckpointRequest(true))
	a.Close()
	b := checkpointOpen(t, file)
	job := filterCheckpointResume(t, b, m)
	filterCheckpointWait(t, job)
	if job.Status != "done" {
		t.Fatal(job.Status, job.Error)
	}
	result := b.filterSnapshot(job.ResultID)
	if result == nil {
		t.Fatal("missing final million-row result")
	}
	defer result.close()
	expectedCount := 1 + 1000000 - 1000000/7
	if result.Rows.Len() != expectedCount || result.Rows.At(0) != 0 || !result.Sorted {
		t.Fatal("incomplete sorted result", result.Rows.Len(), expectedCount)
	}
	// Independent integer cent/group oracle; never recompute using the tested
	// filter or sorting comparators. Every resulting source position is checked.
	position := 1
	for cents := 16; cents >= 0; cents-- {
		first := cents
		if first == 0 {
			first = 17
		}
		for source := first; source <= 1000000; source += 17 {
			if source%7 == 0 {
				continue
			}
			if result.Rows.At(position) != source {
				t.Fatal("exact million-row ordering differs from integer oracle", position, result.Rows.At(position), source)
			}
			position++
		}
	}
	if position != expectedCount || printScaleHash(t, file) != before {
		t.Fatal("million-row coverage or source bytes changed", position, expectedCount)
	}
	final := checkpointManifest(t, m.ID)
	if !final.Complete || final.Records != int64(2*expectedCount) {
		t.Fatal(final)
	}
	var cursor filterTaskCursor
	_ = json.Unmarshal(m.Cursor, &cursor)
	if path := os.Getenv("EXCELLENS_FILTER_RECOVERY_SCALE_REPORT"); path != "" {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		report := map[string]any{"result": "PASS", "actualDataRows": 1000000, "sourceBytes": info.Size(), "confirmedBeforeReopen": cursor.Next, "filterMatchedIncludingHeader": expectedCount, "exactSourcePositionsChecked": position, "journalRecords": final.Records, "journalBytes": final.DataBytes, "journalBlocks": final.Blocks, "sourceUnchanged": true, "oracle": "independent integer cents descending and source ordinal ascending, excluding each seventh row", "boundary": "actual million source records, full sorted position verification; no performance or peak memory claim; process termination covered separately"}
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(fmt.Sprintf("actual million data rows; confirmed=%d; fully checked sorted positions=%d; journal=%d bytes", cursor.Next, position, final.DataBytes))
}
