package conditional

import (
	"bytes"
	"context"
	"math/big"
	"path/filepath"
	"testing"

	"excellens/internal/excelformula"
)

func TestGroupBufferAndDiskUseSourceOrderedV1Records(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
	}{{"buffered", 1 << 20}, {"spill", 220}} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "groups.db")
			key := Tuple([]string{"source"})
			want := new(big.Rat)
			terms := []*big.Rat{big.NewRat(10000000000000000, 1), big.NewRat(1, 1), big.NewRat(-10000000000000000, 1), big.NewRat(1, 10), big.NewRat(1, 8)}
			idx, _, err := Open(context.Background(), file, "source-ordered", 3, func(w *Writer) error {
				w.groupLimit = tc.limit
				for i, term := range terms {
					if err := w.nextRow(); err != nil {
						return err
					}
					if !excelformula.AddFloat(want, term) {
						t.Fatal("invalid fixture")
					}
					if err := w.addGroup(0, key, i+1, 1, Value{Number: term}); err != nil {
						return err
					}
					if err := w.putFirstMatch(1, key, i+1); err != nil {
						return err
					}
					if err := w.addGroup(2, key, i+1, 1, Value{Number: term}); err != nil {
						return err
					}
				}
				if err := w.addGroup(2, key, 6, 1, Value{Fault: "#N/A"}); err != nil {
					return err
				}
				return w.addGroup(2, key, 7, 1, Value{Fault: "uncached formula", Unknown: true})
			})
			if err != nil {
				t.Fatal(err)
			}
			defer idx.Close()
			got, err := idx.Lookup(0, key, false)
			if err != nil || got.First != 1 || got.Count != len(terms) || got.Number == nil || got.Number.Cmp(want) != 0 {
				t.Fatal(got, err)
			}
			raw := idx.tx.Bucket(bucketID(0)).Bucket([]byte("values")).Get(key)
			if !bytes.Equal(raw, encode(Result{Count: len(terms), First: 1, Value: Value{Number: want}})) {
				t.Fatal("v1 representation changed")
			}
			match, err := idx.Lookup(1, key, true)
			if err != nil || match.First != 1 {
				t.Fatal(match, err)
			}
			missing, err := idx.Lookup(2, key, false)
			if err != nil || !missing.Unknown || missing.Fault != "uncached formula" || missing.Count != 7 {
				t.Fatal(missing, err)
			}
		})
	}
}
