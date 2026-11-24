package localstore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestoreSpaceBudgetsMissingUnitsPerPhysicalVolume(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	const gb = 1 << 30
	files := map[string]fileRecord{"config/settings/a": {45 * gb, strings.Repeat("a", 64)}, "local/exports/b": {35 * gb, strings.Repeat("b", 64)}}
	m, _ := json.Marshal(manifest{1, files})
	backup := t.TempDir()
	put(t, backup, "manifest.json", string(m))
	probe := func(string) (volumeSpace, error) { return volumeSpace{"same", 60 * gb, 4096}, nil }
	err := r.RestoreWith(t.Context(), backup, TransferOptions{spaceProbe: probe})
	if err == nil || !strings.Contains(err.Error(), "空间不足") || r.Pending() {
		t.Fatal("expected preflight before opening payload", err)
	}
	units, err := r.units(files)
	if err != nil {
		t.Fatal(err)
	}
	split := func(path string) (volumeSpace, error) { return volumeSpace{path, 60 * gb, 4096}, nil }
	if _, err := r.restoreSpace(t.Context(), units, files, TransferOptions{spaceProbe: split}); err != nil {
		t.Fatal(err)
	}
	put(t, r.Config, "settings/a", "existing bytes will be checked during transfer")
	p, err := r.restoreSpace(t.Context(), units, files, TransferOptions{spaceProbe: probe})
	if err != nil || p.targets["config"].remaining >= 36*gb {
		t.Fatal("existing unit counted as a copy", err)
	}
}
func TestEncryptedBackupSpaceAndMidTransferCapacityLoss(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	parent := t.TempDir()
	put(t, r.Local, "exports/a.csv", strings.Repeat("a", 10<<20))
	free := uint64(1 << 30)
	o := TransferOptions{spaceProbe: func(string) (volumeSpace, error) { return volumeSpace{"target", free, 1}, nil }}
	plain, _, _, err := r.backupSpace(t.Context(), parent, o)
	if err != nil {
		t.Fatal(err)
	}
	o.Password = "test"
	sealed, total, items, err := r.backupSpace(t.Context(), parent, o)
	if err != nil {
		t.Fatal(err)
	}
	if total != 10<<20 || items != 1 || sealed.targets["backup"].remaining <= plain.targets["backup"].remaining {
		t.Fatal("missing payload or encryption overhead")
	}
	// Force the loss at the start of copying as fast memory copies can finish
	// before the next time-based progress notification.
	o.Report = func(p Progress) {
		if p.Phase == "复制" {
			free = 1
		}
	}
	_, err = r.BackupWith(t.Context(), parent, o)
	if err == nil || !strings.Contains(err.Error(), "空间不足") {
		t.Fatal("capacity loss missed", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestAbandonedBackupStageOwnership(t *testing.T) {
	if path := os.Getenv("EXCELLENS_TEST_STAGE_PARENT"); path != "" {
		stage, _, err := newBackupStage(path)
		if err != nil {
			t.Fatal(err)
		}
		put(t, stage, "payload/partial", "data")
		put(t, path, "ready", filepath.Base(stage))
		time.Sleep(time.Minute)
		return
	}
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	parent := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAbandonedBackupStageOwnership$")
	cmd.Env = append(os.Environ(), "EXCELLENS_TEST_STAGE_PARENT="+parent)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	var name string
	for name == "" {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		b, _ := os.ReadFile(filepath.Join(parent, "ready"))
		name = string(b)
		time.Sleep(5 * time.Millisecond)
	}
	list, err := r.BackupStages(parent)
	if err != nil || len(list) != 1 || list[0].State != "active" || r.CleanBackupStage(parent, name) == nil {
		t.Fatal(list, err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	cmd.Wait()
	unknown := ".ExcelLens-20260101-123456-123"
	put(t, parent, unknown+"/keep", "user")
	list, err = r.BackupStages(parent)
	if err != nil || len(list) != 2 {
		t.Fatal(list, err)
	}
	if r.CleanBackupStage(parent, unknown) == nil || r.CleanBackupStage(parent, "..") == nil {
		t.Fatal("unowned stage accepted")
	}
	if err := r.CleanBackupStage(parent, name); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(parent, unknown, "keep")); string(b) != "user" {
		t.Fatal("unowned data removed")
	}
}

func TestBackupBudgetRejectsSourceGrowthBeforeCopy(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	parent := t.TempDir()
	put(t, r.Local, "exports/test", "x")
	_, err := r.BackupWith(t.Context(), parent, TransferOptions{Report: func(p Progress) {
		if p.Phase == "复制" && p.Bytes == 0 {
			put(t, r.Local, "exports/test", "changed")
		}
	}})
	if err == nil || !strings.Contains(err.Error(), "预算后文件发生变化") {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
