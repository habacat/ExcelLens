package editdoc

import (
	"errors"
	"fmt"
	"testing"

	"excellens/internal/checkpoint"
	"excellens/internal/formats"
)

func TestCompactionPreservesBranchesAndIdempotencyAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := createTestDocument(t, store, root)
	load := func() *Document {
		t.Helper()
		d, err := Load(t.Context(), store, entry.Key)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := load()
	defer func() { d.Close() }()
	addr := Address{"Data", 1, 1}
	read := func([]Change) (map[Address]Value, error) { return map[Address]Value{addr: {Kind: "blank"}}, nil }
	var last Operation
	send := func(action, value string) {
		t.Helper()
		last = Operation{Revision: d.Revision, Request: fmt.Sprintf("%032x", d.Revision), Action: action}
		if action == "change" {
			last.Changes = []Change{{addr, Value{Kind: "text", Raw: value}}}
		}
		if _, err := d.Commit(t.Context(), last, read, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 7; i++ {
		send("change", fmt.Sprint(i))
	}
	send("undo", "")
	send("undo", "")
	retry := last
	if err = d.Compact(t.Context(), 3); err != nil {
		t.Fatal(err)
	}
	d.Close()
	d = load()
	check := func(want string) {
		t.Helper()
		if got := d.ValueAt(addr, Value{Kind: "blank"}).Raw; got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
	check("5")
	if rev, err := d.Commit(t.Context(), retry, read, nil); err != nil || rev != retry.Revision+1 {
		t.Fatalf("retry: %d %v", rev, err)
	}
	send("redo", "")
	check("6")
	send("redo", "")
	check("7")
	send("undo", "")
	send("change", "branch")
	if d.Redo {
		t.Fatal("discarded redo branch survived")
	}
	// Cross the real byte threshold in the commit path without writing 4096
	// manifests during every normal unit-test run.
	d.dataSize = formats.EditHistoryBytes
	send("change", "after checkpoint")
	d.Close()
	d = load()
	check("after checkpoint")
	send("undo", "")
	check("branch")
	send("redo", "")
	check("after checkpoint")
	_, err = d.Commit(t.Context(), Operation{Request: fmt.Sprintf("%032x", 990), Revision: 1, Action: "undo"}, read, nil)
	if !errors.Is(err, checkpoint.ErrConflict) {
		t.Fatal(err)
	}
}
