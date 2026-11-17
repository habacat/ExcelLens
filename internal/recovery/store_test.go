package recovery

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const testOwner = "recovery-owner-00000001"

func request(rev uint64, id string, changes ...Change) Request {
	return Request{Owner: testOwner, Kind: "calculator", Revision: rev, RequestID: "request-000000000000-" + id, Changes: changes}
}

func set(key, text string) Change {
	data, _ := json.Marshal(map[string]string{"text": text})
	return Change{Key: key, Data: data}
}

func mustUpdate(t *testing.T, s *Store, req Request) Meta {
	t.Helper()
	m, err := s.Update(req)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustLoad(t *testing.T, s *Store) Snapshot {
	t.Helper()
	v, err := s.Load(testOwner, "calculator", false)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRestartDeleteIsolationAndDetachedSnapshots(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	mustUpdate(t, s, request(0, "first", set("0", "=1+2"), set("1", "备注")))
	copy := mustLoad(t, s)
	copy.Records["0"][0] = '!'
	delete(copy.Records, "1")
	if len(mustLoad(t, s).Records) != 2 {
		t.Fatal("load returned mutable store data")
	}
	mustUpdate(t, s, request(1, "delete", Change{Key: "0", Data: json.RawMessage(" null ")}, set("1", "新备注")))
	v := mustLoad(t, New(root))
	if v.Revision != 2 || len(v.Records) != 1 || !bytes.Contains(v.Records["1"], []byte("新备注")) {
		t.Fatalf("restart lost committed edits: %+v", v)
	}
	for _, pair := range [][2]string{{"recovery-owner-00000002", "calculator"}, {testOwner, "analysis"}} {
		other, err := s.Load(pair[0], pair[1], false)
		if err != nil || other.Revision != 0 || len(other.Records) != 0 {
			t.Fatalf("owner/kind leaked: %+v, %v", other, err)
		}
	}
	for _, owner := range []string{"../recovery-owner-00000001", "short", strings.Repeat("x", 65)} {
		if _, err := s.Load(owner, "calculator", false); err == nil {
			t.Fatalf("invalid owner accepted: %q", owner)
		}
	}
}

func TestOptimisticWritersAndRetryIdentity(t *testing.T) {
	s := New(t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := range 2 {
		wg.Go(func() {
			_, err := s.Update(request(0, fmt.Sprint(i), set("0", fmt.Sprint(i))))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	ok, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("concurrent overwrite: %d successes, %d conflicts", ok, conflicts)
	}
	req := request(1, "next", set("0", "value"), set("1", "x"), set("1", "last"))
	mustUpdate(t, s, req)
	path, _ := s.path(testOwner, "calculator")
	before, _ := os.ReadFile(path)
	if m := mustUpdate(t, New(s.root), req); m.Revision != 2 {
		t.Fatal(m)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("retry appended another transaction")
	}
	altered := req
	altered.Changes = []Change{set("0", "changed")}
	if _, err := s.Update(altered); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused request ID acknowledged different data: %v", err)
	}
	mustUpdate(t, s, request(2, "newer", set("0", "other page")))
	if _, err := s.Update(req); !errors.Is(err, ErrConflict) {
		t.Fatalf("old retry concealed another writer: %v", err)
	}
}

type syncFailure struct{ *os.File }

func (f syncFailure) Sync() error { return errors.New("injected disk sync failure") }

func TestFailedSyncIsNotAcknowledgedAndCanBeRetried(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	s.openAppend = func(path string) (journalWriter, error) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		return syncFailure{f}, err
	}
	req := request(0, "sync", set("0", "输入仍在页面"))
	if _, err := s.Update(req); err == nil {
		t.Fatal("failed Sync was acknowledged")
	}
	if len(s.states) != 0 {
		t.Fatal("failed write remained cached as committed")
	}
	// The bytes may have reached the filesystem despite a failed acknowledgement.
	// A new process must sync the same receipt, not apply the edit twice.
	reopened := New(root)
	if m := mustUpdate(t, reopened, req); m.Revision != 1 {
		t.Fatal(m)
	}
	if got := mustLoad(t, reopened); got.Revision != 1 || len(got.Records) != 1 {
		t.Fatal(got)
	}
}

func TestInterruptedTailPreservesEvidenceButCompleteCorruptionFailsClosed(t *testing.T) {
	for _, tail := range [][]byte{{1, 2, 3}, {32, 0, 0, 0, 0, 0, 0, 0, '{', '"'}} {
		t.Run(fmt.Sprintf("tail-%d", len(tail)), func(t *testing.T) {
			s := New(t.TempDir())
			mustUpdate(t, s, request(0, "good", set("0", "valid")))
			path, _ := s.path(testOwner, "calculator")
			original, _ := os.ReadFile(path)
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			_, _ = f.Write(tail)
			_ = f.Close()
			v := mustLoad(t, New(s.root))
			if !v.Recovered || v.Revision != 1 || len(v.Records) != 1 {
				t.Fatal(v)
			}
			preserved, err := os.ReadFile(fmt.Sprintf("%s.incomplete-%08x", path, crc32.ChecksumIEEE(tail)))
			if err != nil || !bytes.Equal(preserved, tail) {
				t.Fatalf("tail evidence lost: %v", err)
			}
			fixed, _ := os.ReadFile(path)
			if !bytes.Equal(original, fixed) {
				t.Fatal("committed transaction changed during repair")
			}
			mustUpdate(t, New(s.root), request(1, "after-tail", set("1", "later")))
		})
	}
	s := New(t.TempDir())
	mustUpdate(t, s, request(0, "good", set("0", "original")))
	path, _ := s.path(testOwner, "calculator")
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(s.root).Load(testOwner, "calculator", false); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("checksum corruption was treated as EOF: %v", err)
	}
	if current, _ := os.ReadFile(path); !bytes.Equal(current, data) {
		t.Fatal("corrupt complete frame was silently rewritten")
	}
}

func TestTailRepairRefusesToDiscardUnpreservedBytes(t *testing.T) {
	s := New(t.TempDir())
	mustUpdate(t, s, request(0, "good", set("0", "committed")))
	path, _ := s.path(testOwner, "calculator")
	tail := []byte{1, 2}
	data, _ := os.ReadFile(path)
	data = append(data, tail...)
	_ = os.WriteFile(path, data, 0600)
	evidence := fmt.Sprintf("%s.incomplete-%08x", path, crc32.ChecksumIEEE(tail))
	_ = os.WriteFile(evidence, []byte("different evidence"), 0600)
	if _, err := New(s.root).Load(testOwner, "calculator", false); err == nil {
		t.Fatal("repaired without preserving the exact tail")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
		t.Fatal("unpreserved tail was discarded")
	}
}

func TestCheckpointFailureAndMissingPrimaryRecovery(t *testing.T) {
	s := New(t.TempDir())
	req := request(0, "first", set("0", "first"))
	mustUpdate(t, s, req)
	path, _ := s.path(testOwner, "calculator")
	v, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	s.rename = func(from, to string) error {
		if strings.HasSuffix(from, ".tmp") {
			return errors.New("injected install failure")
		}
		return os.Rename(from, to)
	}
	if err := s.checkpoint(path, v); err == nil {
		t.Fatal("checkpoint installation unexpectedly succeeded")
	}
	if got := mustLoad(t, New(s.root)); got.Revision != 1 {
		t.Fatal("failed checkpoint lost synced append", got)
	}
	s.rename = os.Rename
	if err := s.checkpoint(path, v); err != nil {
		t.Fatal(err)
	}
	checkpointCopy, _ := s.Load(testOwner, "calculator", true)
	if checkpointCopy.Revision != 1 {
		t.Fatal(checkpointCopy)
	}
	// Simulate process termination after moving the journal to .bak.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	reopened := New(s.root)
	if got := mustLoad(t, reopened); !got.Recovered || got.Revision != 1 {
		t.Fatal(got)
	}
	mustUpdate(t, reopened, req) // Sync retry against the recovered backup.
	mustUpdate(t, reopened, request(1, "continue", set("1", "second")))
	got := mustLoad(t, New(s.root))
	if got.Revision != 2 || len(got.Records) != 2 {
		t.Fatal("append after restoring backup failed", got)
	}
}

func TestCompactionKeepsRetryReceiptAndIndependentBackup(t *testing.T) {
	s := New(t.TempDir())
	text := strings.Repeat("x", 24<<10)
	var req Request
	for i := range 180 {
		req = request(uint64(i), fmt.Sprint(i), set("0", text+fmt.Sprint(i)))
		mustUpdate(t, s, req)
	}
	path, _ := s.path(testOwner, "calculator")
	info, _ := os.Stat(path)
	if info.Size() > 1<<20 {
		t.Fatal("journal did not compact", info.Size())
	}
	reopened := New(s.root)
	mustUpdate(t, reopened, req)
	got := mustLoad(t, reopened)
	if got.Revision != 180 || !bytes.Contains(got.Records["0"], []byte("179")) {
		t.Fatal("compaction lost data or receipt", got.Revision)
	}
	backup, err := reopened.Load(testOwner, "calculator", true)
	if err != nil || backup.Revision == 0 || backup.Revision >= got.Revision {
		t.Fatalf("backup is not a separate prior snapshot: %d %v", backup.Revision, err)
	}
}

func TestRecordAndMemoryLimitsAreAtomicAndCacheAccountingIsStable(t *testing.T) {
	s := New(t.TempDir())
	mustUpdate(t, s, request(0, "base", set("0", "kept")))
	for i, change := range []Change{{Key: "", Data: json.RawMessage(`1`)}, {Key: "1", Data: json.RawMessage(`{`)}, set("1", strings.Repeat("x", MaxRecordBytes))} {
		if _, err := s.Update(request(1, fmt.Sprint(i), change)); err == nil {
			t.Fatal("invalid record accepted", i)
		}
	}
	if got := mustLoad(t, s); got.Revision != 1 || len(got.Records) != 1 {
		t.Fatal("rejected update modified state", got)
	}
	for i := range 70 {
		req := request(0, fmt.Sprint(i), set(strings.Repeat("\n", 1000), "small"))
		req.Owner = fmt.Sprintf("recovery-owner-%020d", i)
		mustUpdate(t, s, req)
		req.Revision = 1
		req.RequestID += "b"
		req.Changes = []Change{set(strings.Repeat("\n", 1000), "larger content")}
		mustUpdate(t, s, req)
	}
	total := 0
	for _, v := range s.states {
		total += v.bytes
		if v.retained != v.bytes {
			t.Fatal("retained accounting diverged")
		}
	}
	if len(s.states) != 64 || s.bytes != total || s.bytes < 0 {
		t.Fatalf("LRU accounting mismatch: %d %d %d", len(s.states), s.bytes, total)
	}
	// Escaped keys count their JSON size, not only raw string bytes.
	if charge(strings.Repeat("\n", 1000), []byte(`1`)) != 2131 {
		t.Fatal("escaped key budget undercounted")
	}
	items := make([]Change, 300)
	for i := range items {
		items[i] = set(fmt.Sprint(i+1), strings.Repeat("x", 30<<10))
	}
	if _, err := s.Update(request(1, "over-budget", items...)); err == nil {
		t.Fatal("accepted more than 8 MiB")
	}
	if got := mustLoad(t, s); got.Revision != 1 || len(got.Records) != 1 {
		t.Fatal("oversized transaction partly applied")
	}
}

func TestMalformedFramesAndUnsafeRevisions(t *testing.T) {
	for name, item := range map[string]frame{
		"unsafe-revision":    {Revision: MaxRevision + 1, Checkpoint: true, Changes: changeList{}, Digest: strings.Repeat("0", 64)},
		"zero-revision":      {Revision: 0, Checkpoint: true, Changes: changeList{}},
		"unbounded-receipts": {Revision: 1, Checkpoint: true, Recent: recentList(make([]string, 65)), Changes: changeList{}},
		"unbounded-records":  {Revision: 1, Checkpoint: true, Changes: changeList(make([]Change, MaxRecords+1))},
		"bad-identity":       {Revision: 1, RequestID: "x", Changes: changeList{set("0", "text")}},
	} {
		t.Run(name, func(t *testing.T) {
			s := New(t.TempDir())
			path, _ := s.path(testOwner, "calculator")
			var b bytes.Buffer
			b.WriteString(magic)
			payload, _ := json.Marshal(item)
			_ = writeFrame(&b, payload)
			_ = os.WriteFile(path, b.Bytes(), 0600)
			if _, err := s.Load(testOwner, "calculator", false); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("malformed complete frame accepted: %v", err)
			}
		})
	}
	s := New(t.TempDir())
	if _, err := s.Update(request(MaxRevision+1, "unsafe", set("0", "x"))); err == nil {
		t.Fatal("unsafe browser revision accepted")
	}
	path, _ := s.path(testOwner, "calculator")
	v := &state{Snapshot: Snapshot{Meta: Meta{Revision: MaxRevision}, Records: map[string]json.RawMessage{}}, digest: strings.Repeat("0", 64)}
	if err := s.checkpoint(path, v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(request(MaxRevision, "overflow", set("0", "x"))); err == nil {
		t.Fatal("revision exceeded JS exact integer range")
	}
}

type shortWriter struct{ calls int }

func (w *shortWriter) Write(p []byte) (int, error) {
	w.calls++
	return len(p) - 1, nil
}

func TestShortWritesAndListMetadata(t *testing.T) {
	if err := writeFrame(&shortWriter{}, []byte("payload")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	s := New(t.TempDir())
	mustUpdate(t, s, request(0, "one", set("0", "data")))
	path, _ := s.path(testOwner, "calculator")
	v, _ := read(path)
	_ = s.checkpoint(path, v)
	_ = os.WriteFile(filepath.Join(s.root, "ignore.analysis.journal"), []byte(magic), 0600)
	list, err := s.List("calculator")
	if err != nil || len(list) != 2 || list[0].Owner != testOwner || list[0].Bytes < 8 || list[0].Updated.IsZero() || list[0].Backup == list[1].Backup {
		t.Fatalf("bad recovery list: %+v %v", list, err)
	}
	if list, err := s.List("analysis"); err != nil || !reflect.DeepEqual(list, []Entry{}) {
		t.Fatalf("invalid file entered list: %+v %v", list, err)
	}
	// A size prefix never causes allocation beyond the frame budget.
	var corrupt bytes.Buffer
	corrupt.WriteString(magic)
	_ = binary.Write(&corrupt, binary.LittleEndian, uint32(maxFrame+1))
	_ = binary.Write(&corrupt, binary.LittleEndian, uint32(0))
	_ = os.WriteFile(path, corrupt.Bytes(), 0600)
	if _, err := New(s.root).Load(testOwner, "calculator", false); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestHTMLLikeJSONAtRecordLimitReplaysWithoutEncodingExpansion(t *testing.T) {
	s := New(t.TempDir())
	raw := json.RawMessage(`"` + strings.Repeat("&", MaxRecordBytes-2) + `"`)
	mustUpdate(t, s, request(0, "html-text", Change{Key: "0", Data: raw}))
	got := mustLoad(t, New(s.root))
	if !bytes.Equal(got.Records["0"], raw) {
		t.Fatal("accepted record changed or failed its replay size limit")
	}
	path, _ := s.path(testOwner, "calculator")
	v, err := read(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkpoint(path, v); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, New(s.root)); !bytes.Equal(got.Records["0"], raw) {
		t.Fatal("checkpoint expanded HTML-like text")
	}
}

func TestOwnersDifferingOnlyByCaseRemainIndependentOnEveryFilesystem(t *testing.T) {
	s := New(t.TempDir())
	a := request(0, "lower-case", set("0", "lower page"))
	a.Owner = "recovery-owner-aaaaaaaa"
	b := request(0, "upper-case", set("0", "upper page"))
	b.Owner = "recovery-owner-AAAAaaaa"
	mustUpdate(t, s, a)
	mustUpdate(t, s, b)
	for _, req := range []Request{a, b} {
		got, err := New(s.root).Load(req.Owner, req.Kind, false)
		if err != nil || !bytes.Equal(got.Records["0"], req.Changes[0].Data) {
			t.Fatal("page owner collision", req.Owner, got, err)
		}
	}
	list, err := s.List("calculator")
	if err != nil || len(list) != 2 || list[0].Owner == list[1].Owner {
		t.Fatal("case-distinct pages merged in recovery list", list, err)
	}
}
