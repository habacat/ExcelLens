// Package recovery owns bounded, checksummed recovery journals for page data.
// It never evaluates formulas, writes source workbooks or shares a tab's edits.
package recovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	magic          = "ELREC001"
	MaxDataBytes   = 8 << 20
	MaxRecords     = 20000
	MaxRecordBytes = 64 << 10
	MaxRevision    = 1<<53 - 1 // Exact in both Go and browser JSON numbers.
	maxFrame       = 16 << 20
	maxLog         = 64 << 20
	cacheBudget    = 32 << 20
)

var ErrConflict = errors.New("恢复副本版本已变化，本页输入未覆盖")
var ErrCorrupt = errors.New("恢复副本校验失败，请选择其他副本或另存恢复副本")
var ErrCapacity = errors.New("恢复副本超过8 MiB或20,000条，原恢复副本未覆盖")

type Change struct {
	Key  string          `json:"key"`
	Data json.RawMessage `json:"data"`
}
type Request struct {
	Owner     string   `json:"owner"`
	Kind      string   `json:"kind"`
	Revision  uint64   `json:"revision"`
	RequestID string   `json:"requestId"`
	Changes   []Change `json:"changes"`
}
type Meta struct {
	Revision  uint64 `json:"revision"`
	Recovered bool   `json:"recovered,omitempty"`
}
type Snapshot struct {
	Meta
	Records map[string]json.RawMessage `json:"records"`
}
type Entry struct {
	Owner   string    `json:"owner"`
	Kind    string    `json:"kind"`
	Updated time.Time `json:"updated"`
	Bytes   int64     `json:"bytes"`
	Backup  bool      `json:"backup,omitempty"`
}
type frame struct {
	Revision   uint64     `json:"revision"`
	RequestID  string     `json:"requestId,omitempty"`
	Changes    changeList `json:"changes"`
	Checkpoint bool       `json:"checkpoint,omitempty"`
	Recent     recentList `json:"recent,omitempty"`
	Digest     string     `json:"digest"`
}

type recentList []string

func (ids *recentList) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('[') {
		return ErrCorrupt
	}
	for d.More() {
		if len(*ids) >= 64 {
			return ErrCorrupt
		}
		var id string
		if err := d.Decode(&id); err != nil || !validID(id) {
			return ErrCorrupt
		}
		*ids = append(*ids, id)
	}
	_, err = d.Token()
	return err
}

// Only the write boundary is replaceable, so tests can exercise a failed Sync
// or checkpoint installation without changing the durable read protocol.
type journalWriter interface {
	io.Writer
	Sync() error
	Close() error
}

type changeList []Change

func (c *changeList) UnmarshalJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	start, err := d.Token()
	if err != nil || start != json.Delim('[') {
		return ErrCorrupt
	}
	for d.More() {
		if len(*c) >= MaxRecords {
			return ErrCorrupt
		}
		var item Change
		if err := d.Decode(&item); err != nil {
			return err
		}
		if err := validateChange(item); err != nil {
			return err
		}
		*c = append(*c, item)
	}
	_, err = d.Token()
	return err
}

type state struct {
	Snapshot
	bytes      int
	retained   int
	recent     []string
	fileSize   int64
	modified   time.Time
	tick       uint64
	fromBackup bool
	digest     string
}
type Store struct {
	root       string
	mu         sync.Mutex
	states     map[string]*state
	tick       uint64
	bytes      int
	openAppend func(string) (journalWriter, error)
	rename     func(string, string) error
}

func New(root string) *Store {
	return &Store{root: root, states: map[string]*state{}, rename: os.Rename,
		openAppend: func(path string) (journalWriter, error) {
			return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		}}
}
func validID(s string) bool {
	if len(s) < 20 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}
