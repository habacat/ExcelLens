package conditional

import (
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSharedIndexPublicationCancellationAndRepair(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "stats.db")
	key := Tuple([]string{"a", "bc"})
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err := Open(ctx, file, "sheet", 1, func(w *Writer) error {
		if err := w.Group(0, key, 2, 1, Value{}); err != nil {
			return err
		}
		cancel()
		return w.Next()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("cancel published an incomplete index", err)
	}
	staged, _ := filepath.Glob(filepath.Join(dir, ".conditional-*"))
	if len(staged) != 0 {
		t.Fatal(staged)
	}
	var builds atomic.Int32
	build := func(w *Writer) error { builds.Add(1); return w.Group(0, key, 3, 2, Value{Number: big.NewRat(7, 2)}) }
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			idx, _, e := Open(context.Background(), file, "sheet", 1, build)
			if e != nil {
				t.Error(e)
				return
			}
			defer idx.Close()
			v, e := idx.Lookup(0, key, false)
			if e != nil || v.Count != 2 || v.First != 3 || v.Number.Cmp(big.NewRat(7, 2)) != 0 {
				t.Errorf("%+v, %v", v, e)
			}
			v, e = idx.Lookup(0, Tuple([]string{"ab", "c"}), false)
			if e != nil || v.Count != 0 {
				t.Error("compound keys collided", v, e)
			}
		})
	}
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatal("unexpected builds", builds.Load())
	}
	if err = os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	idx, rebuilt, e := Open(context.Background(), file, "sheet", 1, build)
	if e != nil {
		t.Fatal(e)
	}
	defer idx.Close()
	if !rebuilt || builds.Load() != 2 {
		t.Fatal("damaged derived index was not rebuilt")
	}
}
func TestMovingSumAndFirstMatchKeepSourceOrder(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stats.db")
	key := Tuple([]string{"k"})
	idx, _, err := Open(context.Background(), file, "sheet", 2, func(w *Writer) error {
		for i, x := range []int64{10000000000000000, 1, -10000000000000000, 5} {
			if err := w.Prefix(0, i+1, Value{Number: big.NewRat(x, 1)}); err != nil {
				return err
			}
		}
		if err := w.Issue(0, 5, Value{Unknown: true, Fault: "missing cache"}); err != nil {
			return err
		}
		if err := w.Group(1, key, 2, 1, Value{}); err != nil {
			return err
		}
		return w.Issue(1, 3, Value{Unknown: true, Fault: "missing cache"})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	for _, tc := range []struct {
		a, b int
		want int64
	}{{1, 3, 0}, {2, 2, 1}, {4, 4, 5}} {
		r, e := idx.Sum(0, tc.a, tc.b)
		if e != nil || r.Number == nil || r.Number.Cmp(big.NewRat(tc.want, 1)) != 0 {
			t.Fatal(tc, r, e)
		}
	}
	r, e := idx.Sum(0, 4, 5)
	if e != nil || !r.Unknown {
		t.Fatal(r, e)
	}
	r, e = idx.Lookup(1, key, true)
	if e != nil || r.Unknown || r.First != 2 {
		t.Fatal(r, e)
	}
	r, e = idx.Lookup(1, Tuple([]string{"absent"}), true)
	if e != nil || !r.Unknown {
		t.Fatal(r, e)
	}
}
