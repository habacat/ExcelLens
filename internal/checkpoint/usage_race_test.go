package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/formats"
)

func TestCheckpointAdmissionToleratesOnlyRenamedOwnedInstallFromDirectorySnapshot(t *testing.T) {
	s, def, _ := fixture(t)
	lease := create(t, s, def)
	m := lease.Snapshot()
	for _, test := range []struct {
		name      string
		transient bool
	}{
		{".state-0123456789abcdef0123456789abcdef.tmp", true},
		{".state-unowned.tmp", false}, {"manifest", false}, {"blocks", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(m.ID, test.name)
			var original []byte
			if test.name == "manifest" || test.name == "blocks" {
				var err error
				original, err = s.root.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
			} else if err := s.root.WriteFile(path, []byte("install contents"), 0600); err != nil {
				t.Fatal(err)
			}
			directory, err := s.root.Open(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := directory.ReadDir(-1)
			_ = directory.Close()
			if err != nil {
				t.Fatal(err)
			}
			var captured os.DirEntry
			for _, entry := range entries {
				if entry.Name() == test.name {
					captured = entry
					break
				}
			}
			if captured == nil {
				t.Fatal("directory snapshot missed the fixture")
			}
			moved := filepath.Join(m.ID, "moved-for-test")
			if err := s.root.Rename(path, moved); err != nil {
				t.Fatal(err)
			}
			info, err := s.usageEntry(m.ID, captured)
			if test.transient {
				if err != nil || info != nil {
					t.Fatal("atomic install was misclassified as corruption", info, err)
				}
			} else if !errors.Is(err, ErrCorrupt) {
				t.Fatal("stable/unknown disappearance was silently accepted", err)
			}
			if err := s.root.Remove(moved); err != nil {
				t.Fatal(err)
			}
			if original != nil || test.name == "blocks" {
				if err := s.root.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	_, used, err := s.usage()
	if err != nil || used < 3*int64(formats.CheckpointManifestBytes) {
		t.Fatal("transient handling bypassed the metadata reservation", used, err)
	}
}