func (s *Store) path(owner, kind string) (string, error) {
	if !validID(owner) || (kind != "calculator" && kind != "analysis") {
		return "", errors.New("恢复副本标识无效")
	}
	// Preserve case-sensitive IDs on case-insensitive filesystems as well.
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(owner)))
	return filepath.Join(s.root, "~"+encoded+"."+kind+".journal"), nil
}
func charge(key string, data []byte) int {
	if len(data) == 0 {
		return 0
	}
	encoded, _ := json.Marshal(key)
	return len(encoded) + len(data) + 128
}
func (s *Store) forget(path string) {
	if old := s.states[path]; old != nil {
		s.bytes -= old.retained
		delete(s.states, path)
	}
}
func (s *Store) retain(path string, v *state) {
	s.forget(path)
	s.tick++
	v.tick = s.tick
	v.retained = v.bytes
	s.states[path] = v
	s.bytes += v.bytes
	for s.bytes > cacheBudget || len(s.states) > 64 {
		oldPath := ""
		oldTick := ^uint64(0)
		for p, old := range s.states {
			if old.tick < oldTick {
				oldPath, oldTick = p, old.tick
			}
		}
		s.forget(oldPath)
	}
}
func (s *Store) load(path string) (*state, error) {
	info, err := os.Stat(path)
	if err == nil {
		if v := s.states[path]; v != nil && v.fileSize == info.Size() && v.modified.Equal(info.ModTime()) {
			s.tick++
			v.tick = s.tick
			return v, nil
		}
	}
	s.forget(path)
	v, err := read(path)
	if errors.Is(err, os.ErrNotExist) {
		if backup, e := read(path + ".bak"); e == nil {
			v, err = backup, nil
			v.Recovered = true
			v.fromBackup = true
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		} else {
			v = &state{Snapshot: Snapshot{Records: map[string]json.RawMessage{}}}
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	s.retain(path, v)
	return v, nil
}
func read(path string) (*state, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < 8 || info.Size() > maxLog {
		return nil, ErrCorrupt
	}
	header := make([]byte, 8)
	if _, err = io.ReadFull(f, header); err != nil || string(header) != magic {
		return nil, ErrCorrupt
	}
	v := &state{Snapshot: Snapshot{Records: map[string]json.RawMessage{}}, fileSize: info.Size(), modified: info.ModTime()}
	good := int64(8)
	for {
		var prefix [8]byte
		n, e := io.ReadFull(f, prefix[:])
		if e == io.EOF {
			return v, nil
		}
		if e == io.ErrUnexpectedEOF {
			return repairTail(path, v, good, append([]byte(nil), prefix[:n]...))
		}
		if e != nil {
			return nil, e
		}
		length := binary.LittleEndian.Uint32(prefix[:])
		if length == 0 || length > maxFrame {
			return nil, ErrCorrupt
		}
		payload := make([]byte, int(length))
		n, e = io.ReadFull(f, payload)
		if e == io.ErrUnexpectedEOF || e == io.EOF {
			return repairTail(path, v, good, append(prefix[:], payload[:n]...))
		}
		if e != nil {
			return nil, e
		}
		if crc32.ChecksumIEEE(payload) != binary.LittleEndian.Uint32(prefix[4:]) {
			return nil, ErrCorrupt
		}
		var item frame
		if json.Unmarshal(payload, &item) != nil || item.Revision == 0 || item.Revision > MaxRevision ||
			(!item.Checkpoint && (item.Revision != v.Revision+1 || !validID(item.RequestID) || len(item.Recent) != 0)) ||
			(item.Checkpoint && (good != 8 || item.RequestID != "")) {
			return nil, ErrCorrupt
		}
		if item.Checkpoint {
			if len(item.Digest) != 64 {
				return nil, ErrCorrupt
			}
			v.recent = append([]string(nil), item.Recent...)
			if len(v.recent) > 64 {
				return nil, ErrCorrupt
			}
		}
		if !item.Checkpoint && item.Digest != digest(item.Changes) {
			return nil, ErrCorrupt
		}
		for _, c := range item.Changes {
			if err := validateChange(c); err != nil {
				return nil, ErrCorrupt
			}
			v.bytes -= charge(c.Key, v.Records[c.Key])
			if len(c.Data) == 0 || bytes.Equal(c.Data, []byte("null")) {
				delete(v.Records, c.Key)
			} else {
				v.Records[c.Key] = c.Data
				v.bytes += charge(c.Key, c.Data)
			}
		}
		if v.bytes > MaxDataBytes || len(v.Records) > MaxRecords {
			return nil, ErrCorrupt
		}
		v.Revision = item.Revision
		v.digest = item.Digest
		if item.RequestID != "" {
			v.recent = append(v.recent, item.RequestID)
			if len(v.recent) > 64 {
				v.recent = v.recent[len(v.recent)-64:]
			}
		}
		good += 8 + int64(length)
	}
}
func repairTail(path string, v *state, good int64, tail []byte) (*state, error) {
	// Preserve the incomplete bytes before truncating only the uncommitted tail.
	backup := fmt.Sprintf("%s.incomplete-%08x", path, crc32.ChecksumIEEE(tail))
	f, err := os.OpenFile(backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		var n int
		n, err = f.Write(tail)
		if err == nil && n != len(tail) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err == nil {
			err = closeErr
		}
	} else if errors.Is(err, os.ErrExist) {
		existing, e := os.ReadFile(backup)
		if e == nil && bytes.Equal(existing, tail) {
			err = nil
		}
	}
	if err != nil {
		return nil, err
	}
	f, err = os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Truncate(good)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	v.Recovered = true
	v.fileSize = good
	if info, e := os.Stat(path); e == nil {
		v.modified = info.ModTime()
	}
	return v, nil
}
func validateChange(c Change) error {
	if len(c.Key) == 0 || len(c.Key) > 1024 || len(c.Data) > MaxRecordBytes || (len(c.Data) > 0 && !json.Valid(c.Data)) {
		return errors.New("恢复记录内容超限或无效")
	}
	return nil
}
func (s *Store) Load(owner, kind string, backup bool) (Snapshot, error) {
	path, err := s.path(owner, kind)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var v *state
	if backup {
		v, err = read(path + ".bak")
	} else {
		v, err = s.load(path)
	}
	if err != nil {
		return Snapshot{}, err
	}
	copy := Snapshot{Meta: v.Meta, Records: make(map[string]json.RawMessage, len(v.Records))}
	for k, data := range v.Records {
		copy.Records[k] = bytes.Clone(data)
	}
	return copy, nil
}
func (s *Store) Update(req Request) (Meta, error) {
	path, err := s.path(req.Owner, req.Kind)
	if err != nil {
		return Meta{}, err
	}
	if !validID(req.RequestID) || req.Revision > MaxRevision || len(req.Changes) == 0 || len(req.Changes) > 2000 {
		return Meta{}, errors.New("恢复保存参数无效")
	}
	requestBytes := 0
	for _, c := range req.Changes {
		if err = validateChange(c); err != nil {
			return Meta{}, err
		}
		requestBytes += charge(c.Key, c.Data)
		if requestBytes > maxFrame {
			return Meta{}, errors.New("恢复保存请求过大")
		}
	}
	// Canonicalize repeated keys and JSON whitespace before hashing the retry.
	changes := map[string]json.RawMessage{}
	for _, c := range req.Changes {
		data := c.Data
		if len(data) > 0 {
			var compact bytes.Buffer
			_ = json.Compact(&compact, data)
			data = compact.Bytes()
		}
		if bytes.Equal(data, []byte("null")) {
			data = nil
		}
		changes[c.Key] = data
	}
	items := sortedChanges(changes)
	requestDigest := digest(items)
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.load(path)
	if err != nil {
		return Meta{}, err
	}
	for _, id := range v.recent {
		if id == req.RequestID {
			// A retry can acknowledge only its own revision. If another writer
			// advanced this owner, the page must reconcile rather than silently
			// continue with data it has never seen.
			if req.Revision+1 != v.Revision || requestDigest != v.digest {
				return Meta{}, ErrConflict
			}
			syncPath := path
			if v.fromBackup {
				syncPath += ".bak"
			}
			f, e := os.OpenFile(syncPath, os.O_WRONLY, 0600)
			if e != nil {
				return Meta{}, e
			}
			e = f.Sync()
			closeErr := f.Close()
			if e == nil {
				e = closeErr
			}
			return v.Meta, e
		}
	}
	if req.Revision != v.Revision {
		return Meta{}, ErrConflict
	}
	if v.Revision == MaxRevision {
		return Meta{}, errors.New("恢复副本版本达到上限，请另存恢复副本")
	}
	size, count := v.bytes, len(v.Records)
	for key, data := range changes {
		old, exists := v.Records[key]
		size -= charge(key, old)
		if exists {
			count--
		}
		if len(data) > 0 {
			size += charge(key, data)
			count++
		}
	}
	if size > MaxDataBytes || count > MaxRecords {
		return Meta{}, ErrCapacity
	}
	if err = os.MkdirAll(s.root, 0700); err != nil {
		return Meta{}, err
	}
	if v.fromBackup {
		if err = s.checkpoint(path, v); err != nil {
			return Meta{}, err
		}
		v.fromBackup = false
	}
	payload, err := marshalJournal(frame{Revision: v.Revision + 1, RequestID: req.RequestID, Changes: items, Digest: requestDigest})
	if err != nil || len(payload) > maxFrame {
		return Meta{}, errors.New("恢复保存请求过大")
	}
	if v.fileSize+int64(len(payload))+8 > maxLog {
		if err := s.checkpoint(path, v); err != nil {
			return Meta{}, fmt.Errorf("恢复日志达到安全限制：%w", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return Meta{}, err
		}
		v.fileSize, v.modified = info.Size(), info.ModTime()
	}
	f, err := s.openAppend(path)
	if err != nil {
		return Meta{}, err
	}
	if v.fileSize == 0 {
		var n int
		n, err = io.WriteString(f, magic)
		if err == nil && n != len(magic) {
			err = io.ErrShortWrite
		}
	}
	if err == nil {
		err = writeFrame(f, payload)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		s.forget(path)
		return Meta{}, err
	}
	for key, data := range changes {
		if len(data) == 0 {
			delete(v.Records, key)
		} else {
			v.Records[key] = bytes.Clone(data)
		}
	}
	v.bytes = size
	v.Revision++
	v.digest = requestDigest
	v.recent = append(v.recent, req.RequestID)
	if len(v.recent) > 64 {
		v.recent = v.recent[len(v.recent)-64:]
	}
	if info, e := os.Stat(path); e == nil {
		v.fileSize = info.Size()
		v.modified = info.ModTime()
	}
	// A failed maintenance checkpoint does not revoke an already synced append.
	if v.fileSize > 4<<20 {
		if s.checkpoint(path, v) == nil {
			if info, e := os.Stat(path); e == nil {
				v.fileSize = info.Size()
				v.modified = info.ModTime()
			}
		}
	}
	s.retain(path, v)
	return v.Meta, nil
}
func writeFrame(w io.Writer, payload []byte) error {
	var p [8]byte
	binary.LittleEndian.PutUint32(p[:], uint32(len(payload)))
	binary.LittleEndian.PutUint32(p[4:], crc32.ChecksumIEEE(payload))
	if n, err := w.Write(p[:]); err != nil {
		return err
	} else if n != len(p) {
		return io.ErrShortWrite
	}
	n, err := w.Write(payload)
	if err == nil && n != len(payload) {
		err = io.ErrShortWrite
	}
	return err
}
func sortedChanges(changes map[string]json.RawMessage) changeList {
	items := make(changeList, 0, len(changes))
	for key, data := range changes {
		items = append(items, Change{key, data})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

func digest(items changeList) string {
	encoded, _ := marshalJournal(items)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func marshalJournal(value any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	// These are JSON frames, not HTML. Escaping RawMessage again can multiply
	// its encoded size and make an accepted record fail its own replay limit.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), nil
}

func (s *Store) checkpoint(path string, v *state) error {
	payload, err := marshalJournal(frame{Revision: v.Revision, Changes: sortedChanges(v.Records), Checkpoint: true, Recent: v.recent, Digest: v.digest})
	if err != nil {
		return err
	}
	if len(payload) > maxFrame {
		return errors.New("恢复检查点超过安全上限")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = io.WriteString(f, magic)
	}
	if err == nil {
		err = writeFrame(f, payload)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err = os.Stat(path); err == nil {
		if err = os.Remove(path + ".bak"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = s.rename(path, path+".bak"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = s.rename(f.Name(), path); err != nil {
		_ = s.rename(path+".bak", path)
	}
	return err
}
func (s *Store) List(kind string) ([]Entry, error) {
	if kind != "calculator" && kind != "analysis" {
		return nil, errors.New("恢复类型无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := os.Open(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	result := []Entry{}
	for {
		entries, err := dir.ReadDir(128)
		if err != nil && err != io.EOF {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			backup := strings.HasSuffix(name, ".bak")
			if backup {
				name = strings.TrimSuffix(name, ".bak")
			}
			suffix := "." + kind + ".journal"
			if !strings.HasSuffix(name, suffix) {
				continue
			}
			encoded := strings.TrimSuffix(name, suffix)
			if !strings.HasPrefix(encoded, "~") {
				continue
			}
			raw, e := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(encoded[1:]))
			owner := string(raw)
			if e != nil || !validID(owner) {
				continue
			}
			info, e := entry.Info()
			if e != nil || info.Size() < 8 {
				continue
			}
			result = append(result, Entry{Owner: owner, Kind: kind, Updated: info.ModTime().UTC(), Bytes: info.Size(), Backup: backup})
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Updated.After(result[j].Updated) })
		if len(result) > 50 {
			result = result[:50]
		}
		if err == io.EOF {
			break
		}
	}
	return result, nil
}
