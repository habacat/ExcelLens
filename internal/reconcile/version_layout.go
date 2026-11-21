package reconcile

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"excellens/internal/workbook"
)

type layoutProperty struct{ key, value string }

func sheetProperties(layout workbook.SourceLayout) []layoutProperty {
	var props []layoutProperty
	add := func(key string, value any) {
		raw, _ := json.Marshal(value)
		props = append(props, layoutProperty{key, string(raw)})
	}
	ref := func(row, col int) string { return workbook.ColumnName(col) + strconv.Itoa(row) }
	for _, m := range layout.Merges {
		add("合并 "+ref(m.Row, m.Col), ref(m.EndRow, m.EndCol))
	}
	for _, s := range layout.Rows {
		add(fmt.Sprintf("行高 %d:%d", s.Min, s.Max), s.Size)
	}
	for col, width := range layout.Cols {
		add("列宽 "+workbook.ColumnName(col), width)
	}
	for _, s := range layout.RowStates {
		add(fmt.Sprintf("源行状态 %d:%d", s.Min, s.Max), s)
	}
	for _, s := range layout.ColStates {
		add(fmt.Sprintf("源列状态 %s:%s", workbook.ColumnName(s.Min), workbook.ColumnName(s.Max)), s)
	}
	if layout.DefaultRow != 0 {
		add("默认行高", layout.DefaultRow)
	}
	if layout.DefaultCol != 0 {
		add("默认列宽", layout.DefaultCol)
	}
	if layout.GridLines != nil {
		add("源网格线", *layout.GridLines)
	}
	if layout.ZeroHeight || layout.DefaultRowZeroSize {
		add("默认零行高", true)
	}
	if layout.DefaultColZeroSize {
		add("默认零列宽", true)
	}
	slices.SortFunc(props, func(x, y layoutProperty) int { return cmp.Compare(x.key, y.key) })
	return props
}
func CompareVersionLayout(ctx context.Context, left, right VersionSheet, emit func(VersionChange) error) error {
	var sets [2][]layoutProperty
	for i, sheet := range [2]VersionSheet{left, right} {
		if sheet.Index == nil {
			continue
		}
		layout, err := sheet.Book.SourceLayoutContext(ctx, sheet.Meta, sheet.Index)
		if err != nil {
			return err
		}
		sets[i] = sheetProperties(layout)
	}
	a, b := sets[0], sets[1]
	for i, j := 0, 0; i < len(a) || j < len(b); {
		if err := ctx.Err(); err != nil {
			return err
		}
		var key, x, y string
		if i < len(a) && (j == len(b) || a[i].key <= b[j].key) {
			key, x = a[i].key, a[i].value
			i++
		}
		if j < len(b) && (key == "" || b[j].key == key) {
			key, y = b[j].key, b[j].value
			j++
		}
		if x != y {
			if err := emit(VersionChange{Kind: "源布局", Ref: key, LeftInfo: x, RightInfo: y}); err != nil {
				return err
			}
		}
	}
	return nil
}
