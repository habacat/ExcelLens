package checkpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"excellens/internal/formats"
)

func TestCheckpointStreamCommitsOneCompletePartAcrossPhysicalBlocks(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	before := l.Snapshot()
	const extra = 257
	length := formats.CheckpointBlockBytes + extra
	pattern := []byte("001,9007199254740993.31,\"两\n行\"\r\n")
	m, err := l.AdvanceStream(context.Background(), before.Revision, func(out io.Writer) (Advance, error) {
		_, err := io.CopyBuffer(out, &repeatedRecords{pattern: pattern, remaining: length}, make([]byte, formats.CheckpointIOBytes))
		return Advance{Revision: before.Revision, Stage: "parts", Cursor: json.RawMessage(`{"parts":1}`), Records: 1}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Revision != before.Revision+1 || m.Blocks != 2 || m.Records != 1 || m.DataBytes != length+2*blockHeaderBytes {
		t.Fatal("one part was acknowledged as separate records or dropped bytes", m)
	}
	info, _ := l.data.Stat()
	if info.Size() != m.DataBytes {
		t.Fatal("reservation slack leaked beyond confirmed data", info.Size(), m.DataBytes)
	}
	_ = l.Close()
	l, err = s.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var position int64
	blocks := 0
	if err := l.VisitCandidate(context.Background(), func(block Block, reader io.Reader) error {
		blocks++
		if block.Records != int64(blocks-1) {
			t.Fatal("part record count was placed before the final block", block)
		}
		buffer := make([]byte, formats.CheckpointIOBytes)
		for {
			n, err := reader.Read(buffer)
			for _, b := range buffer[:n] {
				if b != pattern[position%int64(len(pattern))] {
					t.Fatal("stream changed exact part bytes", position)
				}
				position++
			}
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}); err != nil || position != length {
		t.Fatal("incomplete confirmed part", position, length, err)
	}
}

func TestCheckpointStreamFailuresPreserveOldPrefixAndAcknowledgement(t *testing.T) {
	for _, cause := range []string{"producer", "cancel", "cursor", "revision", "data-sync", "metadata", "ignored-write"} {
		t.Run(cause, func(t *testing.T) {
			s, def, _ := fixture(t)
			l := create(t, s, def)
			before := advance(t, l, `{"part":1}`, 1, []byte("confirmed\n"), false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sentinel := errors.New("injected complete-part failure")
			if cause == "data-sync" {
				original, calls := l.syncData, 0
				l.syncData = func() error {
					calls++
					if calls == 1 {
						return sentinel
					}
					return original()
				}
			}
			if cause == "metadata" {
				l.store.rename = func(string, string) error { return sentinel }
			}
			_, err := l.AdvanceStream(ctx, before.Revision, func(out io.Writer) (Advance, error) {
				next := Advance{Revision: before.Revision, Stage: "parts", Cursor: json.RawMessage(`{"part":2}`), Records: 1}
				if _, err := out.Write(bytes.Repeat([]byte("x"), 12345)); err != nil {
					return next, err
				}
				switch cause {
				case "producer":
					return next, sentinel
				case "cancel":
					cancel()
				case "ignored-write":
					cancel()
					_, _ = out.Write([]byte("ignored cancellation"))
				case "cursor":
					next.Cursor = []byte(`{"broken"`)
				case "revision":
					next.Revision++
				}
				return next, nil
			})
			if err == nil || l.Snapshot().Revision != before.Revision {
				t.Fatal("failed stream advanced an acknowledged cursor", cause, err)
			}
			if cause == "metadata" {
				if !errors.Is(err, ErrUncertain) {
					t.Fatal("uncertain metadata write could be blindly replayed", err)
				}
				return
			}
			info, _ := l.data.Stat()
			if info.Size() != before.DataBytes || string(storedBytes(t, l)) != "confirmed\n" {
				t.Fatal("failed stream retained quota or damaged the confirmed prefix", info.Size())
			}
		})
	}
}

func TestCheckpointStreamEmptyTransitionAndPanicDoNotReserveOrPublishARecord(t *testing.T) {
	s, def, _ := fixture(t)
	l := create(t, s, def)
	before := l.Snapshot()
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("producer panic was swallowed")
			}
		}()
		_, _ = l.AdvanceStream(context.Background(), before.Revision, func(out io.Writer) (Advance, error) {
			_, _ = out.Write([]byte("unconfirmed"))
			panic("injected producer panic")
		})
	}()
	info, _ := l.data.Stat()
	if info.Size() != before.DataBytes || l.Snapshot().Revision != before.Revision {
		t.Fatal("panicking producer left a tail or confirmed a record")
	}
	m, err := l.AdvanceStream(context.Background(), before.Revision, func(io.Writer) (Advance, error) {
		return Advance{Revision: before.Revision, Stage: "ready", Cursor: []byte(`{}`)}, nil
	})
	if err != nil || m.Blocks != 0 || m.Records != 0 || m.Revision != before.Revision+1 {
		t.Fatal("empty structure transition failed", m, err)
	}
}
