package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func fixture(t *testing.T) (*Store, Definition, string) {
	t.Helper()
	directory := t.TempDir()
	s, err := Open(filepath.Join(directory, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	source := filepath.Join(directory, "source.csv")
	if err := os.WriteFile(source, []byte("编号,金额\n001,9007199254740993.31\n"), 0600); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(source)
	digest := sha256.Sum256(data)
	def := Definition{Kind: "export", Driver: "source-export", Semantics: 1, Title: "金额明细", Sources: []Source{{Path: source, Book: "path-identity", Fingerprint: hex.EncodeToString(digest[:]), Format: "csv", Source: "wb:数据", Stamp: "source-stamp"}}, Payload: json.RawMessage(`{"scope":"sheet","mode":"values","protectCSV":true,"range":"A1:B3"}`)}
	return s, def, hex.EncodeToString(digest[:])
}

func create(t *testing.T, s *Store, def Definition) *Lease {
	t.Helper()
	l, err := s.Create(context.Background(), def, "scan", json.RawMessage(`{"next":0}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func advance(t *testing.T, l *Lease, cursor string, records int64, data []byte, complete bool) Manifest {
	t.Helper()
	m, err := l.Advance(context.Background(), Advance{Revision: l.Snapshot().Revision, Stage: "scan", Cursor: json.RawMessage(cursor), Records: records, Bytes: int64(len(data)), Complete: complete}, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func storedBytes(t *testing.T, l *Lease) []byte {
	t.Helper()
	var out bytes.Buffer
	err := l.Visit(context.Background(), func(_ Block, reader io.Reader) error {
		_, err := io.Copy(&out, reader)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCheckpointExactBytesAndCursorSurviveReopenWithDetachedOwnership(t *testing.T) {
	s, def, sourceHash := fixture(t)
	identity, err := DefinitionHash(def)
	if err != nil {
		t.Fatal(err)
	}
	l := create(t, s, def)
	def.Payload[2] = 'X'
	def.Sources[0].Stamp = "changed caller"
	copy := l.Snapshot()
	copy.Cursor[2] = 'X'
	copy.Definition.Payload[2] = 'X'
	copy.Definition.Sources[0].Stamp = "changed snapshot"
	first := []byte("001,9007199254740993.31,\"两\n行\",=2+2\r\n")
	second := []byte("0002,-0.00000000000000000000001,缺缓存\r\n")
	advance(t, l, `{"next":1}`, 1, first, false)
	advance(t, l, `{"next":2}`, 1, second, false)
	m := advance(t, l, `{"next":2,"stage":"written"}`, 0, nil, true)
	if m.Records != 2 || m.Blocks != 2 || m.Revision != 4 || m.DataBytes != int64(len(first)+len(second)+2*blockHeaderBytes) || m.DefinitionHash != identity {
		t.Fatal("wrong complete checkpoint", m)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	directory := s.root.Name()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	resumed, err := reopened.Acquire(context.Background(), m.ID, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if got := resumed.Snapshot(); !reflect.DeepEqual(got.Cursor, m.Cursor) || !got.Complete || got.Definition.Sources[0].Stamp != "source-stamp" {
		t.Fatal("source/cursor ownership was not durable", got)
	}
	if !bytes.Equal(storedBytes(t, resumed), append(first, second...)) {
		t.Fatal("raw amount, leading zero, multiline or formula text changed")
	}
	if _, err := resumed.Advance(context.Background(), Advance{Revision: m.Revision, Stage: "scan", Cursor: json.RawMessage(`{}`)}, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("completed task accepted a new append", err)
	}
	data, _ := os.ReadFile(m.Definition.Sources[0].Path)
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != sourceHash {
		t.Fatal("source file was modified")
	}
}

func TestCheckpointResumeValidatesBeforeTrimmingUnconfirmedTail(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	payload := []byte("confirmed exact data\n")
	m := advance(t, l, `{"next":1}`, 1, payload, false)
	_ = l.Close()
	file, err := s.regular(filepath.Join(m.ID, "blocks"), os.O_WRONLY|os.O_APPEND)
	if err != nil {
		t.Fatal(err)
	}
	tail := []byte("unacknowledged partial \"CSV record")
	_, _ = file.Write(tail)
	_ = file.Close()
	wrong := strings.Repeat("1", 64)
	if _, err := s.Acquire(context.Background(), m.ID, wrong); !errors.Is(err, ErrBinding) {
		t.Fatal("foreign source/rules were accepted", err)
	}
	info, _ := s.root.Stat(filepath.Join(m.ID, "blocks"))
	if info.Size() != m.DataBytes+int64(len(tail)) {
		t.Fatal("binding failure modified retained data")
	}
	resumed, err := s.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	info, _ = s.root.Stat(filepath.Join(m.ID, "blocks"))
	if info.Size() != m.DataBytes || !bytes.Equal(storedBytes(t, resumed), payload) || resumed.Snapshot().Records != 1 {
		t.Fatal("resume did not retain exactly the acknowledged prefix")
	}
}

func TestCheckpointExclusiveOwnersListAndDiscardAcrossStoreHandles(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	m := advance(t, l, `{"next":1}`, 1, []byte("record\n"), false)
	other, err := Open(s.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Acquire(context.Background(), m.ID, m.DefinitionHash); !errors.Is(err, ErrBusy) {
		t.Fatal("second writer was admitted", err)
	}
	if err := other.Discard(m.ID); !errors.Is(err, ErrBusy) {
		t.Fatal("active owner was discarded", err)
	}
	if err := s.Close(); !errors.Is(err, ErrBusy) {
		t.Fatal("store close invalidated an active owner", err)
	}
	entries, err := other.List()
	if err != nil || len(entries) != 1 || !entries[0].Busy || entries[0].Manifest.Records != 1 {
		t.Fatal("lightweight catalogue lost the confirmed state", entries, err)
	}
	_ = l.Close()
	if err := other.Discard(m.ID); err != nil {
		t.Fatal(err)
	}
	entries, err = s.List()
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestCheckpointBackupOnlyWhenMainSlotIsMissing(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "corrupt"}[corrupt], func(t *testing.T) {
			s, def, _ := fixture(t)
			l := create(t, s, def)
			first := advance(t, l, `{"next":1}`, 1, []byte("one\n"), false)
			latest := advance(t, l, `{"next":2}`, 1, []byte("two\n"), false)
			_ = l.Close()
			if corrupt {
				if err := s.root.WriteFile(filepath.Join(latest.ID, "manifest"), []byte("broken current slot"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := s.root.Remove(filepath.Join(latest.ID, "manifest")); err != nil {
				t.Fatal(err)
			}
			resumed, err := s.Acquire(context.Background(), latest.ID, latest.DefinitionHash)
			if corrupt {
				if !errors.Is(err, ErrCorrupt) {
					t.Fatal("corruption silently fell back to an older success", err)
				}
				info, _ := s.root.Stat(filepath.Join(latest.ID, "blocks"))
				if info.Size() != latest.DataBytes {
					t.Fatal("corrupt task was altered")
				}
				entries, err := s.List()
				if err != nil || entries[0].Error == "" || entries[0].Manifest != nil {
					t.Fatal("corrupt checkpoint was displayed as an empty/valid result", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close()
			if !resumed.FromBackup() || resumed.Snapshot().Revision != first.Revision || !bytes.Equal(storedBytes(t, resumed), []byte("one\n")) {
				t.Fatal("missing-slot recovery did not expose its older confirmed state")
			}
		})
	}
}

func TestCheckpointUnknownFilesAndUnsafeIDsNeverDeleteOutsideTheTask(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	m := l.Snapshot()
	_ = l.Close()
	for _, id := range []string{"../outside", strings.Repeat("A", 32), "", m.ID + "/.."} {
		if err := s.Discard(id); !errors.Is(err, ErrBinding) {
			t.Fatal("unsafe identifier was not refused", id, err)
		}
	}
	if err := s.root.WriteFile(filepath.Join(m.ID, "user-file.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Discard(m.ID); err == nil {
		t.Fatal("unknown task content was recursively deleted")
	}
	if got, err := s.root.ReadFile(filepath.Join(m.ID, "user-file.txt")); err != nil || string(got) != "keep" {
		t.Fatal("unrecognized file was removed", err)
	}
}

func TestCheckpointExactCounterOverflowAndBoundaries(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	m := advance(t, l, `{"next":"9223372036854775807"}`, math.MaxInt64, []byte("complete driver records"), false)
	if m.Records != math.MaxInt64 {
		t.Fatal("counter was rounded")
	}
	for _, next := range []Advance{
		{Revision: m.Revision, Stage: "scan", Cursor: json.RawMessage(`{}`), Records: 1, Bytes: 1},
		{Revision: m.Revision, Stage: "scan", Cursor: json.RawMessage(`{}`), Records: 1},
		{Revision: m.Revision, Stage: "scan", Cursor: json.RawMessage(`{}`), Bytes: formats.CheckpointBlockBytes + 1},
		{Revision: m.Revision, Stage: "scan", Cursor: json.RawMessage(`{"x":1,"x":2}`)},
		{Revision: m.Revision - 1, Stage: "scan", Cursor: json.RawMessage(`{}`)},
	} {
		if _, err := l.Advance(context.Background(), next, strings.NewReader("x")); err == nil {
			t.Fatal("invalid or stale advance was accepted", next)
		}
	}
	if got := l.Snapshot(); got.Revision != m.Revision || got.DataBytes != m.DataBytes {
		t.Fatal("rejected request modified confirmed state")
	}
}

func TestCheckpointTaskCapacityIsSharedAndNotLostOnReopen(t *testing.T) {
	s, def, _ := fixture(t)
	for range formats.CheckpointTasks {
		l := create(t, s, def)
		_ = l.Close()
	}
	other, err := Open(s.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Create(context.Background(), def, "scan", json.RawMessage(`{}`)); !errors.Is(err, ErrCapacity) {
		t.Fatal("reopen bypassed the persisted task quota", err)
	}
	entries, err := other.List()
	if err != nil || len(entries) != formats.CheckpointTasks {
		t.Fatal("quota rejection discarded existing checkpoints", len(entries), err)
	}
}

func TestCheckpointVisitRequiresCompleteConsumptionAndRetainsDataOnConsumerFailure(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	advance(t, l, `{"next":1}`, 1, []byte("original\n"), false)
	if err := l.Visit(context.Background(), func(Block, io.Reader) error { return nil }); !errors.Is(err, ErrCorrupt) {
		t.Fatal("partial block consumption was accepted", err)
	}
	failure := errors.New("consumer failure")
	if err := l.Visit(context.Background(), func(Block, io.Reader) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if string(storedBytes(t, l)) != "original\n" {
		t.Fatal("consumer failure damaged the checkpoint")
	}
}
