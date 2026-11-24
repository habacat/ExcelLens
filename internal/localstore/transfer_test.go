package localstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func put(t *testing.T, root, name, body string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreConflictsAndFileSelection(t *testing.T) {
	ctx := t.Context()
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	put(t, r.Config, "edit-documents/id/manifest", "confirmed transaction")
	put(t, r.Config, "edit-documents/id/blocks", "confirmed data")
	put(t, r.Config, "edit-documents/id/.lease", "runtime")
	put(t, r.Config, "edit-documents/id/.state-pending.tmp", "unconfirmed")
	put(t, r.Local, "temp/ExcelLens-upload-1.csv", "source")
	put(t, r.Local, "temp/original.csv", "unmanaged")
	put(t, r.Local, "exports/report.csv", "financial results")
	put(t, r.Local, "cache/index", "regenerable")
	destination := t.TempDir()
	if _, err := r.Backup(ctx, r.Local); err == nil {
		t.Fatal("recursive backup accepted")
	}
	folder, err := r.Backup(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config/edit-documents/id/.lease", "config/edit-documents/id/.state-pending.tmp", "local/temp/original.csv", "local/cache/index"} {
		if _, err := os.Stat(filepath.Join(folder, name)); !os.IsNotExist(err) {
			t.Fatalf("unexpected backup entry %s", name)
		}
	}
	target := Roots{Config: t.TempDir(), Local: t.TempDir()}
	put(t, target.Local, "exports/report.csv", "different confirmed result")
	if err := target.Restore(ctx, folder); err == nil {
		t.Fatal("conflicting group overwritten")
	}
	if _, err := os.Stat(filepath.Join(target.Config, "edit-documents")); !os.IsNotExist(err) {
		t.Fatal("conflict published partial data")
	}
	if b, _ := os.ReadFile(filepath.Join(target.Local, "exports/report.csv")); string(b) != "different confirmed result" {
		t.Fatal("existing result changed")
	}
	fresh := Roots{Config: t.TempDir(), Local: t.TempDir()}
	for i := 0; i < 2; i++ {
		if err := fresh.Restore(ctx, folder); err != nil {
			t.Fatal("restore or retry", err)
		}
	}
	for name, want := range map[string]string{"edit-documents/id/manifest": "confirmed transaction", "edit-documents/id/blocks": "confirmed data"} {
		if b, err := os.ReadFile(filepath.Join(fresh.Config, name)); err != nil || string(b) != want {
			t.Fatal(name, string(b), err)
		}
	}
	if b, err := os.ReadFile(filepath.Join(fresh.Local, "temp/ExcelLens-upload-1.csv")); err != nil || string(b) != "source" {
		t.Fatal("missing uploaded base")
	}
	put(t, folder, "config/edit-documents/id/blocks", "altered")
	bad := Roots{Config: t.TempDir(), Local: t.TempDir()}
	if err := bad.Restore(ctx, folder); err == nil {
		t.Fatal("corrupt backup installed")
	}
	if _, err := os.Stat(filepath.Join(bad.Config, "edit-documents")); !os.IsNotExist(err) {
		t.Fatal("partial corrupt group installed")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Backup(ctx, destination); err == nil {
		t.Fatal("cancelled backup published")
	}
	entries, _ := os.ReadDir(destination)
	if len(entries) != 1 {
		t.Fatal("incomplete backup left behind", entries)
	}
}

func TestLegacyMigrationKeepsNewerWorkAndReportsAllocatedBytes(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	key := strings.Repeat("a", 40) + ".json"
	put(t, r.Local, "annotations/"+key, "old records")
	got, err := r.MigrateLegacy(t.Context())
	if err != nil || got.Copied != 1 {
		t.Fatal(got, err)
	}
	got, err = r.MigrateLegacy(t.Context())
	if err != nil || got.Identical != 1 {
		t.Fatal(got, err)
	}
	put(t, r.Config, "annotations/"+key, "new records")
	got, err = r.MigrateLegacy(t.Context())
	if err != nil || got.Different != 1 {
		t.Fatal(got, err)
	}
	for dir, want := range map[string]string{r.Config: "new records", r.Local: "old records"} {
		if b, err := os.ReadFile(filepath.Join(dir, "annotations", key)); string(b) != want || err != nil {
			t.Fatal(string(b), err)
		}
	}
	found := false
	for _, row := range r.Inventory(t.Context()) {
		if row.Error != "" {
			t.Fatal(row.Error)
		}
		if row.Name == "审计标记" {
			found = row.Files == 1 && row.Bytes == 11 && row.Allocated >= 0
		}
	}
	if !found {
		t.Fatal("missing or incorrect measured storage")
	}
}
