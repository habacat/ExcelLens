package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"excellens/internal/formats"
)

type repeatedRecords struct {
	pattern   []byte
	remaining int64
	offset    int
	maxRead   int
}

func (r *repeatedRecords) Read(data []byte) (int, error) {
	r.maxRead = max(r.maxRead, len(data))
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := int(min(int64(len(data)), r.remaining))
	for done := 0; done < count; {
		n := copy(data[done:count], r.pattern[r.offset:])
		done += n
		r.offset = (r.offset + n) % len(r.pattern)
	}
	r.remaining -= int64(count)
	return count, nil
}

type heldInput struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	reader  io.Reader
}

func (r *heldInput) Read(data []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.reader.Read(data)
}

func TestCheckpointInflightLengthReservationIsSharedWithoutHoldingAdmissionDuringCopy(t *testing.T) {
	s, def, _ := fixture(t)
	a, b, reserved := create(t, s, def), create(t, s, def), create(t, s, def)
	if err := markSparse(reserved.data); err != nil {
		t.Skipf("filesystem cannot create a sparse quota reservation: %v", err)
	}
	// This is a logical-size quota test, not 8 GiB of physically written data.
	metadataReserve := 3 * int64(formats.CheckpointManifestBytes) * 3
	if err := reserved.data.Truncate(formats.CheckpointBytes - metadataReserve - 3*(1<<20)); err != nil {
		t.Fatal(err)
	}
	length := int64(2 << 20)
	input := &heldInput{entered: make(chan struct{}), release: make(chan struct{}), reader: &repeatedRecords{pattern: []byte("x"), remaining: length}}
	release := sync.OnceFunc(func() { close(input.release) })
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() {
		_, err := a.Advance(context.Background(), Advance{Revision: a.Snapshot().Revision, Stage: "scan", Cursor: json.RawMessage(`{"next":1}`), Records: 1, Bytes: length}, input)
		done <- err
	}()
	select {
	case <-input.entered:
	case err := <-done:
		t.Fatal("first reservation never entered its payload", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first reservation blocked")
	}
	other, err := Open(s.root.Name())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	entries, err := other.List()
	if err != nil || len(entries) != 3 {
		t.Fatal("blocked payload held the admission/catalogue lock", entries, err)
	}
	if _, err := b.Advance(context.Background(), Advance{Revision: b.Snapshot().Revision, Stage: "scan", Cursor: json.RawMessage(`{}`), Bytes: length, Records: 1}, &repeatedRecords{pattern: []byte("y"), remaining: length}); !errors.Is(err, ErrCapacity) {
		t.Fatal("another task bypassed the reserved in-flight bytes", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	m := reserved.Snapshot()
	_ = reserved.Close()
	// Zero confirmed bytes are verified before releasing the artificial tail.
	trimmed, err := other.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	_ = trimmed.Close()
	if _, err := b.Advance(context.Background(), Advance{Revision: b.Snapshot().Revision, Stage: "scan", Cursor: json.RawMessage(`{}`), Bytes: length, Records: 1}, &repeatedRecords{pattern: []byte("y"), remaining: length}); err != nil {
		t.Fatal("verified tail release did not return the shared quota", err)
	}
}

func streamRoundTrip(t *testing.T, records int64, blocks int) {
	t.Helper()
	s, def, _ := fixture(t)
	l := create(t, s, def)
	line := []byte("001,9007199254740993.31,\"多\n行\",=2+2\r\n")
	length := records * int64(len(line))
	largest := 0
	for n := range blocks {
		reader := &repeatedRecords{pattern: line, remaining: length}
		if _, err := l.Advance(context.Background(), Advance{Revision: l.Snapshot().Revision, Stage: "scan", Cursor: json.RawMessage(fmt.Sprintf(`{"block":%d}`, n+1)), Records: records, Bytes: length}, reader); err != nil {
			t.Fatal(err)
		}
		largest = max(largest, reader.maxRead)
	}
	m := l.Snapshot()
	if m.Records != records*int64(blocks) || largest > formats.CheckpointIOBytes {
		t.Fatal("stream buffered the entire payload or lost exact record count", m, largest)
	}
	_ = l.Close()
	root := s.root.Name()
	_ = s.Close()
	other, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	resumed, err := other.Acquire(context.Background(), m.ID, m.DefinitionHash)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	actual := sha256.New()
	if err := resumed.Visit(context.Background(), func(block Block, reader io.Reader) error {
		if block.Bytes != length || block.Records != records {
			return errors.New("block boundary or exact record count changed")
		}
		_, err := io.CopyBuffer(actual, reader, make([]byte, formats.CheckpointIOBytes))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	expected := sha256.New()
	_, _ = io.CopyBuffer(expected, &repeatedRecords{pattern: line, remaining: length * int64(blocks)}, make([]byte, formats.CheckpointIOBytes))
	if !bytes.Equal(actual.Sum(nil), expected.Sum(nil)) {
		t.Fatal("independent streamed round trip changed exact bytes")
	}
	info, _ := other.root.Stat(filepath.Join(m.ID, "blocks"))
	if info.Size() != length*int64(blocks)+int64(blocks*blockHeaderBytes) {
		t.Fatal("unexpected payload/file expansion", info.Size())
	}
	t.Logf("checkpoint records=%d payloadBytes=%d blocks=%d largestInputRead=%d sourceQueryOrExportResume=false", records*int64(blocks), length*int64(blocks), blocks, largest)
}

func TestCheckpointBoundedStreamingSurvivesStoreReopen(t *testing.T) { streamRoundTrip(t, 16384, 2) }

func TestCheckpointActualThreeMillionRecordPayloadRoundTrip(t *testing.T) {
	if os.Getenv("EXCELLENS_CHECKPOINT_SCALE") != "1" {
		t.Skip("set EXCELLENS_CHECKPOINT_SCALE=1 for the separate real-byte scale gate")
	}
	streamRoundTrip(t, 1000000, 3)
}
