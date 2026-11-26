package localstore

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"excellens/internal/formats"
)

func TestSnapshotStagesRecoverKilledWriterOnly(t *testing.T) {
	const env = "EXCELLENS_TEST_SNAPSHOT_STAGE"
	if root := os.Getenv(env); root != "" {
		payload, hold, err := NewSnapshotStage(root)
		if err != nil {
			t.Fatal(err)
		}
		defer hold()
		put(t, payload, "partial.rows", "unpublished data")
		fmt.Println(filepath.Base(filepath.Dir(payload)))
		time.Sleep(time.Minute)
		return
	}
	r := Roots{Local: t.TempDir(), Config: t.TempDir()}
	parent := filepath.Join(r.Local, formats.DatabaseSnapshotDirectory)
	put(t, parent, ".snapshot-123.tmp/old.rows", "unknown")
	put(t, parent, "retained/keep.rows", "published")
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSnapshotStagesRecoverKilledWriterOnly$")
	child.Env = append(os.Environ(), env+"="+parent)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("writer did not publish a lease", scanner.Err())
	}
	name := scanner.Text()
	list, err := r.SnapshotStages(t.Context(), true)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	var active bool
	for _, item := range list {
		if item.Name == name && item.State == "active" {
			active = true
		}
	}
	if !active {
		t.Fatal("active writer lost", list)
	}
	for _, g := range r.Inventory(t.Context()) {
		if g.Name == "数据库快照暂存" && (g.Error != "" || g.Bytes < int64(len("unpublished dataunknown"))) {
			t.Fatal(g)
		}
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	list, err = r.SnapshotStages(t.Context(), true)
	if err != nil || len(list) != 1 || list[0].State != "unknown" {
		t.Fatal(list, err)
	}
	if _, err := os.Stat(filepath.Join(parent, name)); !os.IsNotExist(err) {
		t.Fatal("abandoned capture still on disk", err)
	}
	for _, file := range []string{".snapshot-123.tmp/old.rows", "retained/keep.rows"} {
		if _, err := os.Stat(filepath.Join(parent, file)); err != nil {
			t.Fatal(err)
		}
	}
}
