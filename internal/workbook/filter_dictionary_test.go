package workbook

import (
	"context"
	"slices"
	"testing"
)

func TestDictionaryFilterScanMatchesScalarAtEveryPosition(t *testing.T) {
	const n = 1159
	offsets := make([]RowOffset, n)
	idx := &SheetIndex{MaxCol: 2}
	w := &Workbook{}
	values := []CellData{{}, {Type: "s", Display: ""}, {Type: "s", Display: "  "},
		{Type: "s", Display: "001"}, {Type: "n", Raw: "1", Display: "001"},
		{Type: "s", Display: "K码"}, {Type: "s", Display: "k码"}, {Type: "b", Display: "TRUE"},
		{Type: "s", Display: "GroupA"}, {Type: "s", Display: "9007199254740993"},
		{Type: "n", Raw: "9007199254740993", Display: "9007199254740993"}}
	for i := range offsets {
		offsets[i].Number = 2*i + 1
	}
	idx.RowIndex = NewRowIndex(offsets)
	for col := 1; col <= 2; col++ {
		cache := beginColumnCache(n)
		for i := range n {
			k := (i*col + i/13) % len(values)
			cache = writeColumnCache(cache, i, values[k], k != 0)
		}
		w.cacheColumn(idx, col, cache)
	}
	cases := []struct {
		rule  FilterRule
		batch bool
	}{
		{FilterRule{Operator: "contains", Value: "码"}, true},
		{FilterRule{Operator: "notContains", Value: "UP"}, true},
		{FilterRule{Operator: "startsWith", Value: "k"}, true},
		{FilterRule{Operator: "endsWith", Value: "EA"}, true},
		{FilterRule{Operator: "in", Values: []string{"001", "k码", "TRUE"}}, true},
		{FilterRule{Operator: "equals", Value: "groupa"}, true},
		{FilterRule{Operator: "notEquals", Value: "absent"}, true},
		{FilterRule{Operator: "blank"}, true},
		{FilterRule{Operator: "notBlank"}, true},
		{FilterRule{Operator: "equals", Value: "1"}, false},
		{FilterRule{Operator: "gte", Value: "9007199254740993"}, false},
	}
	for _, tc := range cases {
		rule := tc.rule
		rule.Column = 1
		rules := []FilterRule{rule, {Column: 2, Operator: "notEquals", Value: "TRUE"}}
		s, err := newFilterScanner(w, idx, rules, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		filters, err := s.dictionaryFilters(t.Context(), n)
		if err != nil || (len(filters) > 0) != tc.batch {
			t.Fatalf("%s path: %d %v", rule.Operator, len(filters), err)
		}
		for _, start := range []int{0, 1, 63, 64, 65, n - 64, n - 7} {
			const header = 19
			var want, got []int
			for i := start; i < n; i++ {
				if err := s.read(i); err != nil {
					t.Fatal(err)
				}
				if idx.RowAt(i).Number <= header || s.matches() {
					want = append(want, i)
				}
			}
			next := start
			err = s.scan(t.Context(), start, header, func(i int, isHeader bool) error {
				if isHeader != (idx.RowAt(i).Number <= header) {
					t.Fatal("header", i)
				}
				got = append(got, i)
				return nil
			}, func(cursor int) error {
				next++
				if cursor != next {
					t.Fatalf("cursor %d want %d", cursor, next)
				}
				return nil
			})
			if err != nil || next != n || !slices.Equal(got, want) {
				t.Fatalf("%s start %d: got %d want %d, next %d, err %v", rule.Operator, start, len(got), len(want), next, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		err = s.scan(ctx, 0, 0, func(int, bool) error { return nil }, func(next int) error {
			if next == 64 {
				cancel()
			}
			if next > 64 {
				t.Fatal("scan advanced past cancellation")
			}
			return nil
		})
		cancel()
		s.close()
		if err != context.Canceled {
			t.Fatal(err)
		}
	}
}
