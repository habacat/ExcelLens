package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"excellens/internal/formats"
)

type failingReader struct {
	remaining int
}

func (r *failingReader) Read(out []byte) (int, error) {
	if r.remaining == 0 {
		return 0, errors.New("injected source read failure")
	}
	n := min(len(out), r.remaining)
	clear(out[:n])
	r.remaining -= n
	return n, nil
}

func TestCheckpointReadCancelAndDataSyncFailuresKeepThePriorCursor(t *testing.T) {
	for _, cause := range []string{"read", "short", "cancel", "data-sync"} {
		t.Run(cause, func(t *testing.T) {
			s, def, _ := fixture(t)
			l := create(t, s, def)
			before := advance(t, l, `{"next":1}`, 1, []byte("first confirmed row\n"), false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var reader io.Reader = &failingReader{remaining: 17}
			if cause == "short" {
				reader = strings.NewReader("short")
			}
			if cause == "cancel" {
				cancel()
				reader = strings.NewReader(strings.Repeat("x", 128))
			}
			if cause == "data-sync" {
				reader = strings.NewReader(strings.Repeat("x", 128))
				original, calls := l.syncData, 0
				l.syncData = func() error {
					calls++
					if calls == 1 {
						return errors.New("injected data sync failure")
					}
					return original()
				}
			}
			if _, err := l.Advance(ctx, Advance{Revision: before.Revision, Stage: "scan", Cursor: json.RawMessage(`{"next":2}`), Bytes: 128, Records: 1}, reader); err == nil {
				t.Fatal("failed append was acknowledged")
			}
			if m := l.Snapshot(); m.Revision != before.Revision || m.Records != before.Records || !bytes.Equal(m.Cursor, before.Cursor) {
				t.Fatal("failed append changed the acknowledged cursor", m)
			}
			info, _ := l.data.Stat()
			if info.Size() != before.DataBytes {
				t.Fatal("recoverable read/sync failure retained an unaccounted extension", info.Size())
			}
			if got := string(storedBytes(t, l)); got != "first confirmed row\n" {
				t.Fatal("prior prefix was damaged", got)
			}
		})
	}
}

type failedWriter struct {
	syncedWriter
	mode string
}

func (w failedWriter) Write(data []byte) (int, error) {
	if w.mode == "short-write" {
		return len(data) / 2, nil
	}
	return w.syncedWriter.Write(data)
}

func (w failedWriter) Sync() error {
	if w.mode == "metadata-sync" {
		return errors.New("injected metadata Sync failure")
	}
	return w.syncedWriter.Sync()
}

func (w failedWriter) Close() error {
	err := w.syncedWriter.Close()
	if w.mode == "metadata-close" {
		return errors.New("injected metadata Close failure")
	}
	return err
}

func TestCheckpointMetadataFailuresNeverAcknowledgeOrBlindlyRetryUncertainWrites(t *testing.T) {
	for _, failure := range []string{"short-write", "metadata-sync", "metadata-close", "rename-before", "rename-after", "directory-sync"} {
		t.Run(failure, func(t *testing.T) {
			s, def, _ := fixture(t)
			l := create(t, s, def)
			before := advance(t, l, `{"next":1}`, 1, []byte("one\n"), false)
			if failure == "short-write" || failure == "metadata-sync" || failure == "metadata-close" {
				original, calls := s.createTemporary, 0
				s.createTemporary = func(name string) (syncedWriter, error) {
					writer, err := original(name)
					calls++
					if err == nil && calls == 2 { // backup succeeds, candidate slot fails
						return failedWriter{writer, failure}, nil
					}
					return writer, err
				}
			}
			if strings.HasPrefix(failure, "rename-") {
				original := s.rename
				s.rename = func(from, to string) error {
					if filepath.Base(to) == "manifest" {
						if failure == "rename-after" {
							if err := original(from, to); err != nil {
								return err
							}
						}
						return errors.New("injected installation failure")
					}
					return original(from, to)
				}
			}
			if failure == "directory-sync" {
				original, calls := s.syncDirectory, 0
				s.syncDirectory = func(name string) error {
					calls++
					if calls == 2 {
						return errors.New("injected directory sync failure")
					}
					return original(name)
				}
			}
			_, err := l.Advance(context.Background(), Advance{Revision: before.Revision, Stage: "scan", Cursor: json.RawMessage(`{"next":2}`), Bytes: 4, Records: 1}, strings.NewReader("two\n"))
			if !errors.Is(err, ErrUncertain) || l.Snapshot().Revision != before.Revision {
				t.Fatal("an uncertain installation was acknowledged", err)
			}
			if _, err := l.Advance(context.Background(), Advance{Revision: before.Revision, Stage: "scan", Cursor: json.RawMessage(`{}`)}, nil); !errors.Is(err, ErrUncertain) {
				t.Fatal("same lease blindly retried an indeterminate append", err)
			}
			_ = l.Close()
			resumed, err := s.Acquire(context.Background(), before.ID, before.DefinitionHash)
			if err != nil {
				t.Fatal("reacquiring could not identify the installed durable slot", err)
			}
			defer resumed.Close()
			committed := failure == "rename-after" || failure == "directory-sync"
			want, records := "one\n", int64(1)
			if committed {
				want, records = "one\ntwo\n", 2
			}
			if string(storedBytes(t, resumed)) != want || resumed.Snapshot().Records != records {
				t.Fatal("ambiguous commit was duplicated, lost or truncated", resumed.Snapshot())
			}
		})
	}
}

func TestCheckpointConfirmedCorruptionNeverGetsTrimmedIntoSuccess(t *testing.T) {
	for _, damage := range []string{"payload", "sequence", "count", "size", "magic", "truncate-header", "truncate-payload", "swap-blocks"} {
		t.Run(damage, func(t *testing.T) {
			s, def, _ := fixture(t)
			l := create(t, s, def)
			advance(t, l, `{"next":1}`, 1, []byte("one\n"), false)
			m := advance(t, l, `{"next":2}`, 1, []byte("two\n"), false)
			_ = l.Close()
			name := filepath.Join(m.ID, "blocks")
			data, _ := s.root.ReadFile(name)
			switch damage {
			case "payload":
				data[blockHeaderBytes] ^= 1
			case "sequence":
				data[8]++
			case "count":
				data[16]++
			case "size":
				binary.LittleEndian.PutUint64(data[24:32], uint64(formats.CheckpointBlockBytes+1))
			case "magic":
				data[0]++
			case "truncate-header":
				data = data[:blockHeaderBytes-1]
			case "truncate-payload":
				data = data[:len(data)-1]
			case "swap-blocks":
				chunk := blockHeaderBytes + 4
				data = append(bytes.Clone(data[chunk:]), data[:chunk]...)
			}
			if err := s.root.WriteFile(name, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Acquire(context.Background(), m.ID, m.DefinitionHash); !errors.Is(err, ErrCorrupt) {
				t.Fatal("confirmed corruption accepted", damage, err)
			}
			after, _ := s.root.ReadFile(name)
			if !bytes.Equal(after, data) {
				t.Fatal("corruption was silently repaired/deleted")
			}
		})
	}
}

func TestCheckpointAReplacedDataFileOrChangedManifestIsNotOverwritten(t *testing.T) {
	for _, replaced := range []string{"blocks", "manifest"} {
		t.Run(replaced, func(t *testing.T) {
			s, def, _ := fixture(t)
			l := create(t, s, def)
			m := advance(t, l, `{"next":1}`, 1, []byte("one\n"), false)
			if replaced == "blocks" {
				if err := s.root.Rename(filepath.Join(m.ID, "blocks"), filepath.Join(m.ID, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := s.root.WriteFile(filepath.Join(m.ID, "blocks"), []byte("foreign file"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				m.Revision++
				m.Cursor = json.RawMessage(`{"other":true}`)
				data, _ := encodeManifest(m)
				if err := s.atomicFile(m.ID, "manifest", data); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := s.root.ReadFile(filepath.Join(m.ID, replaced))
			if _, err := l.Advance(context.Background(), Advance{Revision: l.Snapshot().Revision, Stage: "scan", Cursor: json.RawMessage(`{}`), Records: 1, Bytes: 4}, strings.NewReader("two\n")); err == nil {
				t.Fatal("replacement was overwritten")
			}
			after, _ := s.root.ReadFile(filepath.Join(m.ID, replaced))
			if !bytes.Equal(before, after) {
				t.Fatal("replacement changed despite the failed guard")
			}
		})
	}
}

func envelope(body []byte) []byte {
	data := make([]byte, manifestHeaderBytes+len(body))
	copy(data, manifestMagic)
	binary.LittleEndian.PutUint64(data[8:16], uint64(len(body)))
	digest := sha256.Sum256(body)
	copy(data[16:48], digest[:])
	copy(data[48:], body)
	return data
}

func TestCheckpointMetadataRejectsIncompatibleOrAmbiguousInterpretations(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	m := l.Snapshot()
	data, _ := encodeManifest(m)
	body := data[manifestHeaderBytes:]
	for _, invalid := range [][]byte{
		bytes.Replace(body, []byte(`"version":1`), []byte(`"version":2`), 1),
		bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1),
		bytes.Replace(body, []byte(`"revision":"1"`), []byte(`"revision":"01"`), 1),
		append(bytes.Clone(body), []byte(` {}`)...),
		[]byte(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)),
	} {
		if _, err := decodeManifest(bytes.NewReader(envelope(invalid))); !errors.Is(err, ErrCorrupt) {
			t.Fatal("incompatible, ambiguous or noncanonical metadata accepted", err)
		}
	}
	for _, invalid := range []Definition{
		{Kind: "script", Driver: "run", Semantics: 1, Sources: def.Sources, Payload: json.RawMessage(`{}`)},
		{Kind: def.Kind, Driver: "../run", Semantics: 1, Sources: def.Sources, Payload: json.RawMessage(`{}`)},
		{Kind: def.Kind, Driver: def.Driver, Semantics: 0, Sources: def.Sources, Payload: def.Payload},
		{Kind: def.Kind, Driver: def.Driver, Semantics: 1, Sources: def.Sources, Payload: json.RawMessage(`{"key":1,"key":2}`)},
	} {
		if _, err := s.Create(context.Background(), invalid, "scan", json.RawMessage(`{}`)); err == nil {
			t.Fatal("invalid task definition accepted")
		}
	}
}

func TestCheckpointSymlinkCannotMakeRecoveryWriteOutsideItsRoot(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	m := l.Snapshot()
	_ = l.Close()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("must keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.root.Remove(filepath.Join(m.ID, "blocks")); err != nil {
		t.Fatal(err)
	}
	if err := s.root.Symlink(outside, filepath.Join(m.ID, "blocks")); err != nil {
		t.Skipf("this test account cannot create filesystem symlinks: %v", err)
	}
	if _, err := s.Acquire(context.Background(), m.ID, m.DefinitionHash); err == nil {
		t.Fatal("external symlink was accepted")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "must keep" {
		t.Fatal("recovery altered an external file")
	}
}
