package conditional

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenExistingAndCheckDetectDamageWithoutRebuilding(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "book1-v1-abc.db")

	state, err := Check(file, "sig1", 1)
	if err != nil || state != StateMissing {
		t.Fatalf("missing file: state=%v err=%v", state, err)
	}
	if _, err := OpenExisting(file, "sig1", 1); !errors.Is(err, ErrMissing) {
		t.Fatalf("expected ErrMissing, got %v", err)
	}

	// Zero-byte file must be detected as damaged, NOT ready.
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	state, err = Check(file, "sig1", 1)
	if err != nil || state != StateDamaged {
		t.Fatalf("zero-byte file: state=%v err=%v", state, err)
	}
	if _, err := OpenExisting(file, "sig1", 1); !errors.Is(err, ErrDamaged) {
		t.Fatalf("expected ErrDamaged for zero-byte file, got %v", err)
	}

	// Truncated garbage file must also be detected as damaged.
	if err := os.WriteFile(file, []byte("not-a-bbolt-file"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = Check(file, "sig1", 1)
	if err != nil || state != StateDamaged {
		t.Fatalf("corrupt file: state=%v err=%v", state, err)
	}

	// Build repairs the damaged file cleanly.
	idx, built, err := Build(context.Background(), file, "sig1", 1, func(w *Writer) error {
		return w.addGroup(0, Tuple([]string{"k"}), 2, 1, Value{Number: big.NewRat(4, 1)})
	}, nil)
	if err != nil || !built {
		t.Fatalf("Build failed: built=%v err=%v", built, err)
	}
	if !IsFileInUse(file) {
		t.Fatal("open reader must pin file in use")
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	if IsFileInUse(file) {
		t.Fatal("closed reader must unpin file")
	}
	state, err = Check(file, "sig1", 1)
	if err != nil || state != StateReady {
		t.Fatalf("healthy file: state=%v err=%v", state, err)
	}
	// Wrong key or more plans must be detected as StateDamaged.
	state, err = Check(file, "sig-other", 1)
	if err != nil || state != StateDamaged {
		t.Fatalf("mismatched key: state=%v err=%v", state, err)
	}
}

func TestBudgetLRUEvictionProtectsActiveReadersAndOpenWorkbooks(t *testing.T) {
	dir := t.TempDir()
	cacheName := func(label string) string { return fmt.Sprintf("%s-v1-%x.db", label, sha256.Sum256([]byte(label))) }
	foreign := filepath.Join(dir, "my-ledger.db")
	if err := os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if raw, err := os.ReadFile(foreign); err != nil || string(raw) != "keep" {
			t.Error("unrelated file removed", err)
		}
	})
	buildFile := func(name string, age time.Duration) (string, int64) {
		t.Helper()
		file := filepath.Join(dir, name)
		idx, _, err := Build(context.Background(), file, name, 1, func(w *Writer) error {
			return w.addGroup(0, Tuple([]string{name}), 2, 1, Value{})
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := idx.Close(); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(file, when, when); err != nil {
			t.Fatal(err)
		}
		cacheMu.Lock()
		lastAccessed[cacheFileKey(file)] = when
		cacheMu.Unlock()
		st, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		return file, st.Size()
	}

	oldest, s1 := buildFile(cacheName("bookA"), 30*time.Minute)
	active, s2 := buildFile(cacheName("bookB"), 20*time.Minute)
	newest, s3 := buildFile(cacheName("bookC"), 5*time.Minute)

	// Open active file as an active reader and protect bookA by callback -> only bookC is evictable?
	// Let's protect active via open handle, leave oldest & newest unprotected, and set budget to s2+s3.
	openIdx, err := OpenExisting(active, cacheName("bookB"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer openIdx.Close()

	report, err := EnforceBudget(BudgetPolicy{
		Dir:         dir,
		BudgetBytes: s2 + s3,
	}, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.EvictedFiles != 1 || report.FreedBytes != s1 {
		t.Fatalf("expected oldest file evicted: %+v", report)
	}
	if _, err := os.Stat(oldest); !os.IsNotExist(err) {
		t.Fatal("oldest unprotected file should have been evicted")
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("active reader file was evicted")
	}
	if _, err := os.Stat(newest); err != nil {
		t.Fatal("newer file within budget was evicted")
	}

	// Protecting newest via callback while active handle is open prevents ClearUnused from touching either.
	clearReport, err := ClearUnused(dir, func(name string) bool { return name == cacheName("bookC") })
	if err != nil {
		t.Fatal(err)
	}
	if clearReport.RemovedFiles != 0 || clearReport.KeptFiles != 2 {
		t.Fatalf("protected files should stay: %+v", clearReport)
	}

	// Publishing a new file under a tight budget that only fits active + new evicts unprotected newest.
	fourth := filepath.Join(dir, cacheName("bookD"))
	idx4, built, err := Build(context.Background(), fourth, cacheName("bookD"), 1, func(w *Writer) error {
		return w.addGroup(0, Tuple([]string{"d"}), 2, 1, Value{})
	}, &BudgetPolicy{
		Dir:         dir,
		BudgetBytes: s2 + s3, // enough for active + fourth, not active + newest + fourth
	})
	if err != nil || !built {
		t.Fatalf("Build with LRU eviction failed: built=%v err=%v", built, err)
	}
	_ = idx4.Close()
	if _, err := os.Stat(newest); !os.IsNotExist(err) {
		t.Fatal("newest should have been evicted to stay within global budget")
	}
	if _, err := os.Stat(active); err != nil {
		t.Fatal("active reader must remain protected")
	}
}

func TestConcurrentCachePublicationHonorsOneGlobalBudget(t *testing.T) {
	dir := t.TempDir()
	const size = 4096
	policy := &BudgetPolicy{Dir: dir, BudgetBytes: size, Protected: func(string) bool { return true }}
	ready := make(chan struct{})
	done := make(chan error, 8)
	for n := range cap(done) {
		tmp := filepath.Join(dir, fmt.Sprintf(".conditional-%d.tmp", n))
		if err := os.WriteFile(tmp, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, fmt.Sprintf("book-v1-%064x.db", n))
		go func() { <-ready; done <- publishIndex(context.Background(), tmp, dst, size, policy) }()
	}
	close(ready)
	published := 0
	for range cap(done) {
		err := <-done
		if err == nil {
			published++
		} else if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatal(err)
		}
	}
	stats, err := InspectDir(dir, nil)
	if published != 1 || err != nil || stats.Bytes != size {
		t.Fatal(published, stats, err)
	}
}
