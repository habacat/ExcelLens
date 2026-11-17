package server

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunBoundaryJumpMatchesCellNavigation(t *testing.T) {
	rng := rand.New(rand.NewSource(20261114))
	for sample := 0; sample < 500; sample++ {
		var rows []int
		var runs []occupiedRun
		for row := 1; row <= 300; row++ {
			if rng.Intn(3) == 0 {
				continue
			}
			rows = append(rows, row)
			if n := len(runs); n > 0 && runs[n-1].hi+1 == row {
				runs[n-1].hi = row
			} else {
				runs = append(runs, occupiedRun{row, row})
			}
		}
		for start := 0; start <= 301; start++ {
			for _, dir := range []int{-1, 1} {
				want := boundaryJump(rows, start, dir, 1, 300)
				if got := runBoundaryJump(runs, start, dir, 1, 300); got != want {
					t.Fatalf("start %d direction %d: got %d want %d runs=%v", start, dir, got, want, runs)
				}
			}
		}
	}
}

func TestNavigationDenseColumnCompactsAndCachedJumpDoesNotRead(t *testing.T) {
	isolateTestStorage(t)
	path := filepath.Join(t.TempDir(), "dense.csv")
	if err := os.WriteFile(path, []byte("id\n"+strings.Repeat("00042\n", 20000)), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.OpenPath(path); err != nil {
		t.Fatal(err)
	}
	wb := a.currentWorkbook()
	idx, err := wb.BuildIndex(wb.Sheets[0], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := a.columnOccupied(context.Background(), wb, idx, 1)
	if err != nil || !reflect.DeepEqual(runs, []occupiedRun{{1, 20001}}) {
		t.Fatalf("dense column not compressed: %v %v", runs, err)
	}
	if err := os.Rename(idx.XMLPath, idx.XMLPath+".unavailable"); err != nil {
		t.Fatal(err)
	}
	// A cached column remains usable even while every scan permit is occupied.
	a.querySem = make(chan struct{}, 1)
	a.querySem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := a.columnOccupied(ctx, wb, idx, 1); err != nil || !reflect.DeepEqual(got, runs) {
		t.Fatal("cached jump performed I/O or waited for a scan permit", got, err)
	}
	if _, err := a.columnOccupied(ctx, wb, idx, 2); !errors.Is(err, context.Canceled) {
		t.Fatal("queued navigation ignored cancellation", err)
	}
}

func BenchmarkNavigationDenseBoundary(b *testing.B) {
	rows := make([]int, 1_000_000)
	for i := range rows {
		rows[i] = i + 1
	}
	b.Run("row-list", func(b *testing.B) {
		for b.Loop() {
			boundaryJump(rows, 1, 1, 1, len(rows))
		}
	})
	b.Run("runs", func(b *testing.B) {
		runs := []occupiedRun{{1, len(rows)}}
		for b.Loop() {
			runBoundaryJump(runs, 1, 1, 1, len(rows))
		}
	})
}
