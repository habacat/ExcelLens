package checkpoint

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"excellens/internal/formats"
)

// Lease owns one OS-backed writer lock, not a process-local boolean or PID
// guess. Close/abnormal process exit releases it without deleting the task.
type Lease struct {
	mu       sync.Mutex
	store    *Store
	lock     *os.File
	data     *os.File
	manifest Manifest
	backup   bool
	closed   bool
	poisoned bool
	syncData func() error
}

func newLease(s *Store, lock, data *os.File, manifest Manifest) *Lease {
	return &Lease{store: s, lock: lock, data: data, manifest: manifest, syncData: data.Sync}
}

func (l *Lease) Snapshot() Manifest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return cloneManifest(l.manifest)
}

func (l *Lease) FromBackup() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.backup
}

func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := errors.Join(l.data.Close(), releaseLock(l.lock))
	l.store.active.Add(-1)
	return err
}

func (l *Lease) usable() error {
	if l.closed {
		return ErrClosed
	}
	if l.poisoned {
		return ErrUncertain
	}
	return nil
}

// Advance writes only the supplied complete records/stage bytes. Neither a
// cursor nor a completion flag is acknowledged before payload Sync and atomic
// metadata installation. A failed/ambiguous installation requires reacquiring
// the lease; never blindly retry a possibly committed append.
func (l *Lease) Advance(ctx context.Context, next Advance, payload io.Reader) (Manifest, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return Manifest{}, err
	}
	current := l.manifest
	if current.Complete || next.Revision != current.Revision || current.Revision == math.MaxUint64 {
		return Manifest{}, ErrConflict
	}
	if !validName(next.Stage) || next.Records < 0 || next.Records > math.MaxInt64-current.Records || next.Bytes < 0 || next.Bytes > formats.CheckpointBlockBytes || next.Bytes == 0 && next.Records != 0 || next.Bytes > 0 && payload == nil {
		return Manifest{}, ErrBinding
	}
	cursor, err := compactJSON(next.Cursor, formats.CheckpointCursorBytes)
	if err != nil {
		return Manifest{}, ErrBinding
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	if err := l.currentFiles(); err != nil {
		return Manifest{}, err
	}
	candidate := cloneManifest(current)
	candidate.Revision++
	candidate.Stage, candidate.Cursor, candidate.Complete = next.Stage, cursor, next.Complete
	candidate.Records += next.Records
	candidate.Updated = time.Now().UTC()
	if candidate.Updated.Before(current.Updated) {
		candidate.Updated = current.Updated
	}
	if next.Bytes > 0 {
		if next.Bytes+blockHeaderBytes > formats.CheckpointBytes-current.DataBytes {
			return Manifest{}, ErrCapacity
		}
		candidate.Blocks++
		candidate.DataBytes += blockHeaderBytes + next.Bytes
		// Reserve file length under the cross-process admission lock. The
		// potentially slow reader then runs without monopolizing that lock.
		gate, err := l.store.lock(".store.lock")
		if err != nil {
			return Manifest{}, err
		}
		_, used, err := l.store.usage()
		if err == nil && used > formats.CheckpointBytes-(candidate.DataBytes-current.DataBytes) {
			err = ErrCapacity
		}
		if err == nil {
			info, statErr := l.data.Stat()
			err = statErr
			if err == nil && info.Size() != current.DataBytes {
				err = ErrCorrupt
			}
		}
		if err == nil {
			err = l.data.Truncate(candidate.DataBytes)
		}
		if releaseErr := releaseLock(gate); err == nil {
			err = releaseErr
		}
		if err != nil {
			return Manifest{}, err
		}
		rollback := func(cause error) (Manifest, error) {
			trimErr := l.data.Truncate(current.DataBytes)
			if trimErr == nil {
				trimErr = l.syncData()
			}
			if trimErr != nil {
				l.poisoned = true
				return Manifest{}, errors.Join(cause, ErrUncertain, trimErr)
			}
			return Manifest{}, cause
		}
		if _, err := l.data.Seek(current.DataBytes+blockHeaderBytes, io.SeekStart); err != nil {
			return rollback(err)
		}
		digest := sha256.New()
		reader := &io.LimitedReader{R: contextReader{ctx: ctx, source: payload}, N: next.Bytes}
		written, err := io.CopyBuffer(io.MultiWriter(l.data, digest), reader, make([]byte, formats.CheckpointIOBytes))
		if err == nil && written != next.Bytes {
			err = io.ErrUnexpectedEOF
		}
		if err == nil {
			err = ctx.Err()
		}
		if err != nil {
			return rollback(err)
		}
		header := encodeBlock(Block{Sequence: candidate.Blocks, Records: next.Records, Bytes: next.Bytes}, digest.Sum(nil))
		if _, err := l.data.WriteAt(header, current.DataBytes); err != nil {
			return rollback(err)
		}
		if err := l.syncData(); err != nil {
			return rollback(err)
		}
		if err := ctx.Err(); err != nil {
			return rollback(err)
		}
		candidate.ChainHash = nextChain(current.ChainHash, header)
	}
	if next.Bytes == 0 {
		if err := ctx.Err(); err != nil {
			return Manifest{}, err
		}
	}
	if err := l.currentFiles(); err != nil {
		l.poisoned = true
		return Manifest{}, err
	}
	if err := l.store.writeManifest(candidate, &current); err != nil {
		// Main-slot rename may have succeeded before a directory Sync failed.
		// Keep both old/new data prefixes; truncating here could corrupt the
		// new valid manifest. Acquire validates whichever slot is installed.
		l.poisoned = true
		return Manifest{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	l.manifest, l.backup = candidate, false
	return cloneManifest(candidate), nil
}

func (l *Lease) currentFiles() error {
	info, err := l.data.Stat()
	if err != nil {
		return err
	}
	onDisk, err := l.store.root.Lstat(filepath.Join(l.manifest.ID, l.manifest.dataName()))
	if err != nil || !onDisk.Mode().IsRegular() || !os.SameFile(info, onDisk) {
		return ErrCorrupt
	}
	m, _, err := l.store.load(l.manifest.ID)
	if err != nil {
		return err
	}
	actual, err := encodeManifest(m)
	if err != nil {
		return err
	}
	expected, err := encodeManifest(l.manifest)
	if err != nil || !bytes.Equal(actual, expected) {
		return ErrConflict
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(data)
}

func (l *Lease) Verify(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	return l.verify(ctx)
}

func (l *Lease) verify(ctx context.Context) error { return l.walk(ctx, nil) }

// Visit verifies each block before streaming it to the consumer and checks the
// streamed bytes a second time. The consumer builds private candidates only;
// it must not publish them until Visit and the source driver's checks succeed.
func (l *Lease) Visit(ctx context.Context, consume func(Block, io.Reader) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if consume == nil {
		return ErrBinding
	}
	return l.walk(ctx, consume)
}

// VisitCandidate validates while streaming each block once. Consumers MUST
// retain private, bounded candidates and publish nothing until this method
// succeeds. Unlike Visit, a corrupt block may reach the private consumer before
// its digest is rejected. This avoids rereading large durable position streams.
func (l *Lease) VisitCandidate(ctx context.Context, consume func(Block, io.Reader) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return err
	}
	if consume == nil {
		return ErrBinding
	}
	return l.walkBlocks(ctx, consume, false)
}

func (l *Lease) walk(ctx context.Context, consume func(Block, io.Reader) error) error {
	return l.walkBlocks(ctx, consume, true)
}

func (l *Lease) walkBlocks(ctx context.Context, consume func(Block, io.Reader) error, verifyFirst bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m := l.manifest
	info, err := l.data.Stat()
	if err != nil {
		return err
	}
	if info.Size() < m.DataBytes || info.Size() > formats.CheckpointBytes {
		return ErrCorrupt
	}
	var offset, records int64
	var sequence uint64
	chain := initialChain(m.ID, m.DefinitionHash)
	buffer := make([]byte, formats.CheckpointIOBytes)
	var header [blockHeaderBytes]byte
	for offset < m.DataBytes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.DataBytes-offset < blockHeaderBytes {
			return ErrCorrupt
		}
		if _, err := l.data.ReadAt(header[:], offset); err != nil {
			return ErrCorrupt
		}
		count, size := binary.LittleEndian.Uint64(header[16:24]), binary.LittleEndian.Uint64(header[24:32])
		if string(header[:8]) != blockMagic || binary.LittleEndian.Uint64(header[8:16]) != sequence+1 || count > uint64(math.MaxInt64-records) || size == 0 || size > uint64(formats.CheckpointBlockBytes) || size > uint64(m.DataBytes-offset-blockHeaderBytes) {
			return ErrCorrupt
		}
		block := Block{Sequence: sequence + 1, Records: int64(count), Bytes: int64(size)}
		if verifyFirst || consume == nil {
			digest := sha256.New()
			reader := contextReader{ctx: ctx, source: io.NewSectionReader(l.data, offset+blockHeaderBytes, block.Bytes)}
			copied, err := io.CopyBuffer(digest, reader, buffer)
			if err != nil {
				return err
			}
			if copied != block.Bytes || !bytes.Equal(header[32:64], digest.Sum(nil)) {
				return ErrCorrupt
			}
		}
		if consume != nil {
			confirmed := sha256.New()
			stream := &io.LimitedReader{R: contextReader{ctx: ctx, source: io.NewSectionReader(l.data, offset+blockHeaderBytes, block.Bytes)}, N: block.Bytes}
			if err := consume(block, io.TeeReader(stream, confirmed)); err != nil {
				return err
			}
			if stream.N != 0 || !bytes.Equal(header[32:64], confirmed.Sum(nil)) {
				return ErrCorrupt
			}
		}
		chain = nextChain(chain, header[:])
		offset += blockHeaderBytes + block.Bytes
		records += block.Records
		sequence++
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if offset != m.DataBytes || sequence != m.Blocks || records != m.Records || chain != m.ChainHash {
		return ErrCorrupt
	}
	return nil
}
