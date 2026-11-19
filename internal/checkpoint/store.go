package checkpoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"excellens/internal/formats"
)

type syncedWriter interface {
	io.WriteCloser
	Sync() error
}

const taskFileReserve = 5 // lease, data, two manifest slots and one install file

type Store struct {
	mu     sync.RWMutex
	root   *os.Root
	active atomic.Int64
	// Only persistence boundaries are injectable for deterministic IO failures.
	createTemporary func(string) (syncedWriter, error)
	rename          func(string, string) error
	syncDirectory   func(string) error
}

func Open(directory string) (*Store, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrCorrupt
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root}
	s.rename = root.Rename
	s.syncDirectory = func(name string) error { return syncDirectory(root, name) }
	s.createTemporary = func(name string) (syncedWriter, error) {
		return root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	if s.active.Load() != 0 {
		return ErrBusy
	}
	err := s.root.Close()
	s.root = nil
	return err
}

func newID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func (s *Store) regular(name string, flags int) (*os.File, error) {
	if info, err := s.root.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return nil, ErrCorrupt
		}
	} else if !errors.Is(err, os.ErrNotExist) || flags&os.O_CREATE == 0 {
		return nil, err
	}
	return s.root.OpenFile(name, flags, 0600)
}

func (s *Store) lock(name string) (*os.File, error) {
	f, err := s.regular(name, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func releaseLock(f *os.File) error { return errors.Join(unlockFile(f), f.Close()) }

func (s *Store) directory(id string) error {
	if !validID(id) {
		return ErrBinding
	}
	info, err := s.root.Lstat(id)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrCorrupt
	}
	return nil
}

func (s *Store) directories() ([]string, error) {
	dir, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	var ids []string
	seen := 0
	for {
		entries, err := dir.ReadDir(64)
		for _, entry := range entries {
			seen++
			if seen > formats.CheckpointFiles {
				return nil, ErrCapacity
			}
			if !validID(entry.Name()) {
				continue
			}
			if err := s.directory(entry.Name()); err != nil {
				return nil, err
			}
			ids = append(ids, entry.Name())
			if len(ids) > formats.CheckpointTasks {
				return nil, ErrCapacity
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return nil, err
			}
			break
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Called with the global admission lock. Data size is reserved by Truncate
// before releasing that lock; readers need not wait on a blocked payload copy.
func (s *Store) usage() (tasks int, used int64, err error) {
	ids, err := s.directories()
	if err != nil {
		return 0, 0, err
	}
	files := 0
	for _, id := range ids {
		dir, err := s.root.Open(id)
		if err != nil {
			return 0, 0, err
		}
		var data, metadata int64
		taskFiles := 0
		for {
			entries, readErr := dir.ReadDir(64)
			for _, entry := range entries {
				files++
				taskFiles++
				if files > formats.CheckpointFiles {
					_ = dir.Close()
					return 0, 0, ErrCapacity
				}
				info, err := s.usageEntry(id, entry)
				if err != nil {
					_ = dir.Close()
					return 0, 0, err
				}
				if info == nil {
					continue
				}
				if entry.Name() == "blocks" {
					data = info.Size()
				} else {
					metadata += info.Size()
				}
				if metadata > formats.CheckpointBytes {
					_ = dir.Close()
					return 0, 0, ErrCapacity
				}
			}
			if readErr != nil {
				_ = dir.Close()
				if !errors.Is(readErr, io.EOF) {
					return 0, 0, readErr
				}
				break
			}
		}
		files += max(0, taskFileReserve-taskFiles)
		if files > formats.CheckpointFiles-taskFileReserve {
			return 0, 0, ErrCapacity
		}
		used += data + max(metadata, 3*int64(formats.CheckpointManifestBytes))
		if used > formats.CheckpointBytes {
			return 0, 0, ErrCapacity
		}
	}
	return len(ids), used, nil
}

// Manifest installation does not hold directory admission during Sync. A
// ReadDir snapshot may therefore include an owned installation file already
// atomically renamed by another task when Lstat reaches it. Its space is still
// covered by the per-task three-manifest reserve. Only this exact transient
// name may disappear; missing stable files, unknown names and links still fail.
func (s *Store) usageEntry(id string, entry os.DirEntry) (os.FileInfo, error) {
	info, err := s.root.Lstat(filepath.Join(id, entry.Name()))
	if errors.Is(err, os.ErrNotExist) && stateTemporary(entry.Name()) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > formats.CheckpointBytes {
		return nil, ErrCorrupt
	}
	return info, nil
}

func (s *Store) atomicFile(id, name string, data []byte) error {
	key, err := newID()
	if err != nil {
		return err
	}
	temporary := filepath.Join(id, ".state-"+key+".tmp")
	f, err := s.createTemporary(temporary)
	if err != nil {
		return err
	}
	defer s.root.Remove(temporary)
	_, err = io.Copy(f, strings.NewReader(string(data)))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := s.rename(temporary, filepath.Join(id, name)); err != nil {
		return err
	}
	return s.syncDirectory(id)
}

func (s *Store) writeManifest(next Manifest, previous *Manifest) error {
	data, err := encodeManifest(next)
	if err != nil {
		return err
	}
	if previous != nil {
		backup, err := encodeManifest(*previous)
		if err != nil {
			return err
		}
		if err := s.atomicFile(next.ID, "manifest.bak", backup); err != nil {
			return err
		}
	}
	return s.atomicFile(next.ID, "manifest", data)
}

func (s *Store) load(id string) (Manifest, bool, error) {
	f, err := s.regular(filepath.Join(id, "manifest"), os.O_RDONLY)
	backup := false
	if errors.Is(err, os.ErrNotExist) {
		f, err = s.regular(filepath.Join(id, "manifest.bak"), os.O_RDONLY)
		backup = err == nil
	}
	if err != nil {
		return Manifest{}, false, err
	}
	defer f.Close()
	m, err := decodeManifest(f)
	if err == nil && m.ID != id {
		err = ErrCorrupt
	}
	return m, backup, err
}

func (s *Store) Create(ctx context.Context, definition Definition, stage string, cursor json.RawMessage) (*Lease, error) {
	def, binding, err := prepareDefinition(definition)
	if err != nil {
		return nil, err
	}
	cursor, err = compactJSON(cursor, formats.CheckpointCursorBytes)
	if err != nil || !validName(stage) {
		return nil, ErrBinding
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gate, err := s.lock(".store.lock")
	if err != nil {
		return nil, err
	}
	defer releaseLock(gate)
	tasks, used, err := s.usage()
	if err != nil {
		return nil, err
	}
	if tasks >= formats.CheckpointTasks || used > formats.CheckpointBytes-3*int64(formats.CheckpointManifestBytes) {
		return nil, ErrCapacity
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := newID()
	if err != nil {
		return nil, err
	}
	if err := s.root.Mkdir(id, 0700); err != nil {
		return nil, err
	}
	// Failed initialization is kept as an unavailable entry, never erased as
	// if a source-backed task had not existed; explicit Discard can remove it.
	lock, err := s.lock(filepath.Join(id, ".lease"))
	if err != nil {
		return nil, err
	}
	data, err := s.regular(filepath.Join(id, "blocks"), os.O_CREATE|os.O_EXCL|os.O_RDWR)
	if err != nil {
		_ = releaseLock(lock)
		return nil, err
	}
	now := time.Now().UTC()
	m := Manifest{Version: Schema, ID: id, Revision: 1, Definition: def, DefinitionHash: binding, Created: now, Updated: now, Stage: stage, Cursor: cursor, ChainHash: initialChain(id, binding)}
	err = data.Sync()
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = s.writeManifest(m, nil)
	}
	if err == nil {
		err = s.syncDirectory(".")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		_ = data.Close()
		_ = releaseLock(lock)
		return nil, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	s.active.Add(1)
	return newLease(s, lock, data, m), nil
}

func (s *Store) Acquire(ctx context.Context, id, binding string) (*Lease, error) {
	return s.acquire(ctx, id, binding, true)
}

// AcquireCurrent refuses a damaged primary manifest before opening or trimming
// its blocks. Confirmed document edits cannot be rolled back to a backup slot.
func (s *Store) AcquireCurrent(ctx context.Context, id, binding string) (*Lease, error) {
	return s.acquire(ctx, id, binding, false)
}

func (s *Store) acquire(ctx context.Context, id, binding string, allowBackup bool) (*Lease, error) {
	if !validHash(binding) {
		return nil, ErrBinding
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	gate, err := s.lock(".store.lock")
	if err != nil {
		return nil, err
	}
	if err := s.directory(id); err != nil {
		_ = releaseLock(gate)
		return nil, err
	}
	lock, err := s.lock(filepath.Join(id, ".lease"))
	_ = releaseLock(gate)
	if err != nil {
		return nil, err
	}
	m, backup, err := s.load(id)
	if err == nil && backup && !allowBackup {
		err = ErrCorrupt
	}
	if err == nil && m.DefinitionHash != binding {
		err = ErrBinding
	}
	var data *os.File
	if err == nil {
		data, err = s.regular(filepath.Join(id, "blocks"), os.O_RDWR)
	}
	if err != nil {
		_ = releaseLock(lock)
		return nil, err
	}
	s.active.Add(1)
	lease := newLease(s, lock, data, m)
	lease.backup = backup
	if err := lease.verify(ctx); err != nil {
		_ = lease.Close()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = lease.Close()
		return nil, err
	}
	info, err := data.Stat()
	if err == nil && info.Size() != m.DataBytes {
		err = data.Truncate(m.DataBytes)
		if err == nil {
			err = data.Sync()
		}
	}
	if err != nil {
		_ = lease.Close()
		return nil, err
	}
	return lease, nil
}

// List reads bounded metadata only. A last-confirmed checkpoint is shown even
// while its writer is appending; listing never hashes a source-sized payload.
func (s *Store) List() ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return nil, ErrClosed
	}
	gate, err := s.lock(".store.lock")
	if err != nil {
		return nil, err
	}
	defer releaseLock(gate)
	ids, err := s.directories()
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(ids))
	for _, id := range ids {
		entry := Entry{ID: id}
		lock, err := s.lock(filepath.Join(id, ".lease"))
		if errors.Is(err, ErrBusy) {
			entry.Busy = true
		} else if err != nil {
			entry.Error = err.Error()
		} else {
			_ = releaseLock(lock)
		}
		m, backup, err := s.load(id)
		if err != nil {
			entry.Error = err.Error()
		} else {
			entry.Manifest, entry.Backup = &m, backup
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func managedFile(name string) bool {
	if name == ".lease" || name == "blocks" || name == "manifest" || name == "manifest.bak" {
		return true
	}
	return stateTemporary(name)
}

func stateTemporary(name string) bool {
	return strings.HasPrefix(name, ".state-") && strings.HasSuffix(name, ".tmp") && validID(strings.TrimSuffix(strings.TrimPrefix(name, ".state-"), ".tmp"))
}

func (s *Store) Discard(id string) error {
	return s.discard(id, nil)
}

// DiscardRevision checks an expected UI snapshot under the same cross-process
// locks as deletion. Revision zero permits explicitly removing corrupt entries
// which have no readable revision; it never removes a healthy newer task.
func (s *Store) DiscardRevision(id string, revision uint64) error {
	return s.discard(id, &revision)
}

func (s *Store) discard(id string, revision *uint64) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.root == nil {
		return ErrClosed
	}
	gate, err := s.lock(".store.lock")
	if err != nil {
		return err
	}
	defer releaseLock(gate)
	if err := s.directory(id); err != nil {
		return err
	}
	lock, err := s.lock(filepath.Join(id, ".lease"))
	if err != nil {
		return err
	}
	if revision != nil {
		manifest, _, loadErr := s.load(id)
		if *revision == 0 {
			if loadErr == nil {
				_ = releaseLock(lock)
				return ErrConflict
			}
			if !errors.Is(loadErr, ErrCorrupt) && !errors.Is(loadErr, os.ErrNotExist) {
				_ = releaseLock(lock)
				return loadErr
			}
		} else if loadErr != nil || manifest.Revision != *revision {
			_ = releaseLock(lock)
			if loadErr != nil {
				return loadErr
			}
			return ErrConflict
		}
	}
	dir, err := s.root.Open(id)
	if err != nil {
		_ = releaseLock(lock)
		return err
	}
	entries, err := dir.ReadDir(formats.CheckpointFiles + 1)
	_ = dir.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > formats.CheckpointFiles {
		_ = releaseLock(lock)
		return ErrCapacity
	}
	for _, entry := range entries {
		if !managedFile(entry.Name()) || entry.IsDir() {
			_ = releaseLock(lock)
			return errors.New("任务目录包含未知文件，未执行清理")
		}
	}
	// All acquisitions take the global gate before the per-task nonblocking
	// lease. Keep the gate until deletion ends; closing .lease for Windows
	// cannot admit another owner in the middle of removal.
	if err := releaseLock(lock); err != nil {
		return err
	}
	for _, entry := range entries {
		if err := s.root.Remove(filepath.Join(id, entry.Name())); err != nil {
			return err
		}
	}
	if err := s.root.Remove(id); err != nil {
		return err
	}
	return s.syncDirectory(".")
}
