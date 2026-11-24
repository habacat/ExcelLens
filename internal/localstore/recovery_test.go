package localstore

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedVolumeRoundTripAndRejection(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	name := "exports/客户保单.csv"
	body := strings.Repeat("internal-financial-record", 13000)
	put(t, r.Local, name, body)
	put(t, r.Config, "settings/empty", "")
	folder, err := r.BackupWith(t.Context(), t.TempDir(), TransferOptions{Password: "审计 passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(folder, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if strings.Contains(p, "客户") || bytes.Contains(b, []byte("internal-financial-record")) || bytes.Contains(b, []byte("客户")) {
			t.Fatal("plaintext in sealed backup")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, pass := range []string{"", "wrong", "审计 passphrase"} {
		to := Roots{Config: t.TempDir(), Local: t.TempDir()}
		err := to.RestoreWith(t.Context(), folder, TransferOptions{Password: pass})
		if pass == "审计 passphrase" {
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(filepath.Join(to.Local, name))
			if string(b) != body {
				t.Fatal("round trip mismatch")
			}
		} else if err == nil {
			t.Fatal("missing or wrong password accepted")
		}
	}
	objects, _ := os.ReadDir(filepath.Join(folder, "objects"))
	for _, obj := range objects {
		p := filepath.Join(folder, "objects", obj.Name())
		b, _ := os.ReadFile(p)
		if len(b) > 100000 {
			b[70000] ^= 1
			if err := os.WriteFile(p, b, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	to := Roots{Config: t.TempDir(), Local: t.TempDir()}
	if err := to.RestoreWith(t.Context(), folder, TransferOptions{Password: "审计 passphrase"}); err == nil {
		t.Fatal("damaged ciphertext accepted")
	}
	if _, err := os.Stat(filepath.Join(to.Config, "settings/empty")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial publish", err)
	}
}
func TestCommittedRestoreRecoversWithoutBackupOrPassword(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	put(t, r.Config, "settings/a", "one")
	put(t, r.Local, "exports/b", "two")
	folder, err := r.BackupWith(t.Context(), t.TempDir(), TransferOptions{Password: "passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	to := Roots{Config: t.TempDir(), Local: t.TempDir()}
	// A collision after PREPARE forces failure after the first real publication.
	err = to.RestoreWith(t.Context(), folder, TransferOptions{Password: "passphrase", Report: func(p Progress) {
		if p.Phase == "提交恢复事务" && p.Files == 0 {
			put(t, to.Local, "exports/b", "other")
		}
	}})
	if err == nil || !to.Pending() {
		t.Fatal("expected pending transaction", err)
	}
	if b, _ := os.ReadFile(filepath.Join(to.Config, "settings/a")); string(b) != "one" {
		t.Fatal("first unit not installed")
	}
	if err := os.Rename(folder, folder+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err := to.Recover(t.Context()); err == nil {
		t.Fatal("conflicting work overwritten")
	}
	if err := os.Remove(filepath.Join(to.Local, "exports/b")); err != nil {
		t.Fatal(err)
	}
	// Model power loss between durable rename and acknowledging that unit.
	stages, _ := filepath.Glob(filepath.Join(to.Config, ".restore-*-config"))
	if len(stages) != 1 {
		t.Fatal(stages)
	}
	if err := os.Remove(filepath.Join(stages[0], ".next")); err != nil {
		t.Fatal(err)
	}
	if err := to.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if to.Pending() {
		t.Fatal("decision left behind")
	}
	if b, _ := os.ReadFile(filepath.Join(to.Local, "exports/b")); string(b) != "two" {
		t.Fatal("lost pending unit")
	}
}
func TestCancelDuringRestorePreparationPublishesNothing(t *testing.T) {
	r := Roots{Config: t.TempDir(), Local: t.TempDir()}
	put(t, r.Config, "layouts/a", "layout")
	folder, err := r.Backup(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	to := Roots{Config: t.TempDir(), Local: t.TempDir()}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = to.RestoreWith(ctx, folder, TransferOptions{Report: func(p Progress) {
		if p.Phase == "核验并暂存" {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) || to.Pending() {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(to.Config, "layouts")); !os.IsNotExist(err) {
		t.Fatal("cancelled data installed")
	}
}
