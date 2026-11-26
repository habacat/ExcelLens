package rowindex

import (
	"context"
	"errors"
	"math/big"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"excellens/internal/formats"
)

func TestRationalKeyStreamExactOrderAcrossFloatTiesUnderflowOverflowAndSpilledRuns(t *testing.T) {
	const count = 3101
	values := []string{"900719925474099331/100", "900719925474099330/100", "-1/100", "0", "1/" + "1" + strings.Repeat("0", 400), "1" + strings.Repeat("0", 400), "-1" + strings.Repeat("0", 400), "2/3", "1/3", "-2/3"}
	rows := make([]KeyRecord, count)
	for at := range rows {
		rows[at] = KeyRecord{Position: at, Key: values[at%len(values)]}
	}
	want := slices.Clone(rows)
	sort.Slice(want, func(i, j int) bool {
		a, _ := new(big.Rat).SetString(want[i].Key)
		b, _ := new(big.Rat).SetString(want[j].Key)
		cmp := a.Cmp(b)
		return cmp < 0 || cmp == 0 && want[i].Position < want[j].Position
	})
	for _, budget := range []int64{32 << 10, 2 << 20} {
		dir := t.TempDir()
		before := processQueryQuota.used.Load()
		var got []KeyRecord
		err := SortRationalKeyStream(context.Background(), dir, count, budget, func(ctx context.Context, emit func(KeyRecord) error) error {
			for _, row := range rows {
				if err := emit(row); err != nil {
					return err
				}
			}
			return nil
		}, func(row KeyRecord) error { got = append(got, row); return nil }, nil)
		if err != nil || !slices.Equal(got, want) {
			t.Fatal("non-exact numeric order", budget, err, len(got))
		}
		if processQueryQuota.used.Load() != before || QueryFilesInUse(dir) {
			t.Fatal("runs leaked")
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatal("temporary runs not removed", entries)
		}
	}
}

func TestRationalKeyStreamRejectsUnboundedGrammarAndReleasesOnScanConsumerOrCancellation(t *testing.T) {
	for _, key := range []string{"", "1e2147483647", "1.5", "1/0", "1/-2", "1/", "/2", "-", "1/2/3", " 1", "NaN", strings.Repeat("1", formats.RationalSortKeyBytes+1)} {
		if _, ok := rationalKeyNumber(key); ok {
			t.Fatal("invalid grammar accepted", key[:min(80, len(key))])
		}
	}
	for _, kind := range []string{"scan", "consumer", "cancel"} {
		dir := t.TempDir()
		before := processQueryQuota.used.Load()
		ctx, cancel := context.WithCancel(context.Background())
		sentinel := errors.New("injected " + kind)
		err := SortRationalKeyStream(ctx, dir, 9000, 32<<10, func(ctx context.Context, emit func(KeyRecord) error) error {
			for at := 0; at < 9000; at++ {
				if at == 4000 && kind == "scan" {
					return sentinel
				}
				if at == 4000 && kind == "cancel" {
					cancel()
				}
				if err := emit(KeyRecord{Position: at, Key: strings.Repeat("9", 64)}); err != nil {
					return err
				}
			}
			return nil
		}, func(row KeyRecord) error {
			if kind == "consumer" {
				return sentinel
			}
			return nil
		}, nil)
		cancel()
		if kind == "cancel" && !errors.Is(err, context.Canceled) || kind != "cancel" && !errors.Is(err, sentinel) {
			t.Fatal(kind, err)
		}
		if processQueryQuota.used.Load() != before || QueryFilesInUse(dir) {
			t.Fatal(kind, "temporary quota leaked")
		}
	}
}
