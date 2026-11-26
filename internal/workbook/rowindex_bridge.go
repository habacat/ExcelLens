package workbook

import (
	"context"

	"excellens/internal/rowindex"
)

const (
	MaxResidentRowOffsets = rowindex.MaxResidentRowOffsets
	diskRowOffsetBytes    = rowindex.DiskRowOffsetBytes
	residentOrderRows     = rowindex.ResidentOrderRows
)

type (
	RowOffset              = rowindex.RowOffset
	odsRowSpan             = rowindex.ODSRowSpan
	RowIndex               = rowindex.RowIndex
	RowOrder               = rowindex.RowOrder
	RowSet                 = rowindex.RowSet
	RowProjection          = rowindex.RowProjection
	AxisVisibility         = rowindex.AxisVisibility
	AxisOutlineGroup       = rowindex.AxisOutlineGroup
	AxisViewSettings       = rowindex.AxisViewSettings
	AxisInterval           = rowindex.AxisInterval
	AxisVisibilityOverride = rowindex.AxisVisibilityOverride
	AxisOutlineKey         = rowindex.AxisOutlineKey
	AxisOutlineOverride    = rowindex.AxisOutlineOverride
	SourceAxisRange        = rowindex.SourceAxisRange
	SourceSizeRange        = rowindex.SourceSizeRange
	KeyRecord              = rowindex.KeyRecord
	KeyScan                = rowindex.KeyScan
	sortValue              = rowindex.SortValue
)

func NewRowIndex(rows []RowOffset) RowIndex {
	return rowindex.NewRowIndex(rows)
}

// FirstRows limits indexed positions while retaining source coordinates,
// worksheet metadata and shared formulas. Both storage modes share their data.
func (idx SheetIndex) FirstRows(limit int) *SheetIndex {
	idx.RowIndex = idx.RowIndex.Truncate(limit)
	return &idx
}

func QueryFilesInUse(dir string) bool {
	return rowindex.QueryFilesInUse(dir)
}

func NewRowOrder(ctx context.Context, dir string, sourceRows int, positions []int) (*RowOrder, error) {
	return rowindex.NewRowOrder(ctx, dir, sourceRows, positions)
}

func BuildRowOrder(ctx context.Context, dir string, sourceRows int, scan func(context.Context, func(int) error) error) (*RowOrder, error) {
	return rowindex.BuildRowOrder(ctx, dir, sourceRows, scan)
}

func NewRowProjection(ctx context.Context, dir string, idx *SheetIndex, order *RowOrder, visibility *AxisVisibility, excludeThrough int, progress func(done, total int)) (*RowProjection, error) {
	if idx == nil {
		return rowindex.NewRowProjection(ctx, dir, nil, 0, order, visibility, excludeThrough, progress)
	}
	return rowindex.NewRowProjection(ctx, dir, &idx.RowIndex, idx.MaxRow, order, visibility, excludeThrough, progress)
}

func (s SourceLayout) BuildAxisVisibility(ctx context.Context, axis string, maximum int, settings AxisViewSettings) (*AxisVisibility, error) {
	return rowindex.BuildAxisVisibility(ctx, rowindex.AxisLayoutInput{
		Rows:               s.Rows,
		Cols:               s.Cols,
		RowStates:          s.RowStates,
		ColStates:          s.ColStates,
		ZeroHeight:         s.ZeroHeight,
		DefaultRowZeroSize: s.DefaultRowZeroSize,
		DefaultColZeroSize: s.DefaultColZeroSize,
		SummaryBelow:       s.SummaryBelow,
		SummaryRight:       s.SummaryRight,
		StateNotice:        s.StateNotice,
	}, axis, maximum, settings)
}

func FoldComparisonText(text string) string {
	return rowindex.FoldComparisonText(text)
}

func SortKeyStream(ctx context.Context, dir string, sourceRows int, budget int64, scan KeyScan, emit func(KeyRecord) error, phase func(string, int, int)) error {
	return rowindex.SortKeyStream(ctx, dir, sourceRows, budget, scan, emit, phase)
}

func SortRationalKeyStream(ctx context.Context, dir string, sourceRows int, budget int64, scan KeyScan, emit func(KeyRecord) error, phase func(string, int, int)) error {
	return rowindex.SortRationalKeyStream(ctx, dir, sourceRows, budget, scan, emit, phase)
}
