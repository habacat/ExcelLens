package checkpoint

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"

	"excellens/internal/formats"
)

func (m Manifest) dataName() string {
	if m.AlternateBlocks {
		return "blocks.alt"
	}
	return "blocks"
}

// ReplaceEditJournal checkpoints the current logical revision in a separate
// data file. An interrupted publication leaves the old manifest and its data
// readable. The manifest switch, never a truncate of the active file, commits.
func (l *Lease) ReplaceEditJournal(ctx context.Context, revision uint64, payload []byte) (Manifest, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(); err != nil {
		return Manifest{}, err
	}
	old := l.manifest
	if old.Definition.Kind != "edit" || old.Complete || revision != old.Revision || l.backup {
		return Manifest{}, ErrConflict
	}
	if len(payload) == 0 || int64(len(payload)) > formats.CheckpointBlockBytes {
		return Manifest{}, ErrCapacity
	}
	if err := l.currentFiles(); err != nil {
		return Manifest{}, err
	}
	next := cloneManifest(old)
	next.AlternateBlocks = !old.AlternateBlocks
	next.CompactedAt = revision
	next.Records, next.Blocks = 1, 1
	next.DataBytes = int64(len(payload)) + blockHeaderBytes
	sum := sha256.Sum256(payload)
	header := encodeBlock(Block{Sequence: 1, Records: 1, Bytes: int64(len(payload))}, sum[:])
	next.ChainHash = nextChain(initialChain(old.ID, old.DefinitionHash), header)
	gate, err := l.store.lock(".store.lock")
	if err != nil {
		return Manifest{}, err
	}
	defer releaseLock(gate)
	other, err := l.store.regular(filepath.Join(old.ID, next.dataName()), os.O_CREATE|os.O_RDWR)
	if err != nil {
		return Manifest{}, err
	}
	defer func() {
		if other != nil {
			other.Close()
		}
	}()
	_, used, err := l.store.usage()
	if err != nil {
		return Manifest{}, err
	}
	stat, err := other.Stat()
	if err != nil {
		return Manifest{}, err
	}
	if used-stat.Size()+next.DataBytes > formats.CheckpointBytes {
		return Manifest{}, ErrCapacity
	}
	if err = ctx.Err(); err != nil {
		return Manifest{}, err
	}
	// The old slot is the independently valid fallback throughout publication.
	prior, err := encodeManifest(old)
	if err != nil {
		return Manifest{}, err
	}
	if err = l.store.atomicFile(old.ID, "manifest.bak", prior); err != nil {
		return Manifest{}, err
	}
	if err = other.Truncate(next.DataBytes); err == nil {
		_, err = other.WriteAt(header, 0)
	}
	if err == nil {
		_, err = other.WriteAt(payload, blockHeaderBytes)
	}
	if err == nil {
		err = other.Sync()
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return Manifest{}, err
	}
	if err = l.store.writeManifest(next, nil); err != nil {
		l.poisoned = true
		return Manifest{}, errors.Join(ErrUncertain, err)
	}
	previous := l.data
	l.data = other
	l.syncData = other.Sync
	other = nil
	l.manifest = next
	// Keep both slots if the backup could not be advanced to the same snapshot.
	stamped, err := encodeManifest(next)
	if err == nil && l.store.atomicFile(old.ID, "manifest.bak", stamped) == nil {
		_ = previous.Truncate(0)
		_ = previous.Sync()
	}
	_ = previous.Close()
	return cloneManifest(next), nil
}
