package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestCheckpointPrivateCandidateStreamValidatesBeforePublication(t *testing.T) {
	s, def, _ := fixture(t)
	lease := create(t, s, def)
	advance(t, lease, `{"next":2}`, 2, []byte("first\nsecond\n"), false)
	advance(t, lease, `{"next":3}`, 1, []byte("third\n"), true)
	var candidate bytes.Buffer
	err := lease.VisitCandidate(context.Background(), func(_ Block, source io.Reader) error {
		_, err := io.Copy(&candidate, source)
		return err
	})
	if err != nil || candidate.String() != "first\nsecond\nthird\n" {
		t.Fatal(candidate.String(), err)
	}
	if err := lease.VisitCandidate(context.Background(), func(Block, io.Reader) error { return nil }); !errors.Is(err, ErrCorrupt) {
		t.Fatal("unconsumed candidate bytes became success", err)
	}
	failure := errors.New("private row builder rejected data")
	if err := lease.VisitCandidate(context.Background(), func(Block, io.Reader) error { return failure }); err != failure {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lease.VisitCandidate(ctx, func(Block, io.Reader) error { t.Fatal("cancelled stream consumed data"); return nil }); err != context.Canceled {
		t.Fatal(err)
	}
	// Validated candidates may be discarded, but a failed stream must never
	// publish them. Original Visit still rejects before invoking its consumer.
	if _, err := lease.data.WriteAt([]byte("X"), blockHeaderBytes); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := lease.Visit(context.Background(), func(Block, io.Reader) error { called = true; return nil }); !errors.Is(err, ErrCorrupt) || called {
		t.Fatal("original verified-first contract changed", called, err)
	}
	candidate.Reset()
	err = lease.VisitCandidate(context.Background(), func(_ Block, source io.Reader) error { _, err := io.Copy(&candidate, source); return err })
	if !errors.Is(err, ErrCorrupt) || candidate.Len() == 0 {
		t.Fatal("candidate integrity failure was hidden", candidate.Len(), err)
	}
}
