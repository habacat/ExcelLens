package checkpoint

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"time"

	"excellens/internal/formats"
)

// AdvanceStream commits one complete driver record/part without buffering it.
// A part may span blocks; only the last block carries its record count, and
// the old manifest remains authoritative until every block and cursor Sync.
// The callback must not call lease methods. Bytes is computed here (return 0
// in Advance.Bytes). Never replay the callback after an uncertain commit.
func (l *Lease) AdvanceStream(ctx context.Context, revision uint64, produce func(io.Writer) (Advance, error)) (Manifest, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return Manifest{}, err
	}
	current := l.manifest
	if current.Complete || current.Revision != revision || revision == math.MaxUint64 {
		return Manifest{}, ErrConflict
	}
	if produce == nil {
		return Manifest{}, ErrBinding
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if err := l.currentFiles(); err != nil {
		return Manifest{}, err
	}
	w := &streamTransaction{lease: l, ctx: ctx, offset: current.DataBytes, reserved: current.DataBytes, sequence: current.Blocks, chain: current.ChainHash}
	rollback := func(cause error) (Manifest, error) {
		if w.reserved != current.DataBytes {
			trimErr := l.data.Truncate(current.DataBytes)
			if trimErr == nil {
				trimErr = l.syncData()
			}
			if trimErr != nil {
				l.poisoned = true
				return Manifest{}, errors.Join(cause, ErrUncertain, trimErr)
			}
		}
		return Manifest{}, cause
	}
	// Also roll back a panicking producer before its caller unwinds. It cannot
	// leave a reserved tail that blocks otherwise independent task admission.
	settled := false
	defer func() {
		if !settled {
			_, _ = rollback(ErrUncertain)
		}
	}()
	next, err := produce(w)
	if err == nil {
		err = w.err
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (next.Revision != revision || !validName(next.Stage) || next.Bytes != 0 || next.Records < 0 || next.Records > math.MaxInt64-current.Records || w.digest == nil && next.Records != 0) {
		err = ErrBinding
	}
	cursor, cursorErr := compactJSON(next.Cursor, formats.CheckpointCursorBytes)
	if err == nil && cursorErr != nil {
		err = ErrBinding
	}
	if err == nil {
		err = w.finishBlock(next.Records)
	}
	if err == nil && w.reserved != w.offset {
		err = l.data.Truncate(w.offset)
	}
	if err == nil && w.offset != current.DataBytes {
		err = l.syncData()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		settled = true
		return rollback(err)
	}
	if err := l.currentFiles(); err != nil {
		l.poisoned = true
		settled = true // preserve evidence if directory/manifest identity changed
		return Manifest{}, err
	}
	candidate := cloneManifest(current)
	candidate.Revision++
	candidate.Stage, candidate.Cursor, candidate.Complete = next.Stage, cursor, next.Complete
	candidate.Records += next.Records
	candidate.Blocks, candidate.DataBytes, candidate.ChainHash = w.sequence, w.offset, w.chain
	candidate.Updated = time.Now().UTC()
	if candidate.Updated.Before(current.Updated) {
		candidate.Updated = current.Updated
	}
	settled = true
	if err := l.store.writeManifest(candidate, &current); err != nil {
		l.poisoned = true
		return Manifest{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	l.manifest, l.backup = candidate, false
	return cloneManifest(candidate), nil
}

type streamTransaction struct {
	lease    *Lease
	ctx      context.Context
	offset   int64 // start of the current unfinalized block, or next block
	reserved int64
	size     int64
	sequence uint64
	chain    string
	digest   hash.Hash
	err      error
}

func (w *streamTransaction) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	written := 0
	for len(data) > 0 {
		if w.err = w.ctx.Err(); w.err != nil {
			break
		}
		if w.size == formats.CheckpointBlockBytes {
			if w.err = w.finishBlock(0); w.err != nil {
				break
			}
		}
		count := int(min(int64(len(data)), formats.CheckpointBlockBytes-w.size, int64(formats.CheckpointIOBytes)))
		end := w.offset + blockHeaderBytes + w.size + int64(count)
		if w.err = w.reserve(end); w.err != nil {
			break
		}
		if w.digest == nil {
			w.digest = sha256.New()
		}
		n, err := w.lease.data.WriteAt(data[:count], w.offset+blockHeaderBytes+w.size)
		_, _ = w.digest.Write(data[:n])
		w.size += int64(n)
		written += n
		data = data[n:]
		if err == nil && n != count {
			err = io.ErrShortWrite
		}
		if err != nil {
			w.err = err
			break
		}
	}
	return written, w.err
}

func (w *streamTransaction) finishBlock(records int64) error {
	if w.digest == nil {
		return nil
	}
	header := encodeBlock(Block{Sequence: w.sequence + 1, Records: records, Bytes: w.size}, w.digest.Sum(nil))
	if _, err := w.lease.data.WriteAt(header, w.offset); err != nil {
		return err
	}
	w.chain = nextChain(w.chain, header)
	w.sequence++
	w.offset += blockHeaderBytes + w.size
	w.digest, w.size = nil, 0
	return nil
}

// Only this admission step retries a busy directory lock, before changing its
// reservation. Source production and uncertain writes are never replayed.
func (w *streamTransaction) reserve(end int64) error {
	if end <= w.reserved {
		return nil
	}
	if end > formats.CheckpointBytes {
		return ErrCapacity
	}
	deadline := time.Now().Add(time.Second)
	for {
		if err := w.ctx.Err(); err != nil {
			return err
		}
		gate, err := w.lease.store.lock(".store.lock")
		if errors.Is(err, ErrBusy) && time.Now().Before(deadline) {
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-w.ctx.Done():
				timer.Stop()
				return w.ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			return err
		}
		_, used, err := w.lease.store.usage()
		next := w.reserved
		if err == nil {
			growth := min((end-w.reserved+formats.CheckpointStreamReserve-1)/formats.CheckpointStreamReserve*formats.CheckpointStreamReserve, formats.CheckpointBytes-used, formats.CheckpointBytes-w.reserved)
			if growth < end-w.reserved {
				err = ErrCapacity
			} else {
				next += growth
			}
		}
		if err == nil {
			info, statErr := w.lease.data.Stat()
			err = statErr
			if err == nil && info.Size() != w.reserved {
				err = ErrCorrupt
			}
		}
		if err == nil {
			err = w.lease.data.Truncate(next)
			if err == nil {
				w.reserved = next
			}
		}
		return errors.Join(err, releaseLock(gate))
	}
}
