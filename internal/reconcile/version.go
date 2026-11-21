package reconcile

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// VersionChange carries source coordinates. A missing side is distinct from a
// present empty cell. Changes are consumed before either row reader advances.
type VersionChange struct {
	Kind                   string
	Ref                    string
	LeftRef, RightRef, Key string
	Before, After          *workbook.CellData
	LeftInfo, RightInfo    string
}

type VersionSheet struct {
	Book  *workbook.Workbook
	Meta  workbook.SheetMeta
	Index *workbook.SheetIndex
}

var errEndVersionRead = errors.New("version comparison reader closed")

func versionRows(ctx context.Context, source VersionSheet, positions iter.Seq[int]) iter.Seq2[workbook.RowData, error] {
	if positions == nil {
		positions = func(next func(int) bool) {
			for i := range source.Index.RowCount() {
				if !next(i) {
					return
				}
			}
		}
	}
	return func(yield func(workbook.RowData, error) bool) {
		if source.Index == nil {
			return
		}
		err := source.Book.VisitRowsWithOptionsContext(ctx, source.Index, positions, 1, formats.ExcelColumns, workbook.RowReadOptions{FormatValue: true, IncludeFormula: true}, func(row workbook.RowData) error {
			byColumn := func(a, b workbook.CellData) int { return cmp.Compare(a.Col, b.Col) }
			if !slices.IsSortedFunc(row.Cells, byColumn) {
				slices.SortFunc(row.Cells, byColumn)
			}
			for i := 1; i < len(row.Cells); i++ {
				if row.Cells[i-1].Col == row.Cells[i].Col {
					return fmt.Errorf("源行 %d 出现重复单元格坐标", row.Number)
				}
			}
			if !yield(row, nil) {
				return errEndVersionRead
			}
			return nil
		})
		if err != nil && !errors.Is(err, errEndVersionRead) {
			yield(workbook.RowData{}, err)
		}
	}
}

func versionCellChanges(a, b *workbook.CellData) string {
	if a == nil {
		return "新增单元格"
	}
	if b == nil {
		return "删除单元格"
	}
	var changes []string
	if a.HasFormula != b.HasFormula || a.Formula != b.Formula {
		changes = append(changes, "公式")
	}
	if (a.HasFormula && a.Formula == "") || (b.HasFormula && b.Formula == "") {
		changes = append(changes, "公式原文待核")
	}
	if a.Type != b.Type || a.SourceType != b.SourceType || a.Currency != b.Currency {
		changes = append(changes, "源类型")
	}
	if a.CacheMissing && b.CacheMissing {
		changes = append(changes, "两侧缺少缓存")
	}
	if a.CacheMissing != b.CacheMissing {
		changes = append(changes, "缓存状态")
	}
	if a.Raw != b.Raw {
		changes = append(changes, "原值")
	} else if a.Display != b.Display {
		changes = append(changes, "显示值")
	}
	return strings.Join(changes, "、")
}

func compareVersionCells(ctx context.Context, row int, left, right []workbook.CellData, emit func(VersionChange) error) error {
	for i, j := 0, 0; i < len(left) || j < len(right); {
		if err := ctx.Err(); err != nil {
			return err
		}
		var before, after *workbook.CellData
		col := 0
		if i < len(left) && (j == len(right) || left[i].Col <= right[j].Col) {
			before = &left[i]
			col = before.Col
			i++
		}
		if j < len(right) && (before == nil || right[j].Col == col) {
			after = &right[j]
			col = after.Col
			j++
		}
		if kind := versionCellChanges(before, after); kind != "" {
			if err := emit(VersionChange{Kind: kind, Ref: workbook.ColumnName(col) + strconv.Itoa(row), Before: before, After: after}); err != nil {
				return err
			}
		}
	}
	return nil
}

// CompareVersionSheets walks existing indexed rows, including blank styled
// rows. Sparse coordinate gaps cost no additional reads or cells.
func CompareVersionSheets(ctx context.Context, left, right VersionSheet, emit func(VersionChange) error, progress func(int)) error {
	nextL, stopL := iter.Pull2(versionRows(ctx, left, nil))
	defer stopL()
	nextR, stopR := iter.Pull2(versionRows(ctx, right, nil))
	defer stopR()
	l, le, hasL := nextL()
	r, re, hasR := nextR()
	visited := 0
	for hasL || hasR {
		if err := ctx.Err(); err != nil {
			return err
		}
		if le != nil {
			return le
		}
		if re != nil {
			return re
		}
		takeL := hasL && (!hasR || l.Number <= r.Number)
		takeR := hasR && (!hasL || r.Number <= l.Number)
		var a, b []workbook.CellData
		row := r.Number
		if takeL {
			a = l.Cells
			row = l.Number
		}
		if takeR {
			b = r.Cells
		}
		if takeL != takeR {
			kind := "新增源行"
			if takeL {
				kind = "删除源行"
			}
			if err := emit(VersionChange{Kind: kind, Ref: strconv.Itoa(row)}); err != nil {
				return err
			}
		}
		if err := compareVersionCells(ctx, row, a, b, emit); err != nil {
			return err
		}
		if takeL {
			l, le, hasL = nextL()
			visited++
		}
		if takeR {
			r, re, hasR = nextR()
			visited++
		}
		if progress != nil && visited&255 < 2 {
			progress(visited)
		}
	}
	if progress != nil {
		progress(visited)
	}
	return ctx.Err()
}
