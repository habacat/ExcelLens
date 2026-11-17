package checkpoint

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointDiscardRevisionRejectsStaleAndUnavailableViewsAtomically(t *testing.T) {
	s, def, _ := fixture(t)
	lease := create(t, s, def)
	first := lease.Snapshot()
	if err := s.DiscardRevision(first.ID, first.Revision); !errors.Is(err, ErrBusy) {
		t.Fatal("active task was discarded", err)
	}
	final := advance(t, lease, `{"next":1}`, 1, []byte("exact\n"), true)
	_ = lease.Close()
	for _, revision := range []uint64{0, first.Revision, final.Revision + 1} {
		if err := s.DiscardRevision(first.ID, revision); !errors.Is(err, ErrConflict) {
			t.Fatal("stale/unavailable view removed a healthy task", revision, err)
		}
	}
	if err := s.DiscardRevision(final.ID, final.Revision); err != nil {
		t.Fatal(err)
	}
	other := create(t, s, def)
	id := other.Snapshot().ID
	_ = other.Close()
	if err := s.root.WriteFile(filepath.Join(id, "manifest"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.DiscardRevision(id, 0); err != nil {
		t.Fatal("explicit corrupt entry could not be discarded", err)
	}
	if _, err := s.root.Stat(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
