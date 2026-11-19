package editdoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"excellens/internal/checkpoint"
)

func createTestDocument(t *testing.T, store *checkpoint.Store, root string) Entry {
	t.Helper()
	e, err := Create(t.Context(), store, Base{
		Source: checkpoint.Source{Path: filepath.Join(root, "source.csv"), Book: "book", Fingerprint: "raw", Format: "csv", Source: "csv", Stamp: "sha"},
		Name:   "source.csv", Layout: json.RawMessage(`{"test":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDurableEditsUndoRedoAndRetry(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	e := createTestDocument(t, store, root)
	load := func() *Document {
		t.Helper()
		d, err := Load(t.Context(), store, e.Key)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	a, b := Address{"Data", 1, 2}, Address{"Data", 999999, 3}
	base := map[Address]Value{a: {Kind: "text", Raw: "00123"}, b: {Kind: "blank"}}
	read := func([]Change) (map[Address]Value, error) { return base, nil }
	commit := func(d *Document, op Operation, expect uint64, want error) {
		t.Helper()
		rev, err := d.Commit(t.Context(), op, read, nil)
		if rev != expect || !errors.Is(err, want) {
			t.Fatalf("%s: got %d %v, want %d %v", op.Action, rev, err, expect, want)
		}
	}
	id := func(n int) string { return fmt.Sprintf("%032x", n) }
	change := Operation{Request: id(1), Revision: 1, Action: "change", Changes: []Change{{a, Value{Kind: "text", Raw: "00124"}}, {b, Value{Kind: "number", Raw: "9007199254740993.25"}}}}
	d := load()
	bad := Operation{Request: id(2), Revision: 1, Action: "change", Changes: []Change{{a, Value{Kind: "text", Raw: "edited"}}, {a, Value{Kind: "blank"}}}}
	commit(d, bad, 0, ErrInvalid)
	if d.Revision != 1 || len(d.Snapshot()) != 0 {
		t.Fatal("invalid batch partially applied")
	}
	commit(d, change, 2, nil)
	if _, err := Load(t.Context(), store, e.Key); !errors.Is(err, checkpoint.ErrBusy) {
		t.Fatalf("second owner acquired journal: %v", err)
	}
	frozen := d.Snapshot()
	commit(d, Operation{Request: id(3), Revision: 1, Action: "undo"}, 0, checkpoint.ErrConflict)
	commit(d, Operation{Request: id(4), Revision: 2, Action: "undo"}, 3, nil)
	if len(d.Snapshot()) != 0 || !d.Redo || d.Undo || frozen[b].Raw != "9007199254740993.25" {
		t.Fatalf("undo or immutable snapshot failed: %+v %+v", d.Info, frozen)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	d = load()
	defer d.Close()
	commit(d, change, 2, nil)
	if d.Revision != 3 || !d.Redo {
		t.Fatal("retry unexpectedly reapplied original change")
	}
	change.Changes[0].Value.Raw = "different"
	commit(d, change, 0, checkpoint.ErrConflict)
	commit(d, Operation{Request: id(5), Revision: 3, Action: "redo"}, 4, nil)
	commit(d, Operation{Request: id(6), Revision: 4, Action: "undo"}, 5, nil)
	commit(d, Operation{Request: id(7), Revision: 5, Action: "change", Changes: []Change{{a, Value{Kind: "blank"}}}}, 6, nil)
	if d.Redo || d.ValueAt(a, base[a]).Kind != "blank" || d.ValueAt(b, base[b]) != base[b] {
		t.Fatalf("new branch was incorrect: %+v", d.Info)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := d.Commit(ctx, Operation{Request: id(8), Revision: 6, Action: "undo"}, read, nil); !errors.Is(err, context.Canceled) || d.Revision != 6 {
		t.Fatalf("cancel committed: %v %+v", err, d.Info)
	}
}

func TestDamagedEditManifestDoesNotRollBackConfirmedBlocks(t *testing.T) {
	root := t.TempDir()
	store, err := checkpoint.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e := createTestDocument(t, store, root)
	d, err := Load(t.Context(), store, e.Key)
	if err != nil {
		t.Fatal(err)
	}
	a := Address{"Data", 1, 1}
	_, err = d.Commit(t.Context(), Operation{Request: fmt.Sprintf("%032x", 1), Revision: 1, Action: "change", Changes: []Change{{a, Value{Kind: "text", Raw: "saved"}}}}, func([]Change) (map[Address]Value, error) {
		return map[Address]Value{a: {Kind: "blank"}}, nil
	}, nil)
	closeErr := d.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("commit: %v, close: %v", err, closeErr)
	}
	blocks := filepath.Join(root, e.ID, "blocks")
	before, err := os.ReadFile(blocks)
	if err != nil || len(before) == 0 {
		t.Fatal("missing confirmed block", err)
	}
	if err := os.WriteFile(filepath.Join(root, e.ID, "manifest"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), store, e.Key); !errors.Is(err, checkpoint.ErrCorrupt) {
		t.Fatalf("damaged primary silently recovered: %v", err)
	}
	after, err := os.ReadFile(blocks)
	if err != nil || string(after) != string(before) {
		t.Fatal("confirmed block was changed", err)
	}
}

func TestCalculatedColumnRestoresTemplateEvenWhenEveryMemberIsAnException(t *testing.T) {
	store, err := checkpoint.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := createTestDocument(t, store, t.TempDir())
	open := func() *Document {
		t.Helper()
		d, err := Load(t.Context(), store, entry.Key)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	anchor := Address{"Data", 2, 3}
	second := Address{"Data", 3, 3}
	base := map[Address]Value{anchor: {Kind: "formula", Raw: "=A2"}, second: {Kind: "formula", Raw: "=A3"}}
	changes := []Change{{anchor, Value{Kind: "formula", Raw: "=A2*2"}}, {second, Value{Kind: "formula", Raw: "=A3*2"}}}
	op := Operation{Request: fmt.Sprintf("%032x", 1), Revision: 1, Action: "change", Changes: changes[1:]}
	d := open()
	defer func() { _ = d.Close() }()
	if _, err = d.CommitPlanned(t.Context(), op, func([]Change) (PreparedChanges, error) {
		return PreparedChanges{changes, base, []ColumnFormula{{anchor, "=A2", "=A2*2"}}}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d = open()
	if rev, err := d.CommitPlanned(t.Context(), op, nil, nil); err != nil || rev != 2 {
		t.Fatal(rev, err)
	}
	for i, action := range []string{"undo", "redo"} {
		if _, err = d.Commit(t.Context(), Operation{Request: fmt.Sprintf("%032x", i+2), Revision: d.Revision, Action: action}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if (d.ColumnFormula(anchor, "=A2") == "=A2*2") != (action == "redo") || (d.ValueAt(second, base[second]).Raw == "=A3*2") != (action == "redo") {
			t.Fatal(action, d.ColumnFormulas(), d.Snapshot())
		}
	}
	values := []Change{{anchor, Value{Kind: "number", Raw: "1"}}, {second, Value{Kind: "number", Raw: "2"}}}
	if _, err = d.Commit(t.Context(), Operation{Request: fmt.Sprintf("%032x", 4), Revision: d.Revision, Action: "change", Changes: values}, func([]Change) (map[Address]Value, error) { return base, nil }, nil); err != nil {
		t.Fatal(err)
	}
	if d.ColumnFormula(anchor, "=A2") != "=A2*2" {
		t.Fatal("literal exceptions erased the stored column template")
	}
	// A full-column overwrite may only remove its template. Replaying and
	// undoing this entry must distinguish removal from inheriting the source.
	clearTemplate := Operation{Request: fmt.Sprintf("%032x", 5), Revision: d.Revision, Action: "change", Changes: values}
	if _, err = d.CommitPlanned(t.Context(), clearTemplate, func([]Change) (PreparedChanges, error) {
		return PreparedChanges{values, base, []ColumnFormula{{anchor, "=A2", ""}}}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err = d.Close(); err != nil {
		t.Fatal(err)
	}
	d = open()
	if d.ColumnFormula(anchor, "=A2") != "" {
		t.Fatal(d.ColumnFormulas())
	}
	for i, action := range []string{"undo", "redo"} {
		if _, err = d.Commit(t.Context(), Operation{Request: fmt.Sprintf("%032x", i+6), Revision: d.Revision, Action: action}, nil, nil); err != nil {
			t.Fatal(err)
		}
		if (d.ColumnFormula(anchor, "=A2") == "") != (action == "redo") {
			t.Fatal(action, d.ColumnFormulas())
		}
	}
}
