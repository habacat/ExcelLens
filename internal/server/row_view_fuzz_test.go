package server

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"excellens/internal/workbook"
)

// Compare mixed frozen/source/sorted coordinates with an independent small
// literal sheet. In particular, pinned rows are visible even when unmatched,
// while hidden pinned rows must never reappear in a cross-pane selection.
func FuzzRowViewSelectionMatchesLiteralVisibleSheet(f *testing.F) {
	f.Add([]byte{17, 1, 6, 3, 4, 5, 0, 9})
	f.Add([]byte{31, 255, 0})
	f.Add([]byte{3, 0, 30})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1024 {
			return
		}
		at := func(i int) byte {
			if len(input) == 0 {
				return 0
			}
			return input[i%len(input)]
		}
		n, through := int(at(0)%40)+1, int(at(2)%31)
		hidden := make([]bool, n+1)
		layout := workbook.SourceLayout{}
		idx := &workbook.SheetIndex{SourceFormat: "csv", MaxRow: n}
		for row := 1; row <= n; row++ {
			hidden[row] = at(row+3)&1 != 0
			if hidden[row] {
				layout.RowStates = append(layout.RowStates, workbook.SourceAxisRange{Min: row, Max: row, Hidden: true, HiddenSet: true})
			}
			if row == 1 || row == n || at(row+4)&2 == 0 {
				idx.Rows = append(idx.Rows, workbook.RowOffset{Number: row})
			}
		}
		axis, err := layout.BuildAxisVisibility(context.Background(), "row", n, workbook.AxisViewSettings{})
		if err != nil {
			t.Fatal(err)
		}
		positions := []int{}
		for i := 0; i < idx.RowCount(); i++ {
			if at(i+5)&4 == 0 {
				positions = append(positions, i)
			}
		}
		sorted := at(1)&2 != 0
		if sorted {
			for i := len(positions) - 1; i > 0; i-- {
				j := int(at(i+6)) % (i + 1)
				positions[i], positions[j] = positions[j], positions[i]
			}
		}
		base := rowOrderFixture(t, idx.RowCount(), positions...)
		projection, err := workbook.NewRowProjection(context.Background(), t.TempDir(), idx, base, axis, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, filtered := range []bool{false, true} {
			data := &sheetRowViewData{id: "literal", idx: idx, visibility: axis}
			if filtered {
				data.projection, data.sorted = projection, sorted
			}
			lease := ownSheetRowView(data)
			if filtered {
				defer lease.close()
			} else {
				t.Cleanup(lease.close)
			}
			view, err := newSelectionRowView(idx, nil, through, lease)
			if err != nil {
				t.Fatal(err)
			}
			pinned, body := []int{}, []int{}
			for row := 1; row <= min(n, through); row++ {
				if !hidden[row] {
					pinned = append(pinned, row)
				}
			}
			if filtered {
				for _, position := range positions {
					row := idx.RowAt(position).Number
					if row > through && !hidden[row] {
						body = append(body, row)
					}
				}
			} else {
				for row := through + 1; row <= n; row++ {
					if !hidden[row] {
						body = append(body, row)
					}
				}
			}
			if view.count() != len(body) {
				t.Fatal("visible count", filtered, view.count(), body)
			}
			page, err := view.numbersPage(0, n)
			if err != nil || !slices.Equal(page, body) {
				t.Fatal("visible page", filtered, page, body, err)
			}
			end, err := view.numbersPage(len(body), n)
			if err != nil || len(end) != 0 {
				t.Fatal("end page must be empty without rank/select", end, err)
			}
			for row := through + 1; row <= n; row++ {
				index, found := view.locate(row)
				want := slices.Index(body, row)
				if found != (want >= 0) || found && index != want {
					t.Fatal("locate", filtered, row, index, found, body)
				}
			}
			full := append(slices.Clone(pinned), body...)
			if plan := view.fullPlan(); plan.Len() != len(full) || !slices.Equal(slices.Collect(plan.Iter()), full) {
				t.Fatal("full visible stream differs", filtered, full)
			}
			if len(full) == 0 {
				continue
			}
			for sample := 0; sample < 8; sample++ {
				one, two := int(at(sample+7))%len(full), int(at(sample+13))%len(full)
				visual := func(index int) int {
					if index < len(pinned) {
						return full[index]
					}
					return index - len(pinned) + 1
				}
				rows, err := view.rows(full[one], full[two], visual(one), visual(two), n)
				want := full[min(one, two) : max(one, two)+1]
				if err != nil || !reflect.DeepEqual(rows, want) {
					t.Fatal("selection", filtered, through, full, one, two, rows, want, err)
				}
				plan, err := view.plan(full[one], full[two], visual(one), visual(two))
				if err != nil || plan.Len() != len(want) || !slices.Equal(slices.Collect(plan.Iter()), want) {
					t.Fatal("selection stream differs", err)
				}
				for ref := range plan.Rows() {
					position := idx.FindRowOffset(ref.Number)
					wantPosition := -1
					if position < idx.RowCount() && idx.RowAt(position).Number == ref.Number {
						wantPosition = position
					}
					if ref.Position != wantPosition {
						t.Fatal("stream source position differs", ref, wantPosition)
					}
				}
				if _, err := view.rows(full[one], full[two], visual(one), visual(two), len(want)-1); err == nil {
					t.Fatal("selection allocation limit ignored")
				}
			}
		}
	})
}
