package workbook

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"excellens/internal/buildinfo"
	"excellens/internal/formats"
)

func TestRowProjectionMillionIndexedRecords(t *testing.T) {
	if os.Getenv("EXCELLENS_ROW_PROJECTION_SCALE") != "1" {
		t.Skip("optional synthetic index/order scale observation; not a workbook-file or desktop benchmark")
	}
	const count = 1_000_000
	const resident = 64 << 10 // lower developer budget deliberately exercises disk mapping
	idx := &SheetIndex{MaxRow: count, MaxCol: 1, Rows: make([]RowOffset, count)}
	for i := range idx.Rows {
		idx.Rows[i].Number = i + 1
	}
	positions := rand.New(rand.NewSource(20261115)).Perm(count)
	root := t.TempDir()
	hashOrder := func(order *RowOrder) string {
		h := sha256.New()
		var raw [8]byte
		for i := 0; i < order.Len(); i++ {
			position := order.At(i)
			if position != positions[i] {
				t.Fatal("source order was modified", i)
			}
			binary.LittleEndian.PutUint64(raw[:], uint64(position))
			_, _ = h.Write(raw[:])
		}
		return hex.EncodeToString(h.Sum(nil))
	}
	started := time.Now()
	base, err := NewRowOrder(context.Background(), filepath.Join(root, "source-order"), count, positions)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	orderMs := time.Since(started).Milliseconds()
	before := hashOrder(base)
	visibility, err := (SourceLayout{RowStates: []SourceAxisRange{{Min: 1, Max: count / 2, Hidden: true, HiddenSet: true}}}).BuildAxisVisibility(context.Background(), "row", formats.ExcelRows, AxisViewSettings{})
	if err != nil {
		t.Fatal(err)
	}
	quota := &queryQuota{max: 32 << 20}
	dir := filepath.Join(root, "view")
	started = time.Now()
	p, err := newRowProjection(context.Background(), dir, idx, base, visibility, 0, nil, resident, quota)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	projectMs := time.Since(started).Milliseconds()
	if !p.data.mapped || p.data.file == nil || p.Len() != count/2 || p.data.base.data != base.data {
		t.Fatal("scale observation did not exercise mapped mask with the shared original inverse")
	}
	maskBytes := p.data.file.size
	parentBytes := base.data.file.size
	if maskBytes >= int64(count) || quota.used.Load() != maskBytes {
		t.Fatal("mask expanded into a duplicated row vector", maskBytes, quota.used.Load())
	}
	// An active cancellation in the same directory may remove only its own
	// partial file, not the previously published still-live projection.
	ctx, cancel := context.WithCancel(context.Background())
	done := 0
	failed, err := newRowProjection(ctx, dir, idx, base, visibility, 0, func(n, _ int) {
		done = n
		if n > 0 {
			cancel()
		}
	}, resident, quota)
	cancel()
	if failed != nil || !errors.Is(err, context.Canceled) || done != rowProjectionBlockRows || quota.used.Load() != maskBytes {
		t.Fatal("active cancellation leaked or removed an existing projection", done, quota.used.Load(), err)
	}
	snapshot, err := p.Retain()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if hashOrder(base) != before {
		t.Fatal("projection changed input order")
	}
	if err := base.Close(); err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	expected := 0
	seen := 0
	for source := range snapshot.Iter() {
		for positions[expected] < count/2 {
			expected++
		}
		if source != positions[expected] || idx.RowAt(source).Number != source+1 {
			t.Fatal("visible source/order mismatch", seen, source, positions[expected])
		}
		if seen%4096 == 0 {
			if snapshot.At(seen) != source {
				t.Fatal("random access drifted", seen)
			}
			if at, ok := snapshot.Position(source); !ok || at != seen {
				t.Fatal("reverse access drifted", seen, at, ok)
			}
		}
		expected++
		seen++
	}
	if seen != count/2 {
		t.Fatal("partial visible stream", seen)
	}
	verifyMs := time.Since(started).Milliseconds()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if quota.used.Load() != 0 {
		t.Fatal("last visible snapshot retained disk bytes", quota.used.Load())
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("view files not reclaimed", files, err)
	}
	report := map[string]any{"version": buildinfo.Version, "result": "PASS", "syntheticIndexedRecords": count, "visibleRecordsVerified": seen, "sourceHiddenIntervals": 1, "forcedMaskMemoryBudget": resident, "mappedMaskBytes": maskBytes, "sharedParentOrderBytes": parentBytes, "inputOrderSHA256": before, "inputOrderUnchanged": true, "sharedOriginalInverse": true, "buildParentOrderMs": orderMs, "buildVisibilityProjectionMs": projectMs, "verifyVisibleStreamAfterOwnerRemovalMs": verifyMs, "cancelAfterOrderPositions": done, "heapAllocObserved": memory.HeapAlloc, "memoryIncludesSyntheticIndexAndPermutationAndOtherTestState": true, "sourceWorkbookFileBenchmark": false, "desktopManualAcceptance": false}
	raw, _ := json.MarshalIndent(report, "", "  ")
	t.Log(string(raw))
	if destination := os.Getenv("EXCELLENS_ROW_PROJECTION_SCALE_REPORT"); destination != "" {
		if err := os.WriteFile(destination, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
