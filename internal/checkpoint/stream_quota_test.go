package checkpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"excellens/internal/formats"
)

func TestCheckpointStreamSharesInflightQuotaAndKeepsAdmissionAvailable(t *testing.T) {
	s, def, _ := fixture(t)
	a, b, reserve := create(t, s, def), create(t, s, def), create(t, s, def)
	if err := markSparse(reserve.data); err != nil {
		t.Skipf("filesystem cannot create the logical sparse quota fixture: %v", err)
	}
	metadata := 3 * int64(formats.CheckpointManifestBytes) * 3
	if err := reserve.data.Truncate(formats.CheckpointBytes - metadata - 2*formats.CheckpointStreamReserve + (128 << 10)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	done := make(chan error, 1)
	revision := a.Snapshot().Revision
	go func() {
		_, err := a.AdvanceStream(context.Background(), revision, func(out io.Writer) (Advance, error) {
			next := Advance{Revision: revision, Stage: "part", Cursor: []byte(`{}`), Records: 1}
			if _, err := out.Write(bytes.Repeat([]byte("a"), 512<<10)); err != nil {
				return next, err
			}
			close(entered)
			<-release
			_, err := out.Write(bytes.Repeat([]byte("b"), 512<<10))
			return next, err
		})
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("stream failed before reserving its in-flight bytes", err)
	case <-time.After(5 * time.Second):
		t.Fatal("stream reservation blocked")
	}
	entries, err := s.List()
	if err != nil || len(entries) != 3 {
		t.Fatal("a producer held the admission lock while blocked", entries, err)
	}
	before := b.Snapshot()
	if _, err := b.AdvanceStream(context.Background(), before.Revision, func(out io.Writer) (Advance, error) {
		_, _ = out.Write(bytes.Repeat([]byte("x"), 1<<20)) // ignored capacity error must remain sticky
		return Advance{Revision: before.Revision, Stage: "part", Cursor: []byte(`{}`), Records: 1}, nil
	}); !errors.Is(err, ErrCapacity) {
		t.Fatal("another producer bypassed shared in-flight quota", err)
	}
	info, _ := b.data.Stat()
	if info.Size() != before.DataBytes || b.Snapshot().Revision != before.Revision {
		t.Fatal("failed reservation retained disk budget or acknowledgement")
	}
	releaseOnce()
	if err := <-done; err != nil {
		t.Fatal("the admitted producer lost its reserved space", err)
	}
	// Release only the synthetic sparse tail after verifying its empty prefix.
	m := reserve.Snapshot()
	_ = reserve.Close()
	trimmed, err := s.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	_ = trimmed.Close()
	if _, err := b.AdvanceStream(context.Background(), before.Revision, func(out io.Writer) (Advance, error) {
		_, err := out.Write(bytes.Repeat([]byte("x"), 1<<20))
		return Advance{Revision: before.Revision, Stage: "part", Cursor: []byte(`{}`), Records: 1}, err
	}); err != nil {
		t.Fatal("released quota was not returned to other producers", err)
	}
}
