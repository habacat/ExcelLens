package workbook

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strings"

	"excellens/internal/formats"
)

// KeyRecord is a projected, source-index position and an opaque comparison key.
// The caller owns key semantics; this primitive never folds, parses or changes
// them. Positions must arrive in increasing source order and are unique.
type KeyRecord struct {
	Position int
	Key      string
}

type KeyScan func(context.Context, func(KeyRecord) error) error

// FoldComparisonText has the same Unicode simple-fold equality as the column
// filter, including long s and Kelvin sign, rather than lowercase-only equality.
func FoldComparisonText(text string) string { return filterFoldKey(text) }

// SortKeyStream shares the filter's verified run format, quotas and bounded
// merging. Keys are borrowed only for emit's duration. No reverse row map or
// whole-table slice is constructed for a merge join's sequential consumer.
func SortKeyStream(ctx context.Context, dir string, sourceRows int, budget int64, scan KeyScan, emit func(KeyRecord) error, phase func(string, int, int)) error {
	return sortProjectedKeyStream(ctx, dir, sourceRows, budget, scan, emit, phase, false)
}

// SortRationalKeyStream sorts internally prepared exact numbers. This is not
// a source-text parser: keys contain only bounded integer numerator/denominator
// syntax, supplied after the caller has applied its own numeric eligibility.
// Saturated monotonic float hints share the existing exact tie comparator.
func SortRationalKeyStream(ctx context.Context, dir string, sourceRows int, budget int64, scan KeyScan, emit func(KeyRecord) error, phase func(string, int, int)) error {
	return sortProjectedKeyStream(ctx, dir, sourceRows, budget, scan, emit, phase, true)
}

func sortProjectedKeyStream(ctx context.Context, dir string, sourceRows int, budget int64, scan KeyScan, emit func(KeyRecord) error, phase func(string, int, int), rational bool) error {
	if sourceRows < 0 || sourceRows > formats.ExternalRows || budget > formats.QuerySortMemoryBytes || scan == nil || emit == nil {
		return errors.New("投影排序参数或容量无效")
	}
	sorter, err := newRowSorter(dir, sourceRows, []SortKey{{Column: 1, Mode: "text"}}, budget, &processQueryQuota)
	if err != nil {
		return err
	}
	defer sorter.abort()
	var values [1]sortValue
	if err := scan(ctx, func(record KeyRecord) error {
		if len(record.Key) == 0 || int64(len(record.Key))+25 > sorter.maxRecord {
			return errors.New("投影排序键为空或超过单行预算")
		}
		key := strings.Clone(record.Key)
		if rational {
			number, ok := rationalKeyNumber(key)
			if !ok {
				return errors.New("精确排序键无效或超出容量")
			}
			values[0] = sortValue{text: key, number: number, isNumber: true}
		} else {
			values[0] = sortValue{text: key, folded: key}
		}
		return sorter.add(ctx, record.Position, values[:])
	}); err != nil {
		return err
	}
	clear(values[:])
	return sorter.finishRecords(ctx, func(record sortRecord) error {
		return emit(KeyRecord{Position: record.index, Key: record.values[0].text})
	}, phase)
}

func rationalKeyNumber(key string) (float64, bool) {
	if len(key) == 0 || len(key) > formats.RationalSortKeyBytes {
		return 0, false
	}
	slashes, digits := 0, 0
	for at, c := range key {
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '-' && at == 0:
		case c == '/' && slashes == 0 && digits > 0:
			slashes++
			digits = 0
		default:
			return 0, false
		}
	}
	if digits == 0 {
		return 0, false
	}
	number, ok := new(big.Rat).SetString(key)
	if !ok {
		return 0, false
	}
	f, _ := number.Float64()
	if math.IsInf(f, 0) {
		f = math.Copysign(math.MaxFloat64, f)
	}
	return f, true
}
