package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"sort"
	"strconv"

	"excellens/internal/formats"
	"excellens/internal/workbook"
)

// Match business keys with the normal typed external sorter. Only source
// positions are retained between the key pass and the full-fidelity row pass.
func CompareKeyedVersionSheets(ctx context.Context, dir string, sheets [2]VersionSheet, sources [2]Source, keys []Pair, emit func(VersionChange) error, progress Progress) error {
	for side, s := range sheets {
		if s.Index == nil || s.Index.RowCount() > formats.VersionAlignmentRows {
			return errors.New("按主键比较每张表最多支持 1,048,576 条索引记录")
		}
		if sources[side].SourceRows != s.Index.RowCount() {
			return errors.New("主键来源与版本索引记录数不符")
		}
	}
	query := Query{Keys: keys, IncludeEqual: true}
	// 0 = not a unique data row, -1 = deleted, positive = current position + 1.
	pairs := make([]int32, sheets[0].Index.RowCount())
	var added []int
	sinks := Sinks{
		Duplicate: func(_ string, side string, row Record) error {
			right := side == "right"
			key := make([]string, len(keys))
			for i, pair := range keys {
				col := pair.Left
				if right {
					col = pair.Right
				}
				key[i] = row.Values[col].Raw
			}
			change := VersionChange{Kind: "重复主键待核", Key: versionKeyText(key)}
			if right {
				change.RightRef = strconv.Itoa(row.Number)
			} else {
				change.LeftRef = strconv.Itoa(row.Number)
			}
			return emit(change)
		},
		Group: func(group Group) error {
			switch group.Status {
			case "invalid_key":
				d := VersionChange{Kind: "无效主键待核", Key: versionKeyText(group.Key)}
				if group.Left != nil {
					d.LeftRef, d.LeftInfo = strconv.Itoa(group.Left.Number), group.Reason
				} else {
					d.RightRef, d.RightInfo = strconv.Itoa(group.Right.Number), group.Reason
				}
				return emit(d)
			case "left_only":
				pairs[group.Left.Position] = -1
			case "right_only":
				added = append(added, group.Right.Position)
			case "equal":
				pairs[group.Left.Position] = int32(group.Right.Position + 1)
			}
			return nil
		},
	}
	if _, err := Run(ctx, dir, query, sources[0], sources[1], sinks, progress); err != nil {
		return err
	}
	// The rows above and including each header keep their physical coordinates.
	prefix := sheets
	for i := range prefix {
		idx := sheets[i].Index
		prefix[i].Index = idx.FirstRows(idx.FindRowOffset(sources[i].Info.HeaderRow + 1))
	}
	if err := CompareVersionSheets(ctx, prefix[0], prefix[1], emit, nil); err != nil {
		return err
	}
	stable, err := stableVersionPositions(ctx, pairs)
	if err != nil {
		return err
	}
	if progress != nil {
		progress("比较已对齐的业务行", 0, int64(len(pairs)))
	}
	positions := func(right bool) iter.Seq[int] {
		return func(yield func(int) bool) {
			for i, mapped := range pairs {
				if mapped <= 0 {
					continue
				}
				pos := i
				if right {
					pos = int(mapped) - 1
				}
				if !yield(pos) {
					return
				}
			}
		}
	}
	next, stop := iter.Pull2(versionRows(ctx, sheets[1], positions(true)))
	defer stop()
	for before, err := range versionRows(ctx, sheets[0], positions(false)) {
		if err != nil {
			return err
		}
		after, err, ok := next()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("主键对应的当期源行缺失")
		}
		pos := sheets[0].Index.FindRowOffset(before.Number)
		if pos >= len(pairs) || pairs[pos] <= 0 || after.Number != sheets[1].Index.RowAt(int(pairs[pos])-1).Number {
			return errors.New("主键与源行坐标不一致")
		}
		kind := ""
		if stable != nil && !stable[pos] {
			kind = "业务行移位"
		}
		if err := emitVersionPair(ctx, &before, &after, kind, keys, emit); err != nil {
			return err
		}
		if progress != nil && pos&4095 == 0 {
			progress("比较已对齐的业务行", int64(pos+1), int64(len(pairs)))
		}
	}
	if _, err, more := next(); err != nil {
		return err
	} else if more {
		return errors.New("主键对应的基线源行缺失")
	}
	deleted := func(yield func(int) bool) {
		for i, pos := range pairs {
			if pos < 0 && !yield(i) {
				return
			}
		}
	}
	for row, err := range versionRows(ctx, sheets[0], deleted) {
		if err != nil {
			return err
		}
		if err := emitVersionPair(ctx, &row, nil, "删除业务行", keys, emit); err != nil {
			return err
		}
	}
	slices.Sort(added)
	for row, err := range versionRows(ctx, sheets[1], slices.Values(added)) {
		if err != nil {
			return err
		}
		if err := emitVersionPair(ctx, nil, &row, "新增业务行", keys, emit); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func versionKeyText(values []string) string {
	encoded, _ := json.Marshal(values)
	return string(encoded)
}

func emitVersionPair(ctx context.Context, before, after *workbook.RowData, kind string, keys []Pair, emit func(VersionChange) error) error {
	var l, r []workbook.CellData
	var leftRow, rightRow string
	if before != nil {
		l = before.Cells
		leftRow = strconv.Itoa(before.Number)
	}
	if after != nil {
		r = after.Cells
		rightRow = strconv.Itoa(after.Number)
	}
	var key string
	withKey := func(d VersionChange) error {
		if key == "" {
			values := make([]string, len(keys))
			for i, pair := range keys {
				cells, col := l, pair.Left
				if before == nil {
					cells, col = r, pair.Right
				}
				j := sort.Search(len(cells), func(j int) bool { return cells[j].Col >= col })
				if j < len(cells) && cells[j].Col == col {
					values[i] = cells[j].Raw
				}
			}
			key = versionKeyText(values)
		}
		d.Key = key
		if d.Before != nil {
			d.LeftRef = d.Before.Ref
		}
		if d.After != nil {
			d.RightRef = d.After.Ref
		}
		return emit(d)
	}
	if kind != "" {
		if err := withKey(VersionChange{Kind: kind, LeftRef: leftRow, RightRef: rightRow}); err != nil {
			return err
		}
	}
	row := 0
	if before != nil {
		row = before.Number
	} else {
		row = after.Number
	}
	return compareVersionCells(ctx, row, l, r, withKey)
}

// Rows inside a longest increasing subsequence keep their relative order.
// An insertion alone therefore shifts no business rows. Ties use the smaller
// current position, so the same inputs always give the same minimal move set.
func stableVersionPositions(ctx context.Context, pairs []int32) ([]bool, error) {
	var last int32
	ordered := true
	for _, p := range pairs {
		if p > 0 {
			if p < last {
				ordered = false
				break
			}
			last = p
		}
	}
	if ordered {
		return nil, ctx.Err()
	}
	previous := make([]int32, len(pairs))
	var tails []int32
	for i, p := range pairs {
		if i&4095 == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if p <= 0 {
			continue
		}
		j := sort.Search(len(tails), func(j int) bool { return pairs[tails[j]] >= p })
		if j > 0 {
			previous[i] = tails[j-1] + 1
		}
		if j == len(tails) {
			tails = append(tails, int32(i))
		} else {
			tails[j] = int32(i)
		}
	}
	stable := make([]bool, len(pairs))
	for i := tails[len(tails)-1] + 1; i > 0; i = previous[i-1] {
		stable[i-1] = true
	}
	return stable, nil
}
