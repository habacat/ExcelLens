package server

import (
	"math/rand"
	"reflect"
	"testing"

	"excellens/internal/workbook"
)

func TestSelectionRowViewMapsSmallRangesWithoutCopyingWholeFilter(t *testing.T) {
	idx := &workbook.SheetIndex{Rows: make([]workbook.RowOffset, 100)}
	for i := range idx.Rows {
		idx.Rows[i].Number = i + 1
	}
	rng := rand.New(rand.NewSource(20261115))
	for sample := 0; sample < 50; sample++ {
		order := rng.Perm(100)
		filter := &FilterResult{Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), order...)}
		for _, through := range []int{0, 1, 3, 30} {
			view, err := newSelectionRowView(idx, filter, through)
			if err != nil {
				t.Fatal(err)
			}
			var visible []int
			for _, offset := range order {
				if offset+1 > through {
					visible = append(visible, offset+1)
				}
			}
			for lo := 1; lo <= len(visible); lo++ {
				hi := min(lo+4, len(visible))
				rows, err := view.rows(visible[lo-1], visible[hi-1], lo, hi, 5)
				if err != nil || !reflect.DeepEqual(rows, visible[lo-1:hi]) {
					t.Fatalf("through=%d visual=%d:%d rows=%v err=%v", through, lo, hi, rows, err)
				}
			}
		}
	}
}

func TestSourceSelectionRowsFrozenReversedAndSparse(t *testing.T) {
	idx := &workbook.SheetIndex{Rows: []workbook.RowOffset{{Number: 1}, {Number: 4}, {Number: 8}, {Number: 9}}}
	filter := &FilterResult{Sorted: true, Rows: rowOrderFixture(t, idx.RowCount(), 2, 0, 1)}
	for _, tc := range []struct {
		row1, row2, visual1, visual2, through int
		want                                  []int
	}{
		{8, 4, 1, 2, 3, []int{8, 4}},
		{4, 8, 2, 1, 3, []int{8, 4}},
		{2, 4, 0, 2, 3, []int{2, 3, 8, 4}},
		{4, 2, 2, 0, 3, []int{2, 3, 8, 4}},
		{3, 2, 0, 0, 3, []int{2, 3}},
	} {
		got, err := sourceSelectionRows(idx, filter, tc.row1, tc.row2, tc.visual1, tc.visual2, tc.through, 10)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%+v: rows=%v err=%v", tc, got, err)
		}
	}
	for _, args := range [][6]int{{8, 4, 1, 3, 3, 10}, {2, 4, 0, 0, 3, 10}, {8, 4, 1, 2, 3, 1}, {1, 1000000000, 0, 0, 0, 250}} {
		if _, err := sourceSelectionRows(idx, filter, args[0], args[1], args[2], args[3], args[4], args[5]); err == nil {
			t.Fatalf("accepted invalid/budget-exceeding selection %v", args)
		}
	}
}
