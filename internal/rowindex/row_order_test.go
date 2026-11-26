package rowindex

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"testing"

	"excellens/internal/formats"
)

func testRowOrder(t *testing.T, positions []int, sourceRows, resident int) (*RowOrder, *QueryQuota) {
	t.Helper()
	quota := &QueryQuota{max: 32 << 20}
	b, err := NewRowOrderBuilder(t.TempDir(), sourceRows, resident, quota)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	for _, pos := range positions {
		if err := b.Add(context.Background(), pos); err != nil {
			t.Fatal(err)
		}
	}
	r, err := b.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r, quota
}

func TestContiguousRowSetUsesConstantStorageIncludingLargeSourcePositions(t *testing.T) {
	quota := &QueryQuota{max: 1}
	dir := filepath.Join(t.TempDir(), "not-created")
	b, err := NewRowOrderBuilder(dir, formats.ExternalRows, 16, quota)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Abort()
	const count = 2_000_001
	const first = formats.ExternalRows - count
	for i := 0; i < count; i++ {
		if err := b.Add(context.Background(), first+i); err != nil {
			t.Fatal(err)
		}
	}
	r, err := b.Finish(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Len() != count || r.At(count-1) != formats.ExternalRows-1 || r.data.rows != nil || r.data.mapping != nil || r.data.inverse != nil || quota.used.Load() != 0 {
		t.Fatal("continuous selection materialized per-row storage")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("implicit range created temporary files", err)
	}
	if at, ok := r.Position(formats.ExternalRows - 1); !ok || at != count-1 {
		t.Fatal(at, ok)
	}
	if _, ok := r.Position(first - 1); ok {
		t.Fatal("non-member was located")
	}
	if set := r.Set(); set.LowerBound(first+123) != 123 || set.At(123) != first+123 {
		t.Fatal("range rank changed")
	}
	if _, err := b.Finish(context.Background()); err == nil {
		t.Fatal("builder published more than once")
	}
}

func TestRowOrderAndRowSetAgreeWithIndependentPermutationsAndSparseMembers(t *testing.T) {
	rng := rand.New(rand.NewSource(20261114))
	for _, resident := range []int{8, ResidentOrderRows} {
		order := rng.Perm(317)
		for i := range order {
			order[i] = order[i]*3 + 1
		}
		r, quota := testRowOrder(t, order, 1000, resident)
		wantSet := slices.Clone(order)
		slices.Sort(wantSet)
		got := slices.Collect(r.Iter())
		if !reflect.DeepEqual(got, order) {
			t.Fatal("visual order changed")
		}
		set := r.Set()
		for visual, source := range order {
			at, ok := r.Position(source)
			if !ok || at != visual {
				t.Fatalf("source %d visual %d got %d %v", source, visual, at, ok)
			}
		}
		for i, source := range wantSet {
			if set.At(i) != source || set.LowerBound(source) != i {
				t.Fatal("membership order changed", i, source)
			}
		}
		for source := 0; source < 1000; source++ {
			at := sortIntSearch(wantSet, source)
			_, ok := r.Position(source)
			if set.LowerBound(source) != at || ok != (at < len(wantSet) && wantSet[at] == source) {
				t.Fatal("non-member/rank mismatch", source)
			}
		}
		page, err := r.Page(300, 100)
		if err != nil || !reflect.DeepEqual(page, order[300:]) {
			t.Fatal(page, err)
		}
		if resident == 8 {
			if r.data.mapping == nil || r.data.rows != nil || r.data.inverse != nil || quota.used.Load() != orderHeaderBytes+int64(len(order))*12 {
				t.Fatal("spilled order retained heap rows or merge runs", quota.used.Load())
			}
		}
	}
	// A source position is an integer address, never an int32 or floating value.
	r, _ := testRowOrder(t, []int{formats.ExternalRows - 1, 0, 99}, formats.ExternalRows, 8)
	if r.At(0) != formats.ExternalRows-1 {
		t.Fatal("large source position truncated")
	}
}

func sortIntSearch(values []int, source int) int {
	for i, value := range values {
		if value >= source {
			return i
		}
	}
	return len(values)
}

func TestSparseIncreasingOrderSpillsWithoutAnInverseAndCopiesCallerInput(t *testing.T) {
	positions := make([]int, 1000)
	for i := range positions {
		positions[i] = i * 2
	}
	r, quota := testRowOrder(t, positions, 2000, 16)
	if !r.data.increasing || quota.used.Load() != orderHeaderBytes+4000 || r.data.inverse != nil {
		t.Fatal("natural-order selection built a reverse map")
	}
	for i := range positions {
		if r.At(i) != positions[i] {
			t.Fatal(i)
		}
	}
	input := []int{7, 1, 9, 2}
	copy, err := NewRowOrder(context.Background(), t.TempDir(), 10, input)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	input[0] = 3
	if copy.At(0) != 7 {
		t.Fatal("caller can mutate published order")
	}
}

func TestRowOrderLeasesKeepMappingAliveDuringPruneGCAndConcurrentReaders(t *testing.T) {
	positions := rand.New(rand.NewSource(1114)).Perm(513)
	parent, quota := testRowOrder(t, positions, 513, 16)
	path := parent.data.file.path
	var group sync.WaitGroup
	start := make(chan struct{})
	for worker := 0; worker < 4; worker++ {
		lease, err := parent.Retain()
		if err != nil {
			t.Fatal(err)
		}
		group.Go(func() {
			defer lease.Close()
			<-start
			for round := 0; round < 100; round++ {
				for i, want := range positions {
					if lease.At(i) != want {
						t.Error("mapping changed after owner was closed")
						return
					}
				}
			}
		})
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if parent.data.mapping == nil || quota.used.Load() == 0 {
		t.Fatal("pruning owner invalidated a reader lease")
	}
	if _, err := parent.Retain(); err == nil {
		t.Fatal("closed lease was reused")
	}
	runtime.GC()
	close(start)
	group.Wait()
	if quota.used.Load() != 0 {
		t.Fatal("last reader did not release query disk space", quota.used.Load())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("last lease retained its private file", err)
	}
}

func TestRowOrderRejectsDuplicateInvalidAndOverBudgetBuildsWithoutPublishing(t *testing.T) {
	for _, resident := range []int{2, 16} {
		quota := &QueryQuota{max: 1 << 20}
		dir := t.TempDir()
		b, _ := NewRowOrderBuilder(dir, 10, resident, quota)
		for _, pos := range []int{3, 0, 5, 3} {
			if err := b.Add(context.Background(), pos); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := b.Finish(context.Background()); err == nil {
			t.Fatal("non-adjacent duplicate source was accepted")
		}
		b.Abort()
		if quota.used.Load() != 0 {
			t.Fatal("failed inverse build leaked files", quota.used.Load())
		}
	}
	quota := &QueryQuota{max: 64}
	dir := t.TempDir()
	b, _ := NewRowOrderBuilder(dir, 1000, 4, quota)
	for i := 0; i < 100; i++ {
		if err := b.Add(context.Background(), i*2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Finish(context.Background()); err == nil {
		t.Fatal("disk budget was ignored")
	}
	b.Abort()
	entries, _ := os.ReadDir(dir)
	if quota.used.Load() != 0 || len(entries) != 0 {
		t.Fatal("rejected build left a published file", quota.used.Load(), entries)
	}
	for _, positions := range [][]int{{-1}, {10}, {1, 1}} {
		if r, err := NewRowOrder(context.Background(), dir, 10, positions); err == nil {
			_ = r.Close()
			t.Fatal("invalid positions accepted", positions)
		}
	}
}

func TestRowOrderCancellationAndViewportBounds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRowOrder(ctx, t.TempDir(), 10, []int{3}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	quota := &QueryQuota{max: 1 << 20}
	b, _ := NewRowOrderBuilder(t.TempDir(), 100, 4, quota)
	defer b.Abort()
	for i := 0; i < 10; i++ {
		if err := b.Add(context.Background(), i*2); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Finish(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	b.Abort()
	if quota.used.Load() != 0 {
		t.Fatal("cancelled builder retained quota")
	}
	r, _ := testRowOrder(t, []int{9, 2, 6}, 10, 16)
	for _, bounds := range [][2]int{{-1, 1}, {4, 1}, {0, -1}, {0, 10001}} {
		if _, err := r.Page(bounds[0], bounds[1]); err == nil {
			t.Fatal("unbounded/invalid viewport accepted", bounds)
		}
	}
	if page, err := r.Page(3, 10); err != nil || len(page) != 0 {
		t.Fatal(page, err)
	}
	_ = r.Close()
	if _, err := r.Page(0, 1); err == nil {
		t.Fatal("closed lease was read")
	}
}

func TestMappedOrderRejectsCorruptAndTruncatedFilesBeforeMapping(t *testing.T) {
	r, _ := testRowOrder(t, []int{7, 0, 9, 2, 8, 1, 5, 3, 6, 4}, 10, 2)
	original, err := os.ReadFile(r.data.file.path)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated": func(b []byte) []byte { return b[:len(b)-1] },
		"payload":   func(b []byte) []byte { b[orderHeaderBytes] ^= 1; return b },
		"header":    func(b []byte) []byte { b[12] ^= 1; return b },
		"forged-size": func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[12:], formats.ExternalRows)
			binary.LittleEndian.PutUint32(b[28:], crc32.ChecksumIEEE(b[:28]))
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bad.order")
			if err := os.WriteFile(path, mutate(slices.Clone(original)), 0600); err != nil {
				t.Fatal(err)
			}
			if mapping, err := mapOrderFile(context.Background(), path, 10, 10, false); err == nil {
				_ = UnmapFile(mapping)
				t.Fatal("corrupt query result mapped")
			}
		})
	}
}

func TestPairRunsVerifyChecksumAndPropagateOutputFailure(t *testing.T) {
	quota := &QueryQuota{max: 1 << 20}
	b, _ := NewRowOrderBuilder(t.TempDir(), 10, 2, quota)
	run, err := b.writePairRun(context.Background(), 2, func(emit func(uint64) error) error {
		if err := emit(1 << 32); err != nil {
			return err
		}
		return emit(2<<32 | 1)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Discard()
	want := io.ErrClosedPipe
	if err := mergePairRuns(context.Background(), []*QueryFile{run}, func(uint64) error { return want }); !errors.Is(err, want) {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(run.path)
	data[len(data)-1] ^= 1
	if err := os.WriteFile(run.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := mergePairRuns(context.Background(), []*QueryFile{run}, func(uint64) error { return nil }); err == nil {
		t.Fatal("corrupt run consumed as a valid sequence")
	}
}

func TestQueryCleanupPreservesLiveFilesAndRetriesRetiredFailures(t *testing.T) {
	dir := t.TempDir()
	quota := &QueryQuota{max: 1 << 20}
	live, err := newQueryFile(dir, quota)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Discard()
	_, _ = live.Write([]byte("live content"))
	_ = live.Close()
	orphan := filepath.Join(dir, ".query-interrupted.tmp")
	foreign := filepath.Join(dir, "preserved.txt")
	_ = os.WriteFile(orphan, []byte("incomplete old run"), 0600)
	_ = os.WriteFile(foreign, []byte("unrelated"), 0600)
	if err := quota.CleanStale(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live.path); err != nil {
		t.Fatal("live result removed", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("unrelated file removed", err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted query file not removed", err)
	}
	retired, err := newQueryFile(dir, quota)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = retired.Write([]byte("retired"))
	_ = retired.Close()
	// Simulate a removal failure with a nonempty directory occupying a private
	// temporary path, independent of platform-specific open-file sharing.
	if err := os.Remove(retired.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(retired.path, 0700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(retired.path, "held")
	_ = os.WriteFile(child, []byte("x"), 0600)
	charged := quota.used.Load()
	if err := retired.Discard(); err == nil || quota.used.Load() != charged {
		t.Fatal("failed cleanup released its disk charge", err)
	}
	if err := quota.CleanStale(context.Background(), dir); err != nil {
		t.Fatal("unrelated live query blocked by a charged retired file", err)
	}
	_ = os.Remove(child)
	if err := quota.CleanStale(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	if quota.used.Load() != live.size {
		t.Fatal("retired file cleanup did not release quota", quota.used.Load())
	}
}

func TestConcurrentQueryCreationCannotBeMistakenForStaleFiles(t *testing.T) {
	dir := t.TempDir()
	quota := &QueryQuota{max: 1 << 20}
	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Go(func() {
			for i := 0; i < 20; i++ {
				file, err := newQueryFile(dir, quota)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := file.Write([]byte("owned")); err != nil {
					t.Error(err)
				}
				if err := file.Close(); err != nil {
					t.Error(err)
				}
				if err := quota.CleanStale(context.Background(), dir); err != nil {
					t.Error(err)
				}
				if _, err := os.Stat(file.path); err != nil {
					t.Error("creation/registration lost ownership", err)
				}
				if err := file.Discard(); err != nil {
					t.Error(err)
				}
			}
		})
	}
	group.Wait()
	if quota.used.Load() != 0 || len(quota.files) != 0 {
		t.Fatal("query ownership leaked", quota.used.Load(), len(quota.files))
	}
}
