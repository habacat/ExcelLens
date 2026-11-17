package workbook

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"testing"
)

func TestOpaqueKeyStreamSharesBoundedRunsAndReleasesOnConsumerFailure(t *testing.T) {
	const count = 3001
	dir := t.TempDir()
	rows := make([]KeyRecord, count)
	for i := range rows {
		rows[i] = KeyRecord{Position: i, Key: fmt.Sprintf("\x01%s\x00%05d", []string{"A", "a", "ſ", "S", "K", "k"}[i%6], i*7919%97)}
	}
	want := slices.Clone(rows)
	sort.Slice(want, func(i, j int) bool {
		if want[i].Key != want[j].Key {
			return want[i].Key < want[j].Key
		}
		return want[i].Position < want[j].Position
	})
	before := processQueryQuota.used.Load()
	for _, failAfter := range []int{0, 17} {
		var got []KeyRecord
		failure := errors.New("consumer failed")
		err := SortKeyStream(context.Background(), dir, count, 32<<10,
			func(ctx context.Context, emit func(KeyRecord) error) error {
				for _, row := range rows {
					if err := emit(row); err != nil {
						return err
					}
				}
				return nil
			}, func(row KeyRecord) error {
				got = append(got, row)
				if len(got) == failAfter {
					return failure
				}
				return nil
			}, nil)
		if failAfter == 0 && (err != nil || !slices.Equal(got, want)) {
			t.Fatal("opaque sorting changed key bytes or stable source order", err)
		}
		if failAfter > 0 && !errors.Is(err, failure) {
			t.Fatal("consumer failure was lost", err)
		}
		if processQueryQuota.used.Load() != before || QueryFilesInUse(dir) {
			t.Fatal("key stream retained temporary files")
		}
	}
}
